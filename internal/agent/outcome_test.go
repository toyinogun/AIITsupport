package agent_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

func (h *harness) wantOutcome(want string) {
	h.t.Helper()
	c := h.conv()
	if c.Outcome != want {
		h.t.Errorf("conversation %s with outcome %q, want %q", c.Status, c.Outcome, want)
	}
}

// covers: spec 0016 AC-6. A closed ticket and a sent reset link both give
// resolved.
func TestOutcomeResolved(t *testing.T) {
	closed := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "x", "body": "y"}),
		agenttest.CallTool("t2", "ticket_update", map[string]any{"note": "n", "state": "closed"}),
		agenttest.Answer("done"),
	))
	closed.send(dm("Ev1", "locked out"))
	closed.wantOutcome("resolved")

	link := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "x", "body": "y"}),
		agenttest.CallTool("t2", "password_reset_link", nil),
		agenttest.Answer("sent"),
	))
	link.tools.Set("password_reset_link", agenttest.Respond(agenttest.OK("Link made.", map[string]any{
		"reset_url": "https://reset.example/CANARY", "expires_in_minutes": float64(15),
	})))
	link.send(dm("Ev1", "forgot password"))
	link.wantStatus(agent.StatusResolved)
	link.wantOutcome("resolved")
}

// covers: spec 0016 AC-6. The model's own escalate takes its outcome from
// the call's reason argument: privileged_request is refused, any other
// reason is escalated. So does the backstop (privileged) and the failure path.
func TestOutcomeEscalationReasons(t *testing.T) {
	for _, c := range []struct {
		name, reason, want string
	}{
		{"privileged", "privileged_request", "refused"},
		{"other", "other", "escalated"},
		{"security", "security_concern", "escalated"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, agenttest.NewModel(escalateCall("t1", c.reason, "summary"), agenttest.Answer("told IT")))
			h.send(dm("Ev1", "help"))
			h.wantStatus(agent.StatusEscalated)
			h.wantOutcome(c.want)
		})
	}

	backstop := newHarness(t, agenttest.NewModel(privilegedCall("t1"), agenttest.Answer("can't do that")))
	backstop.send(dm("Ev1", "make me admin"))
	backstop.wantOutcome("refused")

	failed := newHarness(t, agenttest.NewModel(agenttest.Fail(errors.New("529 overloaded"))))
	failed.send(dm("Ev1", "help"))
	failed.wantOutcome("escalated")

	blocked := newHarness(t, agenttest.NewModel(privilegedCall("t1"), agenttest.Fail(errors.New("529 overloaded"))))
	blocked.send(dm("Ev1", "make me admin"))
	blocked.wantStatus(agent.StatusEscalated)
	blocked.wantOutcome("refused")
}

// covers: spec 0016 AC-6. The gate, directory match, and hourly cap
// refusals are escalated.
func TestOutcomeGateRefusal(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.users.Set("U0GUEST", agent.SlackUser{TeamID: agenttest.Team, IsRestricted: true, IsEmailConfirmed: true, Email: "guest@example.com"})
	in := dm("Ev1", "hi")
	in.UserID = "U0GUEST"
	h.send(in)
	h.wantStatus(agent.StatusEscalated)
	h.wantOutcome("escalated")
}

