package agent_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

const resetURL = "https://reset.noord.test/reset/CANARY-TOKEN-0123456789abcdefghijklmnopqrs"

func resetScript(answer string) *agenttest.Model {
	return agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Forgot password", "body": "Forgot the password; sending a reset link."}),
		agenttest.CallTool("t2", "password_reset_link", map[string]any{}),
		agenttest.Answer(answer),
	)
}

func resetTools() *agenttest.Tools {
	tools := agenttest.NewTools(janUser, false)
	tools.Set("password_reset_link", agenttest.Respond(agenttest.OK("Reset link sent to the requester.", map[string]any{
		"link_id": "0b9c", "expires_at": "2026-10-18T12:15:00Z", "expires_in_minutes": float64(15),
		"voided_previous": float64(0), "ticket_noted": true, "reset_url": resetURL,
	})))
	return tools
}

// covers: spec 0011 AC-1, AC-5. The link is posted once by code with
// unfurling off; it reaches no stored message and no model request; the
// conversation is resolved with its ticket; the reply ends with the
// resolved footer after a tool_choice none call.
func TestResetLinkIsPostedByCodeOnly(t *testing.T) {
	const answer = "I've sent you a reset link in the message above. It works once and expires in 15 minutes."
	h := newHarness(t, resetScript(answer), withTools(resetTools()))
	h.send(dm("Ev1", "I forgot my password"))

	h.wantTools("user_lookup", "ticket_create", "password_reset_link")
	h.wantPosts(agent.TextAck, agent.ResetLinkText(15, resetURL), answerWith(answer, agent.FooterResolved))
	posts := h.poster.Posts()
	if !posts[1].NoUnfurl || posts[0].NoUnfurl || posts[2].NoUnfurl {
		t.Errorf("unfurl flags = %v %v %v, want only the link post unfurl free", posts[0].NoUnfurl, posts[1].NoUnfurl, posts[2].NoUnfurl)
	}

	c := h.conv()
	if c.Status != agent.StatusResolved || c.TicketID != 42 {
		t.Errorf("conversation = %+v, want resolved with ticket 42", c)
	}
	for _, m := range h.store.Messages(c.ID) {
		if strings.Contains(string(m.Content), "CANARY-TOKEN") || strings.Contains(string(m.Content), "reset_url") {
			t.Errorf("stored message %d holds the link: %s", m.Seq, m.Content)
		}
	}
	msgs := h.store.Messages(c.ID)
	if result := string(msgs[4].Content); !strings.Contains(result, "Reset link sent to the requester.") || !strings.Contains(result, "expires_in_minutes") {
		t.Errorf("stored result = %s", result)
	}
	reqs := h.model.Calls()
	for i, r := range reqs {
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "CANARY-TOKEN") {
			t.Errorf("model request %d holds the link", i)
		}
	}
	if last := reqs[len(reqs)-1]; last.ToolChoice.OfNone == nil {
		t.Error("the call after the link is not tool_choice none")
	}
	if strings.Contains(h.logs.String(), "CANARY-TOKEN") {
		t.Error("a log line holds the link")
	}
}

// covers: spec 0011 AC-5. A failed link post replaces the model's reply
// with reset_link_failed.
func TestResetLinkPostFails(t *testing.T) {
	h := newHarness(t, resetScript("Sent!"), withTools(resetTools()))
	h.poster.NoUnfurlErr = errors.New("slack down")
	h.send(dm("Ev1", "I forgot my password"))

	h.wantPosts(agent.TextAck, agent.ResetLinkText(15, resetURL), agent.TextResetLinkFailed)
	h.wantStatus(agent.StatusResolved)
}

// covers: spec 0011 AC-5. A turn that fails after the link went out says
// so with resolved_link_no_summary, not the failure path.
func TestResetLinkThenTurnFails(t *testing.T) {
	h := newHarness(t, agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Forgot password", "body": "Reset link."}),
		agenttest.CallTool("t2", "password_reset_link", map[string]any{}),
		agenttest.Fail(errors.New("model down")),
	), withTools(resetTools()))
	h.send(dm("Ev1", "I forgot my password"))

	h.wantPosts(agent.TextAck, agent.ResetLinkText(15, resetURL), agent.TextResolvedLinkNoSummary)
	h.wantStatus(agent.StatusResolved)
}

// covers: spec 0011 AC-3, AC-5. A refused link leaves the conversation
// open and posts no link.
func TestResetLinkRefused(t *testing.T) {
	tools := agenttest.NewTools(janUser, false)
	tools.Set("password_reset_link", agenttest.Respond(agenttest.Refused("rate_limited", "Too many recent requests of this kind for this user.")))
	h := newHarness(t, resetScript("Too many reset links today; IT will help."), withTools(tools))
	h.send(dm("Ev1", "I forgot my password"))

	h.wantStatus(agent.StatusOpen)
	for _, p := range h.poster.Posts() {
		if p.NoUnfurl {
			t.Errorf("posted %q without unfurl after a refusal", p.Text)
		}
	}
}

// covers: spec 0011 AC-5. An ok result whose link cannot be posted (no
// expiry minutes) still resolves the conversation, posts no link, keeps the
// URL out of storage, and ends with reset_link_failed.
func TestResetLinkUnpostableResult(t *testing.T) {
	tools := agenttest.NewTools(janUser, false)
	tools.Set("password_reset_link", agenttest.Respond(agenttest.OK("Reset link sent to the requester.", map[string]any{
		"link_id": "0b9c", "ticket_noted": true, "reset_url": resetURL,
	})))
	h := newHarness(t, resetScript("Sent!"), withTools(tools))
	h.send(dm("Ev1", "I forgot my password"))

	h.wantPosts(agent.TextAck, agent.TextResetLinkFailed)
	h.wantStatus(agent.StatusResolved)
	for _, p := range h.poster.Posts() {
		if p.NoUnfurl || strings.Contains(p.Text, "CANARY-TOKEN") {
			t.Errorf("post %q leaked or posted the link", p.Text)
		}
	}
	for _, m := range h.store.Messages(h.conv().ID) {
		if strings.Contains(string(m.Content), "CANARY-TOKEN") {
			t.Errorf("stored message %d holds the link", m.Seq)
		}
	}
}
