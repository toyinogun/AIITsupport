package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// wantEscalatedWithNote checks the escalated ending: a ticket created with
// the fixed title and the user's message, a note opening for the reason, and
// the escalated_with_ticket post.
func (h *harness) wantEscalatedWithNote(r agent.Reason, unknown bool) {
	h.t.Helper()
	h.wantStatus(agent.StatusEscalated)
	posts := h.poster.Texts()
	if len(posts) == 0 || posts[len(posts)-1] != agent.TextEscalatedWithTicket {
		h.t.Errorf("posts = %q, want the escalated_with_ticket text last", posts)
	}
	if note := h.lastNote(); note != agent.EscalationNote(r, unknown) {
		h.t.Errorf("note = %q, want %q", note, agent.EscalationNote(r, unknown))
	}
	if !strings.Contains(h.logs.String(), `"msg":"agent_escalated","event_id":"Ev1","reason":"`+string(r)+`","ticket":true`) {
		h.t.Errorf("logs lack agent_escalated for %s:\n%s", r, h.logs.String())
	}
}

// covers: AC-10. Every bound and outage ends escalated, with the reason's
// note on a new ticket and the escalated_with_ticket text.
func TestFailureReasons(t *testing.T) {
	loopForever := make([]agenttest.Step, 10)
	for i := range loopForever {
		loopForever[i] = agenttest.CallTool("t"+string(rune('a'+i)), "ticket_update", map[string]any{"note": "still looking"})
	}
	cases := []struct {
		name   string
		model  *agenttest.Model
		opts   []option
		setup  func(*harness)
		reason agent.Reason
	}{
		{name: "turn limit", model: agenttest.NewModel(loopForever...), opts: []option{withConfig(func(c *agent.Config) { c.MaxTurns = 3 })}, reason: agent.ReasonTurnLimit},
		{name: "message timeout", model: agenttest.NewModel(agenttest.Hang()), opts: []option{withConfig(func(c *agent.Config) { c.MessageTimeout = 100 * time.Millisecond })}, reason: agent.ReasonMessageTimeout},
		{name: "model unavailable", model: agenttest.NewModel(agenttest.Fail(errors.New("529 overloaded after retries"))), reason: agent.ReasonModelUnavailable},
		{name: "max tokens", model: agenttest.NewModel(agenttest.Reply(agenttest.Message("max_tokens", agenttest.Text("I was going to")))), reason: agent.ReasonModelUnusable},
		{name: "refusal", model: agenttest.NewModel(agenttest.Reply(agenttest.Message("refusal"))), reason: agent.ReasonModelUnusable},
		{name: "empty end turn", model: agenttest.NewModel(agenttest.Reply(agenttest.Message("end_turn", agenttest.Text("  ")))), reason: agent.ReasonModelUnusable},
		{name: "tools list down", model: agenttest.NewModel(), setup: func(h *harness) { h.tools.ListErr = errors.New("connection refused") }, reason: agent.ReasonToolServerUnavailable},
		{name: "store down", model: agenttest.NewModel(), setup: func(h *harness) { h.store.FailOn("SumTokens") }, reason: agent.ReasonStoreUnavailable},
		{name: "token budget", model: agenttest.NewModel(), setup: func(h *harness) {
			c, _ := h.store.UpsertConversation(context.Background(), janDM, firstTS, janSlack)
			h.store.SeedUsage(agent.Usage{ConversationID: c.ID, Tokens: eval.Tokens{Input: 30000}})
		}, reason: agent.ReasonTokenBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.model, tc.opts...)
			if tc.setup != nil {
				tc.setup(h)
			}
			h.send(dm("Ev1", "I'm locked out"))
			h.wantEscalatedWithNote(tc.reason, false)
			calls := h.tools.Calls()
			create := calls[len(calls)-2]
			if create.Tool != "ticket_create" || create.Args["title"] != agent.EscalationTitle ||
				create.Args["body"] != agent.EscalationIntro+"\n\nI'm locked out" {
				t.Errorf("escalation create = %+v", create)
			}
			if c := h.conv(); c.TicketID != 42 {
				t.Errorf("ticket id = %d, want the escalation ticket stored", c.TicketID)
			}
		})
	}
}