// covers: spec 0016 AC-6. The sweep works out its reason from the stored
// history before it claims the row, so a blocked privileged request is
// refused and any other interrupted conversation is escalated.
func TestOutcomeSweep(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	old := now.Add(-10 * time.Minute)
	user := agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"text","text":"make me admin"}]`)}
	blocked := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"privileged_access_change","input":{"summary":"x"}}]`)}
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: janUser}, old, user)
	result := agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"blocked: Privileged changes are never made by the assistant."}]}]`)}
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "2.0", RequesterSlackID: janSlack, DirectoryAccount: janUser}, old, user, blocked, result)
	h.a.Sweep(t.Context())
	h.a.Wait()
	for thread, want := range map[string]string{"1.0": "escalated", "2.0": "refused"} {
		c, _ := h.store.Conversation(janDM, thread)
		if c.Status != agent.StatusEscalated || c.Outcome != want {
			t.Errorf("thread %s: %s with outcome %q, want escalated with %q", thread, c.Status, c.Outcome, want)
		}
	}
}

// covers: spec 0016 AC-6. Open stays null; a conversation that goes idle is
// abandoned, never resolved.
func TestOutcomeOpenAndIdle(t *testing.T) {
	store := agenttest.NewStore(func() time.Time { return now })
	c, _ := store.UpsertConversation(t.Context(), janDM, "1.0", janSlack, "")
	if c.Outcome != "" {
		t.Errorf("open conversation outcome = %q, want empty", c.Outcome)
	}
	if n, err := store.CloseIdleConversations(t.Context(), now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("close idle = %d, %v", n, err)
	}
	if got, _ := store.Conversation(janDM, "1.0"); got.Outcome != "abandoned" || got.Status != agent.StatusResolved {
		t.Errorf("idle close = %s with %q, want resolved with abandoned", got.Status, got.Outcome)
	}
}

// covers: spec 0016 AC-7. Each of the four stages inserts one failure row
// with a fixed cause token; a tool result with IsError is not a failure row.
func TestFailureRows(t *testing.T) {
	t.Run("llm", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel(agenttest.Fail(errors.New("529 overloaded"))))
		h.send(dm("Ev1", "help"))
		h.wantFailures(agenttest.FailureRow{ConversationID: 1, Stage: "llm", Cause: "other"})
	})
	t.Run("tool", func(t *testing.T) {
		tools := agenttest.NewTools(janUser, true)
		tools.Handlers["account_unlock"] = agenttest.NoAnswer()
		h := newHarness(t, agenttest.NewModel(agenttest.CallTool("t1", "account_unlock", nil)), withTools(tools))
		h.send(dm("Ev1", "locked"))
		h.wantFailures(agenttest.FailureRow{ConversationID: 1, Stage: "tool", Cause: "other"})
	})
	t.Run("tool result with IsError is not a failure", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel(privilegedCall("t1"), escalateCall("t2", "privileged_request", "s"), agenttest.Answer("ok")))
		h.send(dm("Ev1", "admin please"))
		h.wantFailures()
	})
	t.Run("slack", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel(agenttest.Answer("hello")))
		h.poster.Err = errors.New("slack down")
		h.send(dm("Ev1", "hi"))
		got := h.store.Failures()
		if len(got) == 0 || got[0].Stage != "slack" {
			t.Errorf("failures = %+v, want slack rows", got)
		}
	})
	t.Run("sweep", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel())
		h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: janUser},
			now.Add(-10*time.Minute), agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"text","text":"hi"}]`)})
		h.a.Sweep(t.Context())
		h.a.Wait()
		h.wantFailures(agenttest.FailureRow{ConversationID: 1, Stage: "sweep", Cause: "restart"})
	})
}

func (h *harness) wantFailures(want ...agenttest.FailureRow) {
	h.t.Helper()
	got := h.store.Failures()
	if len(got) != len(want) {
		h.t.Fatalf("failure rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			h.t.Errorf("failure row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// covers: spec 0016 AC-7. A failed insert never stops the turn; it logs
// agent_failure_write_failed with a cause and nothing else.
func TestFailureWriteNeverStopsTheTurn(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(agenttest.Fail(errors.New("529 overloaded"))))
	h.store.FailOn("RecordFailure")
	h.send(dm("Ev1", "help"))
	h.wantStatus(agent.StatusEscalated)
	if !hasText(h.logs.String(), `"msg":"agent_failure_write_failed"`) {
		t.Errorf("logs lack agent_failure_write_failed:\n%s", h.logs.String())
	}
	if len(h.poster.Texts()) == 0 {
		t.Error("the user got no reply")
	}
}

func hasText(s, sub string) bool { return strings.Contains(s, sub) }
