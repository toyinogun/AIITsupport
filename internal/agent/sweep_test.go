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
	if !strings.Contains(h.logs.String(), `"msg":"agent_interrupted","conversation_id":1`) {
		t.Errorf("logs:\n%s", h.logs.String())
	}
}
