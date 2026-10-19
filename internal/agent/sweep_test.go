package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

// covers: AC-14 and spec 0009 AC-11. Only open conversations older than the
// stale window whose last message is the user's or a tool_use are handled;
// with a match they are escalated through escalate (reason restart, the
// restart note) and get the restart text, without one escalated_no_ticket;
// all end escalated; nothing from history is replayed. The ticket the
// escalation used is stored on the conversation.
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

	h.wantTools("user_lookup", "escalate")
	esc := h.lastEscalation()
	wantSummary := agent.EscalationIntro + "\n\nI'm locked out\n\n" + agent.EscalationNote(agent.ReasonRestart, false)
	if esc.Args["reason"] != "restart" || esc.Args["title"] != agent.EscalationTitle || esc.Args["summary"] != wantSummary ||
		esc.Requester.Initiator != "code" {
		t.Errorf("escalation = %+v", esc)
	}
	var posts []agenttest.Post
	for _, p := range h.poster.Posts() {
		if p.Channel != itChannel {
			posts = append(posts, p)
		}
	}
	want := []agenttest.Post{{Channel: janDM, Thread: "1.0", Text: agent.TextRestart}, {Channel: "D0GUEST", Thread: "2.0", Text: agent.TextEscalatedNoTicket}}
	if len(posts) != 2 || posts[0] != want[0] || posts[1] != want[1] {
		t.Errorf("posts = %+v, want %+v", posts, want)
	}
	wantIT := ":arrows_counterclockwise: *Interrupted by a restart*\nRequester: <@U0JAN> (jan.devries)\nRan: escalate ok\n" +
		"Ticket: <http://127.0.0.1:58080/#ticket/zoom/43|#10043>\nSummary: " + agent.NoteOpening(agent.ReasonRestart)
	if it := h.itPosts(); len(it) != 1 || it[0] != wantIT {
		t.Errorf("IT posts = %q, want %q", it, wantIT)
	}
	for conv, status := range map[agent.Conversation]string{a: agent.StatusEscalated, b: agent.StatusEscalated, c: agent.StatusOpen, d: agent.StatusOpen, e: agent.StatusResolved} {
		got, _ := h.store.Conversation(conv.ChannelID, conv.ThreadTS)
		if got.Status != status {
			t.Errorf("thread %s status = %s, want %s", conv.ThreadTS, got.Status, status)
		}
	}
	// The ticket the escalation used is stored on the conversation it claimed.
	if got, _ := h.store.Conversation(a.ChannelID, a.ThreadTS); got.TicketID != 43 {
		t.Errorf("thread 1.0 ticket id = %d, want 43", got.TicketID)
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
// in the escalation summary, and stores the ticket id.
func TestSweepMidToolLoopUsesTheUsersText(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	toolUse := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"ticket_create","input":{"title":"x","body":"y"}}]`)}
	result := agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"Ticket created."}]}]`)}
	c := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: janUser},
		now.Add(-10*time.Minute), staleUser("my laptop says locked"), toolUse, result)

	h.a.Sweep(t.Context())
	h.a.Wait()

	h.wantTools("user_lookup", "escalate")
	summary, _ := h.lastEscalation().Args["summary"].(string)
	if !strings.HasPrefix(summary, agent.EscalationIntro+"\n\nmy laptop says locked\n\n") {
		t.Errorf("summary = %q, want the user's text", summary)
	}
	if got, _ := h.store.Conversation(c.ChannelID, c.ThreadTS); got.TicketID != 43 {
		t.Errorf("ticket id = %d, want 43", got.TicketID)
	}
	if it := h.itPosts(); len(it) != 1 || !strings.Contains(it[0], "\nRan: ticket_create ok, escalate ok\n") || strings.Contains(it[0], "laptop") {
		t.Errorf("IT posts = %q, want the stored call listed and no user text", it)
	}
}

// covers: spec 0009 AC-11. A conversation whose stored history holds a
// blocked privileged request is escalated as privileged_request, and a
// conversation that already has a ticket keeps it.
func TestSweepPrivilegedHistory(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.tools.Set("escalate", agenttest.Escalated(43, "10043", true))
	toolUse := agent.StoredMessage{Role: "assistant", Content: []byte(`[{"type":"tool_use","id":"t1","name":"privileged_access_change","input":{"summary":"x"}}]`)}
	result := agent.StoredMessage{Role: "user", Content: []byte(`[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"blocked: Privileged changes are never made by the assistant."}]}]`)}
	c := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "1.0", RequesterSlackID: janSlack, DirectoryAccount: janUser, TicketID: 42},
		now.Add(-10*time.Minute), staleUser("make me Domain Admin"), toolUse, result)

	h.a.Sweep(t.Context())
	h.a.Wait()

	if e := h.lastEscalation(); e.Args["reason"] != "privileged_request" || e.Requester.TicketID != 42 {
		t.Errorf("escalation = %+v, want privileged_request on ticket 42", e)
	}
	if got, _ := h.store.Conversation(c.ChannelID, c.ThreadTS); got.TicketID != 42 {
		t.Errorf("ticket id = %d, want 42 kept", got.TicketID)
	}
	if it := h.itPosts(); len(it) != 1 || !strings.HasPrefix(it[0], ":rotating_light: *Privileged request refused*\n") ||
		!strings.Contains(it[0], "\nRan: privileged_access_change blocked, escalate ok\n") {
		t.Errorf("IT posts = %q", it)
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

	h.wantPosts(agent.TextRestart)
	if want := `"msg":"agent_status_write_failed","conversation_id":1,"cause":"other"`; !strings.Contains(h.logs.String(), want) {
		t.Errorf("logs lack %s:\n%s", want, h.logs.String())
	}
}
