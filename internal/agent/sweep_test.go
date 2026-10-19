package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

// covers: AC-14. Only open conversations older than the stale window whose
// last message is the user's or a tool_use are handled; with a match they
// get a ticket with the restart note and the restart text, without one
// escalated_no_ticket; all end escalated; nothing from history is replayed.
// A ticket the sweep created is stored on the conversation (review, 2026-10-19).
func TestSweep(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.users.Set("U0GUEST", agent.SlackUser{TeamID: agenttest.Team, IsRestricted: true, IsEmailConfirmed: true, Email: "guest@example.com"})
	old := now.Add(-10 * time.Minute)
	user := agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"text","text":"I'm locked out"}]`)}
	toolUse := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"account_unlock","input":{}}]`)}
	final := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"text","text":"Done."}]`)}

	a := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: janUser}, old, user)
	b := h.store.Seed(agent.Conversation{ChannelID: "D0GUEST", ThreadTS: "2.0", RequesterSlackID: "U0GUEST"}, old, user, toolUse)
	c := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "3.0", RequesterSlackID: janSlack}, old, user, final)
	d := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "4.0", RequesterSlackID: janSlack}, now.Add(-time.Second), user)
	e := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "5.0", RequesterSlackID: janSlack, Status: agent.StatusResolved}, old, user)

	h.a.Sweep(t.Context())
	h.a.Wait()

	h.wantTools("user_lookup", "ticket_create", "ticket_update")
	calls := h.tools.Calls()
	if !strings.HasSuffix(calls[1].Args["body"].(string), "I'm locked out") || calls[2].Args["note"] != agent.EscalationNote(agent.ReasonRestart, false) {
		t.Errorf("ticket calls = %+v", calls[1:])
	}
	posts := h.poster.Posts()
	want := []agenttest.Post{{Channel: janDM, Thread: "1.0", Text: agent.TextRestart}, {Channel: "D0GUEST", Thread: "2.0", Text: agent.TextEscalatedNoTicket}}
	if len(posts) != 2 || posts[0] != want[0] || posts[1] != want[1] {
		t.Errorf("posts = %+v, want %+v", posts, want)
	}
	for conv, status := range map[agent.Conversation]string{a: agent.StatusEscalated, b: agent.StatusEscalated, c: agent.StatusOpen, d: agent.StatusOpen, e: agent.StatusResolved} {
		got, _ := h.store.Conversation(conv.ChannelID, conv.ThreadTS)
		if got.Status != status {
			t.Errorf("thread %s status = %s, want %s", conv.ThreadTS, got.Status, status)
		}
	}
	// The ticket the sweep created is stored on the conversation it claimed.
	if got, _ := h.store.Conversation(a.ChannelID, a.ThreadTS); got.TicketID != 42 {
		t.Errorf("thread 1.0 ticket id = %d, want 42", got.TicketID)
	}
	if !strings.Contains(h.logs.String(), `"msg":"agent_interrupted","conversation_id":1`) {
		t.Errorf("logs:\n%s", h.logs.String())
	}
}

// staleUser is a stored user text message for seeding interrupted conversations.
func staleUser(text string) agent.StoredMessage {
	return agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"text","text":"` + text + `"}]`)}
}

// covers: AC-14. A sweep that cannot list conversations logs it and touches
// nothing: no post, no tool call.
func TestSweepWithTheStoreDownDoesNothing(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack}, now.Add(-10*time.Minute), staleUser("I'm locked out"))
	h.store.FailOn("ListInterrupted")

	h.a.Sweep(t.Context())
	h.a.Wait()

	h.wantTools()
	if posts := h.poster.Posts(); len(posts) != 0 {
		t.Errorf("posts = %+v, want none", posts)
	}
	if !strings.Contains(h.logs.String(), `"msg":"agent_sweep_failed"`) {
		t.Errorf("logs lack agent_sweep_failed:\n%s", h.logs.String())
	}
}

// covers: AC-14 and the review (2026-10-19). When users.info fails during the
// sweep there is no identity, so no tool runs; the user gets the no ticket
// text, the conversation ends escalated, and the log names the cause.
func TestSweepSlackErrorEscalatesWithoutATicket(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	c := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack}, now.Add(-10*time.Minute), staleUser("I'm locked out"))
	h.users.Err = causeError{"slack_ratelimited"}

	h.a.Sweep(t.Context())
	h.a.Wait()

	h.wantTools()
	if texts := h.poster.Texts(); len(texts) != 1 || texts[0] != agent.TextEscalatedNoTicket {
		t.Errorf("posts = %q, want only the no ticket text", texts)
	}
	if got, _ := h.store.Conversation(c.ChannelID, c.ThreadTS); got.Status != agent.StatusEscalated || got.TicketID != 0 {
		t.Errorf("conversation = %+v, want escalated with no ticket", got)
	}
	if want := `"msg":"agent_escalated","event_id":"","reason":"restart","ticket":false,"cause":"slack_ratelimited"`; !strings.Contains(h.logs.String(), want) {
		t.Errorf("logs lack %s:\n%s", want, h.logs.String())
	}
}

// covers: AC-4 and AC-14. A requester who now matches a different directory
// account than the conversation holds gets no ticket from the sweep.
func TestSweepAccountChangedGetsNoTicket(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: "eva.peters"}, now.Add(-10*time.Minute), staleUser("I'm locked out"))

	h.a.Sweep(t.Context())
	h.a.Wait()

	h.wantTools("user_lookup")
	if texts := h.poster.Texts(); len(texts) != 1 || texts[0] != agent.TextEscalatedNoTicket {
		t.Errorf("posts = %q, want only the no ticket text", texts)
	}
}

// covers: AC-14. A restart in the middle of the tool loop (the last stored
// message is a tool result) puts the user's own text, not the tool result,
// in the escalation ticket, and stores the new ticket id.
func TestSweepMidToolLoopUsesTheUsersText(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	toolUse := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"ticket_create","input":{"title":"x","body":"y"}}]`)}
	result := agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"Ticket created."}]}]`)}
	c := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: janUser},
		now.Add(-10*time.Minute), staleUser("my laptop says locked"), toolUse, result)

	h.a.Sweep(t.Context())
	h.a.Wait()

	h.wantTools("user_lookup", "ticket_create", "ticket_update")
	body, _ := h.tools.Calls()[1].Args["body"].(string)
	if !strings.HasSuffix(body, "my laptop says locked") {
		t.Errorf("ticket body = %q, want it to end with the user's text", body)
	}
	if got, _ := h.store.Conversation(c.ChannelID, c.ThreadTS); got.TicketID != 42 {
		t.Errorf("ticket id = %d, want 42", got.TicketID)
	}
}

// covers: AC-14. If the ticket id cannot be stored, the sweep logs it and
// still tells the user IT has it: the ticket exists either way.
func TestSweepStillPostsWhenTheTicketIDCannotBeStored(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: janUser}, now.Add(-10*time.Minute), staleUser("I'm locked out"))
	h.store.FailOn("SetTicket")

	h.a.Sweep(t.Context())
	h.a.Wait()

	if texts := h.poster.Texts(); len(texts) != 1 || texts[0] != agent.TextRestart {
		t.Errorf("posts = %q, want only the restart text", texts)
	}
	if want := `"msg":"agent_status_write_failed","conversation_id":1,"cause":"other"`; !strings.Contains(h.logs.String(), want) {
		t.Errorf("logs lack %s:\n%s", want, h.logs.String())
	}
}