// covers: AC-5, AC-10. A stored tool_use with no result is never sent to the model.
func TestBrokenHistory(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: firstTS, RequesterSlackID: janSlack, DirectoryAccount: janUser}, now.Add(-time.Minute),
		agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"text","text":"hi"}]`)},
		agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"account_unlock","input":{}}]`)},
	)
	h.send(dm("Ev1", "hello?"))
	if len(h.model.Calls()) != 0 {
		t.Error("a broken history was sent to the model")
	}
	h.wantEscalatedWithNote(agent.ReasonHistoryInvalid, false)
}

// covers: AC-10. The escalation ticket's cases: a stored id gets a note
// only; has_ticket gets no note but counts; a failed note still counts; a
// closed ticket does not.
func TestEnsureTicketCases(t *testing.T) {
	failAfterCreate := func() *agenttest.Model {
		return agenttest.NewModel(
			agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "x", "body": "y"}),
			agenttest.Fail(errors.New("down")),
		)
	}
	t.Run("stored id: note only", func(t *testing.T) {
		h := newHarness(t, failAfterCreate())
		h.send(dm("Ev1", "I'm locked out"))
		h.wantTools("user_lookup", "ticket_create", "ticket_update")
		h.wantEscalatedWithNote(agent.ReasonModelUnavailable, false)
	})
	t.Run("has_ticket: no note", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel(agenttest.Fail(errors.New("down"))))
		h.tools.Set("ticket_create", agenttest.Respond(agenttest.Refused("has_ticket", "This conversation already has a ticket.")))
		h.send(dm("Ev1", "I'm locked out"))
		h.wantTools("user_lookup", "ticket_create")
		h.wantPosts(agent.TextAck, agent.TextEscalatedWithTicket)
	})
	t.Run("note fails: ticket still counts", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel(agenttest.Fail(errors.New("down"))))
		h.tools.Set("ticket_update", agenttest.NoAnswer())
		h.send(dm("Ev1", "I'm locked out"))
		h.wantTools("user_lookup", "ticket_create", "ticket_update")
		h.wantPosts(agent.TextAck, agent.TextEscalatedWithTicket)
	})
	t.Run("create fails: no ticket", func(t *testing.T) {
		h := newHarness(t, agenttest.NewModel(agenttest.Fail(errors.New("down"))))
		h.tools.Set("ticket_create", agenttest.Respond(agenttest.Refused("unavailable", "The helpdesk cannot be reached right now.")))
		h.send(dm("Ev1", "I'm locked out"))
		h.wantPosts(agent.TextAck, agent.TextEscalatedNoTicket)
		h.wantStatus(agent.StatusEscalated)
	})
	t.Run("ticket closed: no ticket", func(t *testing.T) {
		h := newHarness(t, failAfterCreate())
		h.tools.Set("ticket_update", agenttest.Respond(agenttest.Refused("ticket_closed", "That ticket is already closed.")))
		h.send(dm("Ev1", "I'm locked out"))
		h.wantPosts(agent.TextAck, agent.TextEscalatedNoTicket)
	})
}

// covers: AC-6. After the close, a failed summary gets resolved_no_summary,
// the conversation stays resolved, and that last call used tool_choice none.
func TestResolvedThenModelFails(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Locked out", "body": "b"}),
		agenttest.CallTool("t2", "account_unlock", nil),
		agenttest.CallTool("t3", "ticket_update", map[string]any{"note": "Unlocked.", "state": "closed"}),
		agenttest.Fail(errors.New("down")),
	))
	h.send(dm("Ev1", "I'm locked out"))
	h.wantPosts(agent.TextAck, agent.TextResolvedNoSummary)
	h.wantStatus(agent.StatusResolved)
	calls := h.model.Calls()
	if last := calls[len(calls)-1]; last.ToolChoice.OfNone == nil || len(last.Tools) == 0 {
		t.Errorf("the call after the close: tool_choice %+v, %d tools", last.ToolChoice, len(last.Tools))
	}
	h.wantTools("user_lookup", "ticket_create", "account_unlock", "ticket_update")
}

