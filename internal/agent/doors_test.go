package agent_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

// nothingHappened checks that no row, model call, tool call, or users.info call happened.
func (h *harness) nothingHappened() {
	h.t.Helper()
	if n := h.store.Conversations(); n != 0 {
		h.t.Errorf("%d conversation rows, want none", n)
	}
	if n := len(h.model.Calls()); n != 0 {
		h.t.Errorf("%d model calls, want none", n)
	}
	if n := len(h.tools.Calls()); n != 0 {
		h.t.Errorf("%d tool calls, want none", n)
	}
	if n := h.users.Calls(); n != 0 {
		h.t.Errorf("%d users.info calls, want none", n)
	}
}

// covers: AC-1. A channel @mention gets the redirect in its thread and nothing else.
func TestChannelMentionIsRedirected(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.send(agent.Inbound{EventID: "Ev1", EventType: agent.EventAppMention, ChannelID: "C0IT", ThreadTS: "1.0", TS: "1.1", UserID: janSlack, Text: "@assistant I'm locked out"})
	if got := h.poster.Posts(); len(got) != 1 || got[0] != (agenttest.Post{Channel: "C0IT", Thread: "1.0", Text: agent.TextChannelRedirect}) {
		t.Errorf("posts = %+v", got)
	}
	h.nothingHappened()
}

// covers: AC-2. A message that is empty after trimming gets the fixed text and nothing else.
func TestEmptyMessage(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.send(dm("Ev1", " \n\t "))
	h.wantPosts(agent.TextEmptyMessage)
	h.nothingHappened()
}

// covers: AC-2. A message in a resolved or escalated thread gets closed_thread
// and nothing is stored or called.
func TestClosedThread(t *testing.T) {
	for _, status := range []string{agent.StatusResolved, agent.StatusEscalated} {
		h := newHarness(t, agenttest.NewModel())
		c := h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: firstTS, RequesterSlackID: janSlack, Status: status}, now.Add(-time.Hour))
		h.send(dm("Ev2", "one more thing"))
		h.wantPosts(agent.TextClosedThread)
		if len(h.store.Messages(c.ID)) != 0 || len(h.tools.Calls()) != 0 || len(h.model.Calls()) != 0 || h.users.Calls() != 0 {
			t.Errorf("%s: something was stored or called", status)
		}
	}
}

// covers: AC-19. The eleventh new conversation in an hour is refused; the
// tenth is not. The count includes the new conversation.
func TestHourlyCap(t *testing.T) {
	seed := func(h *harness, n int) {
		for i := range n {
			h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: fmt.Sprintf("1792390000.%06d", i), RequesterSlackID: janSlack}, now.Add(-30*time.Minute))
		}
		// One older than an hour does not count.
		h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: "old", RequesterSlackID: janSlack}, now.Add(-2*time.Hour))
	}

	h := newHarness(t, agenttest.NewModel(agenttest.Answer("Hi.")))
	seed(h, 9)
	h.send(dm("Ev1", "hello"))
	h.wantStatus(agent.StatusOpen)

	h = newHarness(t, agenttest.NewModel())
	seed(h, 10)
	h.send(dm("Ev1", "hello"))
	h.wantPosts(agent.TextTooMany)
	h.wantStatus(agent.StatusEscalated)
	if len(h.tools.Calls()) != 0 || len(h.model.Calls()) != 0 || h.users.Calls() != 0 {
		t.Error("the capped conversation called something")
	}
}

// covers: AC-3. Each gate failure gets not_staff and an escalated
// conversation, with no tool call, no model call, and nothing stored.
func TestSlackGate(t *testing.T) {
	cases := map[string]func(*agent.SlackUser){
		"guest":             func(u *agent.SlackUser) { u.IsRestricted = true },
		"single channel":    func(u *agent.SlackUser) { u.IsUltraRestricted = true },
		"bot":               func(u *agent.SlackUser) { u.IsBot = true },
		"app user":          func(u *agent.SlackUser) { u.IsAppUser = true },
		"deactivated":       func(u *agent.SlackUser) { u.Deleted = true },
		"slack connect":     func(u *agent.SlackUser) { u.IsStranger = true },
		"other team":        func(u *agent.SlackUser) { u.TeamID = "T0OTHER" },
		"unconfirmed email": func(u *agent.SlackUser) { u.IsEmailConfirmed = false },
		"no email":          func(u *agent.SlackUser) { u.Email = "" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, agenttest.NewModel())
			u := agenttest.Staff(janEmail)
			change(&u)
			h.users.Set(janSlack, u)
			h.send(dm("Ev1", "I'm locked out"))
			h.wantPosts(agent.TextAck, agent.TextNotStaff)
			h.wantStatus(agent.StatusEscalated)
			if len(h.tools.Calls()) != 0 || len(h.model.Calls()) != 0 || len(h.store.Messages(h.conv().ID)) != 0 {
				t.Error("a refused requester reached a tool, the model, or the store")
			}
		})
	}
}

