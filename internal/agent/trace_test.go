package agent_test

import (
	"context"
	"regexp"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

var traceIDShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

// traceSpy is a tool handler that remembers the trace id of each call's context.
type traceSpy struct {
	mu  sync.Mutex
	ids []string
}

func (s *traceSpy) handler(ctx context.Context, _ agenttest.ToolCall) (mcpclient.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, trace.SpanContextFromContext(ctx).TraceID().String())
	return agenttest.OK("Account unlocked and confirmed.", map[string]any{"was_locked": true, "locked": false}), nil
}

// covers: AC-1, AC-2. Two turns of one thread, a tool call in each, share one
// trace id: the stored one, on both agent.turn spans, every llm.call, and
// the context each tool call receives.
func TestConversationIsOneTrace(t *testing.T) {
	rec := recorder()
	before := len(rec.Ended())
	spy := &traceSpy{}
	tools := agenttest.NewTools(janUser, true)
	tools.Handlers["account_unlock"] = spy.handler

	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "account_unlock", nil), agenttest.Answer("first"),
		agenttest.CallTool("t2", "account_unlock", nil), agenttest.Answer("second"),
	), withTools(tools))
	h.send(dm("Ev1", "I'm locked out"))
	h.send(dm("Ev2", "still locked out"))

	want := h.conv().TraceID
	if !traceIDShape.MatchString(want) {
		t.Fatalf("stored trace id %q, want 32 lowercase hex characters", want)
	}
	var turns, calls int
	for _, s := range rec.Ended()[before:] {
		switch s.Name() {
		case "agent.turn":
			turns++
		case "llm.call":
			calls++
		default:
			continue
		}
		if got := s.SpanContext().TraceID().String(); got != want {
			t.Errorf("%s span in trace %s, want %s", s.Name(), got, want)
		}
	}
	if turns != 2 || calls < 4 {
		t.Errorf("%d agent.turn and %d llm.call spans, want 2 and at least 4", turns, calls)
	}
	if len(spy.ids) != 2 {
		t.Fatalf("%d tool calls seen, want 2", len(spy.ids))
	}
	for _, id := range spy.ids {
		if id != want {
			t.Errorf("tool call context in trace %s, want %s", id, want)
		}
	}
}

// covers: AC-1. A failed trace lookup does not stop the turn.
func TestTurnSurvivesThreadTraceFailure(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(agenttest.Answer("hello")))
	h.store.FailOn("ThreadTrace")
	h.send(dm("Ev1", "hi"))
	if len(h.poster.Texts()) == 0 {
		t.Fatal("no reply posted after a trace lookup failure")
	}
}
