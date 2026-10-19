package agent_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/agent/prompt"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

func lockoutScript() *agenttest.Model {
	return agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Locked out", "body": "Jan is locked out; unlocking."}),
		agenttest.CallTool("t2", "account_unlock", nil),
		agenttest.CallTool("t3", "ticket_update", map[string]any{"note": "Account was locked; unlocked it.", "state": "closed"}),
		agenttest.Answer("Your account is unlocked. You can sign in again."),
	)
}

// covers: AC-4, AC-5, AC-6, AC-12, AC-15. The lockout runs ticket, unlock,
// close in order; the ticket id reaches the later calls; history is stored
// as block arrays; four usage rows carry costs; the model never sees
// user_lookup; the call after the close uses tool_choice none.
func TestHappyPathUnlocksAndClosesTheTicket(t *testing.T) {
	h := newHarness(t, lockoutScript())
	h.send(dm("Ev1", "I'm locked out"))

	h.wantTools("user_lookup", "ticket_create", "account_unlock", "ticket_update")
	calls := h.tools.Calls()
	for i, want := range []int64{0, 0, 42, 42} {
		if calls[i].Requester.TicketID != want {
			t.Errorf("call %d (%s) ticket id = %d, want %d", i, calls[i].Tool, calls[i].Requester.TicketID, want)
		}
		wantReq := mcpclient.Requester{SlackID: janSlack, Email: janEmail, ChannelID: janDM, ThreadTS: firstTS, TicketID: want}
		if calls[i].Requester != wantReq {
			t.Errorf("call %d requester = %+v, want %+v", i, calls[i].Requester, wantReq)
		}
	}
	h.wantPosts(agent.TextAck, answerWith("Your account is unlocked. You can sign in again.", agent.FooterResolved))

	c := h.conv()
	if c.Status != agent.StatusResolved || c.TicketID != 42 || c.DirectoryAccount != janUser {
		t.Errorf("conversation = %+v, want resolved, ticket 42, account %s", c, janUser)
	}
	msgs := h.store.Messages(c.ID)
	wantRoles := []string{"user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant"}
	if len(msgs) != len(wantRoles) {
		t.Fatalf("%d stored messages, want %d", len(msgs), len(wantRoles))
	}
	for i, m := range msgs {
		if m.Role != wantRoles[i] || len(m.Content) == 0 || m.Content[0] != '[' {
			t.Errorf("message %d = %s %s, want role %s with a block array", i, m.Role, m.Content, wantRoles[i])
		}
	}
	if b := blocks(t, msgs[2]); len(b) != 1 || b[0].OfToolResult == nil || b[0].OfToolResult.ToolUseID != "t1" {
		t.Errorf("first tool result = %s", msgs[2].Content)
	}

	usage := h.store.Usage()
	if len(usage) != 4 {
		t.Fatalf("%d usage rows, want 4", len(usage))
	}
	for _, u := range usage {
		if u.Model != modelName || u.CostMicroUSD != h.cfg.Price.CostMicroUSD(u.Tokens) || u.CostMicroUSD == 0 || !u.PricingDate.Equal(h.cfg.Price.PricingDate) {
			t.Errorf("usage row = %+v", u)
		}
	}

	reqs := h.model.Calls()
	for i, r := range reqs {
		for _, tool := range r.Tools {
			if tool.OfTool.Name == "user_lookup" {
				t.Errorf("request %d offers user_lookup to the model", i)
			}
		}
		last := r.Tools[len(r.Tools)-1].OfTool
		if last.CacheControl.Type != "ephemeral" {
			t.Errorf("request %d: the last tool is not cache marked", i)
		}
		if r.System[0].Text != prompt.System || r.System[0].CacheControl.Type != "ephemeral" || r.System[1].CacheControl.Type != "" {
			t.Errorf("request %d: system blocks are not the cached prompt then the uncached requester block", i)
		}
		if !strings.Contains(r.System[1].Text, "Username: jan.devries") || !strings.Contains(r.System[1].Text, "Account locked: yes") {
			t.Errorf("request %d requester block:\n%s", i, r.System[1].Text)
		}
		if r.MaxTokens != 1024 || string(r.Model) != modelName {
			t.Errorf("request %d: model %s max_tokens %d", i, r.Model, r.MaxTokens)
		}
		resolved := i == 3
		if resolved != (r.ToolChoice.OfNone != nil) || !resolved && (r.ToolChoice.OfAuto == nil || !r.ToolChoice.OfAuto.DisableParallelToolUse.Value) {
			t.Errorf("request %d tool_choice = %+v", i, r.ToolChoice)
		}
	}
	// The thinking block goes back as it came (an append only history).
	raw, _ := json.Marshal(reqs[1].Messages[1])
	if !strings.Contains(string(raw), `"signature":"sig-abc"`) {
		t.Errorf("second request lost the thinking block: %s", raw)
	}
	if h.tools.Lists() != 1 {
		t.Errorf("tools/list called %d times, want 1 (cached)", h.tools.Lists())
	}
}

// covers: AC-8. Not locked: the model opens a ticket and leaves it open;
// the conversation stays open and the footer says so.
func TestNotLockedLeavesTheTicketOpen(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Cannot sign in", "body": "Not locked; likely the password."}),
		agenttest.Answer("Your account isn't locked, so it's most likely your password. I've opened a ticket for IT."),
	), withTools(agenttest.NewTools(janUser, false)))
	h.send(dm("Ev1", "I can't log in"))

	h.wantTools("user_lookup", "ticket_create")
	h.wantStatus(agent.StatusOpen)
	if c := h.conv(); c.TicketID != 42 {
		t.Errorf("ticket id = %d, want 42", c.TicketID)
	}
	if !strings.Contains(h.model.Calls()[0].System[1].Text, "Account locked: no") {
		t.Error("the requester block does not say the account is not locked")
	}
	posts := h.poster.Texts()
	if len(posts) != 2 || !strings.HasSuffix(posts[1], "\n\n"+agent.FooterOpen) {
		t.Errorf("posts = %q", posts)
	}
}

// covers: AC-6. A ticket id is stored with its result; a refused update
// does not resolve the conversation.
func TestRefusedUpdateKeepsTheConversationOpen(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Locked out", "body": "b"}),
		agenttest.CallTool("t2", "account_unlock", nil),
		agenttest.CallTool("t3", "ticket_update", map[string]any{"note": "Unlock was refused: paused."}),
		agenttest.Answer("Account changes are paused by IT right now, so I couldn't unlock you. IT will follow up."),
	))
	h.tools.Set("account_unlock", agenttest.Respond(agenttest.Refused("read_only", "Account changes are paused by IT right now.")))
	h.send(dm("Ev1", "I'm locked out"))
	h.wantStatus(agent.StatusOpen)
	msgs := h.store.Messages(h.conv().ID)
	if b := blocks(t, msgs[4]); b[0].OfToolResult == nil || !b[0].OfToolResult.IsError.Value {
		t.Errorf("the refused unlock was not stored as an error result: %s", msgs[4].Content)
	}
	if !strings.Contains(string(msgs[4].Content), `\"code\":\"read_only\"`) {
		t.Errorf("the result lacks the code: %s", msgs[4].Content)
	}
}
