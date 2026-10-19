package agent_test

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

// gate is a model step that signals it started and waits for release.
type gate struct {
	started chan struct{}
	release chan struct{}
}

func newGate() *gate { return &gate{started: make(chan struct{}), release: make(chan struct{})} }

func (g *gate) step(text string) agenttest.Step {
	return func(context.Context, anthropic.MessageNewParams) (*anthropic.Message, error) {
		close(g.started)
		<-g.release
		return agenttest.Message("end_turn", agenttest.Text(text)), nil
	}
}

func inThread(eventID, thread, text string) agent.Inbound {
	in := dm(eventID, text)
	in.ThreadTS, in.TS = thread, thread
	return in
}

func answers(n int) []agenttest.Step {
	out := make([]agenttest.Step, n)
	for i := range out {
		out[i] = agenttest.Answer(fmt.Sprintf("answer %d", i+1))
	}
	return out
}

// covers: AC-13. Messages in one thread run in order, the second seeing the
// first's history, and Submit returns at once while a turn runs.
func TestOneThreadRunsInOrder(t *testing.T) {
	g := newGate()
	h := newHarness(t, agenttest.NewModel(g.step("first"), agenttest.Answer("second")))
	h.a.Submit(dm("Ev1", "first message"))
	<-g.started
	start := time.Now()
	h.a.Submit(dm("Ev2", "second message"))
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("Submit blocked for %v", d)
	}
	close(g.release)
	h.a.Wait()

	calls := h.model.Calls()
	if len(calls) != 2 || len(calls[1].Messages) != 3 {
		t.Fatalf("second turn saw %d messages, want 3 (user, assistant, user)", len(calls[1].Messages))
	}
	posts := h.poster.Texts()
	if !strings.HasPrefix(posts[1], "first") || !strings.HasPrefix(posts[3], "second") {
		t.Errorf("posts out of order: %q", posts)
	}
}

// covers: AC-13. Ten messages may wait behind a running turn; the eleventh gets busy.
func TestThreadQueueCap(t *testing.T) {
	g := newGate()
	// Eleven turns at 3600 scripted tokens each would hit the default budget.
	h := newHarness(t, agenttest.NewModel(append([]agenttest.Step{g.step("first")}, answers(10)...)...),
		withConfig(func(c *agent.Config) { c.MaxTokensPerTicket = 1_000_000 }))
	h.a.Submit(dm("Ev0", "first"))
	<-g.started
	for i := 1; i <= 11; i++ {
		h.a.Submit(dm(fmt.Sprintf("Ev%d", i), "again"))
	}
	close(g.release)
	h.a.Wait()

	busy := 0
	for _, p := range h.poster.Texts() {
		if p == agent.TextBusy {
			busy++
		}
	}
	if busy != 1 {
		t.Errorf("%d busy replies, want 1", busy)
	}
	if n := len(h.model.Calls()); n != 11 {
		t.Errorf("%d turns ran, want 11", n)
	}
	if !strings.Contains(h.logs.String(), `"msg":"agent_thread_busy","event_id":"Ev11"`) {
		t.Errorf("logs lack agent_thread_busy:\n%s", h.logs.String())
	}
}

// covers: AC-13. Five threads with AGENT_MAX_CONCURRENT 4 never run more than 4 turns at once.
func TestConcurrencyCap(t *testing.T) {
	var active, peak atomic.Int32
	step := func(context.Context, anthropic.MessageNewParams) (*anthropic.Message, error) {
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		active.Add(-1)
		return agenttest.Message("end_turn", agenttest.Text("ok")), nil
	}
	steps := make([]agenttest.Step, 5)
	for i := range steps {
		steps[i] = step
	}
	h := newHarness(t, agenttest.NewModel(steps...))
	for i := range 5 {
		h.a.Submit(inThread(fmt.Sprintf("Ev%d", i), fmt.Sprintf("179240000%d.000100", i), "hello"))
	}
	h.a.Wait()
	if p := peak.Load(); p != 4 {
		t.Errorf("peak concurrent turns = %d, want 4", p)
	}
}

// covers: AC-13. On shutdown, waiting messages get the shutdown text and a
// running turn finishes within the grace.
func TestShutdownDrain(t *testing.T) {
	g := newGate()
	h := newHarness(t, agenttest.NewModel(g.step("done")))
	ctx, cancel := context.WithCancel(t.Context())
	ran := make(chan error, 1)
	go func() { ran <- h.a.Run(ctx) }()

	h.a.Submit(dm("Ev1", "first"))
	<-g.started
	h.a.Submit(dm("Ev2", "second"))
	h.a.Submit(dm("Ev3", "third"))
	cancel()
	waitFor(t, func() bool { return count(h.poster.Texts(), agent.TextShutdown) == 2 })
	close(g.release)
	if err := <-ran; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if !contains(h.poster.Texts(), answerWith("done", agent.FooterOpen)) {
		t.Errorf("the running turn did not finish: %q", h.poster.Texts())
	}
	h.a.Submit(dm("Ev4", "after"))
	waitFor(t, func() bool { return count(h.poster.Texts(), agent.TextShutdown) == 3 })
}

// covers: AC-13. A turn still running when the grace runs out is cancelled.
func TestShutdownGraceCancels(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(agenttest.Hang()), withConfig(func(c *agent.Config) { c.ShutdownGrace = 100 * time.Millisecond }))
	ctx, cancel := context.WithCancel(t.Context())
	ran := make(chan error, 1)
	go func() { ran <- h.a.Run(ctx) }()
	h.a.Submit(dm("Ev1", "first"))
	waitFor(t, func() bool { return len(h.model.Calls()) == 1 })
	start := time.Now()
	cancel()
	if err := <-ran; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Run took %v after the signal, want about the grace", d)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func count(xs []string, x string) int {
	n := 0
	for _, s := range xs {
		if s == x {
			n++
		}
	}
	return n
}

func contains(xs []string, x string) bool { return count(xs, x) > 0 }