// covers: AC-11. A started call runs past the turn deadline, its result is
// stored, then the failure path runs.
func TestStartedCallFinishesPastTheDeadline(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Locked out", "body": "b"}),
		agenttest.CallTool("t2", "account_unlock", nil),
	), withConfig(func(c *agent.Config) { c.MessageTimeout = 150 * time.Millisecond }))
	h.tools.Set("account_unlock", func(ctx context.Context, _ agenttest.ToolCall) (mcpclient.Result, error) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return mcpclient.Result{}, ctx.Err()
		}
		return agenttest.OK("Account unlocked and confirmed.", map[string]any{"was_locked": true, "locked": false}), nil
	})
	h.send(dm("Ev1", "I'm locked out"))
	msgs := h.store.Messages(h.conv().ID)
	last := msgs[len(msgs)-1]
	if b := blocks(t, last); last.Role != "user" || b[0].OfToolResult == nil || b[0].OfToolResult.ToolUseID != "t2" || b[0].OfToolResult.IsError.Value {
		t.Errorf("the slow unlock's result was not stored: %s", last.Content)
	}
	h.wantEscalatedWithNote(agent.ReasonMessageTimeout, false)
	h.wantTools("user_lookup", "ticket_create", "account_unlock", "ticket_update")
}

// covers: AC-11. A call with no answer stores the unknown result and the note says so.
func TestNoAnswerStoresUnknown(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Locked out", "body": "b"}),
		agenttest.CallTool("t2", "account_unlock", nil),
	))
	h.tools.Set("account_unlock", agenttest.NoAnswer())
	h.send(dm("Ev1", "I'm locked out"))
	msgs := h.store.Messages(h.conv().ID)
	b := blocks(t, msgs[len(msgs)-1])
	if b[0].OfToolResult == nil || !b[0].OfToolResult.IsError.Value || b[0].OfToolResult.Content[0].OfText.Text != agent.UnknownToolResult {
		t.Errorf("stored result = %s", msgs[len(msgs)-1].Content)
	}
	h.wantEscalatedWithNote(agent.ReasonToolServerUnavailable, true)
}

// covers: AC-5. Two tool_use blocks in one message: only the first runs; the
// second gets an error result in the same stored message.
func TestOnlyTheFirstToolRuns(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.Reply(agenttest.Message("tool_use",
			agenttest.ToolUse("t1", "ticket_create", map[string]any{"title": "Locked out", "body": "b"}),
			agenttest.ToolUse("t2", "account_unlock", nil))),
		agenttest.Answer("I've opened a ticket."),
	))
	h.send(dm("Ev1", "I'm locked out"))
	h.wantTools("user_lookup", "ticket_create")
	msgs := h.store.Messages(h.conv().ID)
	b := blocks(t, msgs[2])
	if len(b) != 2 || b[1].OfToolResult.ToolUseID != "t2" || !b[1].OfToolResult.IsError.Value || b[1].OfToolResult.Content[0].OfText.Text != agent.OneToolPerStep {
		t.Errorf("results = %s", msgs[2].Content)
	}
	if b[0].OfToolResult.IsError.Value {
		t.Error("the first call's result is marked as an error")
	}
}

// covers: spec 0008, The model loop. A failed tools/list is not cached; the
// next turn fetches again.
func TestFailedToolListIsNotCached(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(agenttest.Answer("Hi.")))
	h.tools.ListErr = errors.New("connection refused")
	h.send(dm("Ev1", "hello"))
	h.wantStatus(agent.StatusEscalated)
	h.tools.ListErr = nil
	h.send(inThread("Ev2", "1792400100.000100", "hello again"))
	if c, _ := h.store.Conversation(janDM, "1792400100.000100"); c.Status != agent.StatusOpen {
		t.Errorf("second conversation = %+v, want open", c)
	}
	if n := h.tools.Lists(); n != 2 {
		t.Errorf("tools/list calls = %d, want 2", n)
	}
}
