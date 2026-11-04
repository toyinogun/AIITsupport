package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

// covers: AC-3 (spec 0018). WaitIdle blocks while a turn runs and returns once
// the worker is done; a thread that never had a turn is settled at once; a
// context that ends first gives the context error.
func TestWaitIdle(t *testing.T) {
	g := newGate()
	h := newHarness(t, agenttest.NewModel(g.step("done")))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.a.WaitIdle(ctx, janDM, "9999.000100"); err != nil {
		t.Fatalf("a thread with no turn: %v", err)
	}

	h.a.Submit(dm("Ev1", "hello"))
	<-g.started
	short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if err := h.a.WaitIdle(short, janDM, firstTS); err == nil {
		t.Fatal("WaitIdle returned while the turn was running")
	}

	close(g.release)
	if err := h.a.WaitIdle(ctx, janDM, firstTS); err != nil {
		t.Fatalf("after the turn: %v", err)
	}
	if n := len(h.poster.Texts()); n == 0 {
		t.Error("WaitIdle returned before the reply was posted")
	}
	h.a.Wait()
}
