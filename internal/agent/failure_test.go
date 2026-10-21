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

// wantEscalatedWithNote checks the escalated ending (spec 0009, AC-11): one
// code escalate with the fixed title and the user's message plus the
// reason's note, an IT post with the note's opening as its summary, and the
// escalated_with_ticket post.
func (h *harness) wantEscalatedWithNote(r agent.Reason, unknown bool, message string) {
	h.t.Helper()
	h.wantStatus(agent.StatusEscalated)
	posts := h.poster.Texts()
	if len(posts) == 0 || posts[len(posts)-1] != agent.TextEscalatedWithTicket {
		h.t.Errorf("posts = %q, want the escalated_with_ticket text last", posts)
	}
	e := h.lastEscalation()
	wantSummary := agent.EscalationIntro + "\n\n" + message + "\n\n" + agent.EscalationNote(r, unknown)
	if e.Requester.Initiator != mcpclient.InitiatorCode || e.Args["title"] != agent.EscalationTitle ||
		e.Args["summary"] != wantSummary || e.Args["reason"] != "assistant_failure" {
		h.t.Errorf("escalation = %+v, want the code call with summary %q", e, wantSummary)
	}
	it := h.itPosts()
	if len(it) != 1 || !strings.HasPrefix(it[0], ":construction: *Assistant could not finish*\n") ||
		!strings.HasSuffix(it[0], "\nSummary: "+agent.NoteOpening(r)) || strings.Contains(it[0], message) {
		h.t.Errorf("IT posts = %q, want one with the note opening and no user text", it)
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
		{name: "store down", model: agenttest.NewModel(), setup: func(h *harness) { h.store.FailOn("SumUsage") }, reason: agent.ReasonStoreUnavailable},
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
			h.wantEscalatedWithNote(tc.reason, false, "I'm locked out")
			if c := h.conv(); c.TicketID != 43 {
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
	h.wantEscalatedWithNote(agent.ReasonHistoryInvalid, false, "hello?")
}

// covers: AC-10 and spec 0009 AC-11. The failure path's escalate cases: a
// stored ticket is kept; a found one is stored; already_escalated still
// counts (with the short IT post); any other refusal or no answer means no
// ticket; a blocked attempt makes it privileged_request; and a turn already
// escalated makes no call.
func TestFailurePathEscalation(t *testing.T) {
	failAfter := func(steps ...agenttest.Step) *agenttest.Model {
		return agenttest.NewModel(append(steps, agenttest.Fail(errors.New("down")))...)
	}
	create := agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "x", "body": "y"})
	t.Run("stored id kept", func(t *testing.T) {
		h := newHarness(t, failAfter(create))
		h.send(dm("Ev1", "I'm locked out"))
		h.wantTools("user_lookup", "ticket_create", "escalate")
		h.wantEscalatedWithNote(agent.ReasonModelUnavailable, false, "I'm locked out")
		if e := h.lastEscalation(); e.Requester.TicketID != 42 || h.conv().TicketID != 42 {
			t.Errorf("escalate ticket %d, stored %d; want 42 kept", e.Requester.TicketID, h.conv().TicketID)
		}
	})
	t.Run("found ticket stored", func(t *testing.T) {
		h := newHarness(t, failAfter())
		h.tools.Set("escalate", agenttest.Escalated(77, "10077", false))
		h.send(dm("Ev1", "I'm locked out"))
		if c := h.conv(); c.TicketID != 77 || c.Status != agent.StatusEscalated {
			t.Errorf("conversation = %+v, want ticket 77", c)
		}
	})
	t.Run("already escalated counts", func(t *testing.T) {
		h := newHarness(t, failAfter())
		h.tools.Set("escalate", agenttest.Respond(agenttest.Refused("already_escalated", "This conversation is already with IT.")))
		h.send(dm("Ev1", "I'm locked out"))
		h.wantPosts(agent.TextAck, agent.TextEscalatedWithTicket)
		want := ":construction: *Assistant could not finish*\nRequester: <@U0JAN> (jan.devries)\nTicket: already escalated; see the audit log for this thread."
		if it := h.itPosts(); len(it) != 1 || it[0] != want {
			t.Errorf("IT posts = %q, want the short post", it)
		}
	})
	for name, handler := range map[string]agenttest.Handler{
		"closed: no ticket":    agenttest.Respond(agenttest.Refused("ticket_closed", "That ticket is already closed.")),
		"no answer: no ticket": agenttest.NoAnswer(),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, failAfter())
			h.tools.Set("escalate", handler)
			h.send(dm("Ev1", "I'm locked out"))
			h.wantPosts(agent.TextAck, agent.TextEscalatedNoTicket)
			h.wantStatus(agent.StatusEscalated)
			if it := h.itPosts(); len(it) != 0 {
				t.Errorf("IT posts = %q, want none", it)
			}
		})
	}
	t.Run("blocked: privileged_request", func(t *testing.T) {
		h := newHarness(t, failAfter(agenttest.CallTool("t1", "privileged_access_change", map[string]any{"summary": "Domain Admin"})))
		h.tools.Set("escalate", agenttest.Escalated(43, "10043", true))
		h.send(dm("Ev1", "I need Domain Admin"))
		if e := h.lastEscalation(); e.Args["reason"] != "privileged_request" {
			t.Errorf("reason = %v, want privileged_request", e.Args["reason"])
		}
		if it := h.itPosts(); len(it) != 1 || !strings.HasPrefix(it[0], ":rotating_light: *Privileged request refused*\n") ||
			!strings.Contains(it[0], "\nRan: privileged_access_change blocked, escalate ok\n") {
			t.Errorf("IT posts = %q", it)
		}
	})
	t.Run("escalated in the turn: no call", func(t *testing.T) {
		h := newHarness(t, failAfter(agenttest.CallTool("t1", "escalate", map[string]any{"reason": "other_person", "title": "t", "summary": "s"})))
		h.send(dm("Ev1", "Unlock Eva's account"))
		h.wantTools("user_lookup", "escalate")
		h.wantPosts(agent.TextAck, agent.TextEscalatedWithTicket)
		if it := h.itPosts(); len(it) != 1 {
			t.Errorf("IT posts = %q, want only the model's", it)
		}
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
	h.wantEscalatedWithNote(agent.ReasonMessageTimeout, false, "I'm locked out")
	h.wantTools("user_lookup", "ticket_create", "account_unlock", "escalate")
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
	h.wantEscalatedWithNote(agent.ReasonToolServerUnavailable, true, "I'm locked out")
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