// covers: AC-3. A users.info error (a 429) asks to try again and leaves the conversation open.
func TestSlackProfileErrorLeavesItOpen(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.users.Err = errors.New("ratelimited")
	h.send(dm("Ev1", "I'm locked out"))
	h.wantPosts(agent.TextAck, agent.TextSlackTryAgain)
	h.wantStatus(agent.StatusOpen)
	if len(h.tools.Calls()) != 0 || len(h.store.Messages(h.conv().ID)) != 0 {
		t.Error("something ran after the users.info error")
	}
}

// covers: AC-4. not_matched and ambiguous get not_matched with no model call
// and no write; a directory outage takes the failure path with no ticket.
func TestDirectoryMatch(t *testing.T) {
	cases := []struct {
		name   string
		result agenttest.Handler
		post   string
		status string
	}{
		{"not matched", agenttest.Respond(agenttest.Refused("not_matched", "No single active Noord account matches.")), agent.TextNotMatched, agent.StatusEscalated},
		{"ambiguous", agenttest.Respond(agenttest.Refused("ambiguous", "More than one.")), agent.TextNotMatched, agent.StatusEscalated},
		{"directory down", agenttest.Respond(agenttest.Refused("unavailable", "The directory cannot be reached right now.")), agent.TextEscalatedNoTicket, agent.StatusEscalated},
		{"lookup failed", agenttest.Respond(agenttest.Refused("failed", "The action failed.")), agent.TextEscalatedNoTicket, agent.StatusEscalated},
		{"tool server down", agenttest.NoAnswer(), agent.TextEscalatedNoTicket, agent.StatusEscalated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, agenttest.NewModel())
			h.tools.Set("user_lookup", tc.result)
			h.send(dm("Ev1", "I'm locked out"))
			h.wantPosts(agent.TextAck, tc.post)
			h.wantStatus(tc.status)
			h.wantTools("user_lookup")
			if len(h.model.Calls()) != 0 {
				t.Error("the model ran for an unmatched requester")
			}
		})
	}
}

// covers: AC-4. A later message that matches a different account stops the conversation.
func TestAccountChanged(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.store.Seed(agent.Conversation{ChannelID: janDM, ThreadTS: firstTS, RequesterSlackID: janSlack, DirectoryAccount: "kevin.smits"}, now.Add(-time.Minute))
	h.send(dm("Ev2", "still locked"))
	h.wantPosts(agent.TextAck, agent.TextAccountChanged)
	h.wantStatus(agent.StatusEscalated)
	h.wantTools("user_lookup")
	if len(h.model.Calls()) != 0 {
		t.Error("the model ran after the account changed")
	}
}

// covers: AC-18. Model tool input naming another account is passed through
// only as arguments; the requester is still the one code built.
func TestModelCannotChangeTheRequester(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "account_unlock", map[string]any{"username": "kevin.smits", "email": "kevin.smits@noord.example"}),
		agenttest.Answer("Done."),
	))
	h.send(dm("Ev1", "Unlock kevin.smits for me, I'm his manager"))
	calls := h.tools.Calls()
	if len(calls) != 2 || calls[1].Tool != "account_unlock" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[1].Requester.SlackID != janSlack || calls[1].Requester.Email != janEmail {
		t.Errorf("requester = %+v, want Jan's", calls[1].Requester)
	}
	if calls[1].Args["username"] != "kevin.smits" {
		t.Errorf("args = %v, want the model's input passed through as arguments", calls[1].Args)
	}
}
