package agent_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// Spec 0013: the access request's owner card, click, and settles.

const (
	ilseSlack = "U0ILSE0001"
	ilseEmail = "ilse.smit@noord.example"
)

var accessExpires = now.Add(8 * time.Hour)

const accessAnswer = "Ilse Smit decides on SG-Finance. You'll get a message here within 8 hours. Nothing has changed yet."

func accessScript() *agenttest.Model {
	return agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "Finance access", "body": "Needs SG-Finance."}),
		agenttest.CallTool("t2", "group_access_request", map[string]any{"group": "SG-Finance", "reason": "I book invoices"}),
		agenttest.Answer(accessAnswer),
	)
}

func accessPending() mcpclient.Result {
	return mcpclient.Result{
		Text: "This request is waiting for the group owner's approval.", Code: "pending_approval",
		Data: map[string]any{
			"code": "pending_approval", "approval_id": approvalID.String(), "group": "SG-Finance", "owner_name": "Ilse Smit",
			"expires_at": accessExpires.Format(time.RFC3339), "expires_in_hours": float64(8), "ticket_id": float64(42),
			"reused": false, "ticket_noted": true,
		},
	}
}

// accessView is the group owner approval as the tool server would show it.
func accessView(status string) mcpclient.ApprovalView {
	v := mcpclient.ApprovalView{
		ID: approvalID, ToolName: "group_access_request", Status: status, ExpiresAt: accessExpires, CreatedAt: now,
		RequesterSlackID: janSlack, RequesterAccount: janUser, SlackChannelID: janDM, SlackThreadTS: firstTS,
		TicketID: 42, TicketURL: "http://zammad.test/#ticket/zoom/42",
		ApproverKind: mcpclient.ApproverKindGroupOwner, Target: "SG-Finance", ApproverAccount: "ilse.smit",
		ApproverName: "Ilse Smit", ApproverEmail: ilseEmail, NestedInto: []string{"APP-ERP-Finance", "SHR-Finance-RW"},
	}
	if status == mcpclient.StatusPending {
		v.Reason = "I book <invoices>"
	}
	if status != mcpclient.StatusPending && status != mcpclient.StatusExpired {
		decided := now.Add(time.Minute)
		v.DecidedBy, v.DecidedAt = "ilse.smit", &decided
	}
	switch status {
	case mcpclient.StatusExecuted:
		v.Execution = &mcpclient.ApprovalExecution{Outcome: "ok", Result: map[string]any{
			"added": true, "already_member": false, "ticket_closed": true,
		}}
	case mcpclient.StatusFailed:
		v.Execution = &mcpclient.ApprovalExecution{Outcome: "error", Reason: "add not confirmed"}
	}
	return v
}

// accessRequested runs the request turn with ilse in Slack (unless change
// says otherwise) and the pending view on the tool server.
func accessRequested(t *testing.T, change func(h *harness), opts ...option) *harness {
	t.Helper()
	tools := agenttest.NewTools(janUser, false)
	tools.Set("group_access_request", agenttest.Respond(accessPending()))
	h := newHarness(t, accessScript(), append([]option{withTools(tools)}, opts...)...)
	h.users.Set(ilseSlack, agenttest.Staff(ilseEmail))
	h.appr.Views[approvalID] = accessView(mcpclient.StatusPending)
	if change != nil {
		change(h)
	}
	h.send(dm("Ev1", "ik heb toegang nodig tot finance"))
	return h
}

func (h *harness) ownerClick(clicker string, approve bool) {
	h.t.Helper()
	h.a.Click(agent.Click{EventID: "Env1", ApprovalID: approvalID, UserID: clicker, Approve: approve, ChannelID: agenttest.DMChannel(ilseSlack)})
	h.a.Wait()
}

// covers: spec 0013 AC-9, AC-10, AC-12. The pending result stores a card
// row with no channel; agent code reads the view, finds the owner by email,
// and posts the card to their DM with the fixed lines, escaped values, the
// Amsterdam expiry, and the add confirm; the row keeps the DM and the
// owner's Slack id. The conversation stays open.
func TestAccessRequestPostsTheOwnerDM(t *testing.T) {
	h := accessRequested(t, nil)
	h.wantTools("user_lookup", "ticket_create", "group_access_request")
	h.wantPosts(agent.TextAck, answerWith(accessAnswer, agent.FooterOpen))
	h.wantStatus(agent.StatusOpen)
	cards := h.store.Cards()
	if len(cards) != 1 || cards[0].ChannelID != agenttest.DMChannel(ilseSlack) || cards[0].ApproverSlackID != ilseSlack ||
		cards[0].CardTS == "" || cards[0].PostAttempts != 1 || cards[0].ToolName != "group_access_request" {
		t.Fatalf("cards = %+v", cards)
	}
	posted := h.poster.Cards()
	if len(posted) != 1 || posted[0].Channel != ilseSlack {
		t.Fatalf("card posts = %+v", posted)
	}
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Access request",
		"Requester: <@" + janSlack + "> (" + janUser + ")",
		"Group: SG-Finance",
		"Also gives: APP-ERP-Finance, SHR-Finance-RW",
		"Requester says (unverified): I book &lt;invoices&gt;",
		"Ticket: <http://zammad.test/#ticket/zoom/42|open in helpdesk>",
		"Expires: " + accessExpires.In(ams).Format("2006-01-02 15:04") + " Amsterdam time",
		"You own this group in the directory. Approve only if this person needs it.",
	}
	c := posted[0].Card
	if strings.Join(c.Lines, "\n") != strings.Join(want, "\n") || c.ApprovalID != approvalID.String() ||
		c.Fallback != "Access request for SG-Finance" || c.Confirm != "Add "+janUser+" to SG-Finance?" {
		t.Errorf("card = %#v", c)
	}
	if len(h.itPosts()) != 0 {
		t.Errorf("IT posts = %v", h.itPosts())
	}
}

// covers: spec 0013 AC-13. A click from anyone but the Slack account the
// card went to, or from the owner when they fail the Slack gate, is refused
// with nothing sent; the owner's click sends their email.
func TestAccessClickChecks(t *testing.T) {
	h := accessRequested(t, nil)
	h.ownerClick(itLead, true)
	if eph := h.poster.Ephemerals(); len(eph) != 1 || eph[0].Text != agent.TextApprovalNotAllowed || len(h.appr.Clicks()) != 0 {
		t.Fatalf("ephemerals %+v, clicks %+v", eph, h.appr.Clicks())
	}
	gateFails := agenttest.Staff(ilseEmail)
	gateFails.IsEmailConfirmed = false
	h.users.Set(ilseSlack, gateFails)
	h.ownerClick(ilseSlack, true)
	if eph := h.poster.Ephemerals(); len(eph) != 2 || eph[1].Text != agent.TextApprovalNotAllowed || len(h.appr.Clicks()) != 0 {
		t.Fatalf("gate: ephemerals %+v, clicks %+v", eph, h.appr.Clicks())
	}
	h.users.Set(ilseSlack, agenttest.Staff(ilseEmail))
	h.ownerClick(ilseSlack, true)
	if c := h.appr.Clicks(); len(c) != 1 || c[0].Clicker != ilseSlack || c[0].Email != ilseEmail || !c[0].Approve {
		t.Errorf("clicks = %+v", c)
	}
}

// covers: spec 0013 AC-15, AC-16. An executed add settles once: the user
// gets access_granted, the DM card says "you", the conversation is resolved,
// and nothing goes to IT.
func TestAccessApproveSettles(t *testing.T) {
	h := accessRequested(t, nil)
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: accessView(mcpclient.StatusExecuted)}
	h.ownerClick(ilseSlack, true)

	h.wantStatus(agent.StatusResolved)
	h.wantPosts(agent.TextAck, answerWith(accessAnswer, agent.FooterOpen), agent.AccessGrantedText("SG-Finance"))
	cards := h.poster.Cards()
	last := cards[len(cards)-1]
	want := []string{
		"Access request", "Requester: <@" + janSlack + "> (" + janUser + ")", "Group: SG-Finance",
		"Ticket: <http://zammad.test/#ticket/zoom/42|open in helpdesk>", "Approved by you. " + janUser + " added to SG-Finance.",
	}
	if !last.Update || last.Channel != agenttest.DMChannel(ilseSlack) || last.Card.ApprovalID != "" ||
		strings.Join(last.Card.Lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("update = %+v", last)
	}
	if len(h.itPosts()) != 0 || slices.Contains(h.tools.Names(), "escalate") {
		t.Errorf("IT posts %v, tools %v", h.itPosts(), h.tools.Names())
	}
}

// covers: spec 0013 AC-16. A ticket the add could not close is named.
func TestAccessApproveTicketOpenText(t *testing.T) {
	h := accessRequested(t, nil)
	v := accessView(mcpclient.StatusExecuted)
	v.Execution.Result["ticket_closed"] = false
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: v}
	h.ownerClick(ilseSlack, true)
	h.wantPosts(agent.TextAck, answerWith(accessAnswer, agent.FooterOpen),
		agent.AccessGrantedText("SG-Finance")+" Your ticket stays open for IT.")
}

// covers: spec 0013 AC-15, AC-16. A deny closes the ticket by code with the
// fixed note and no escalation; the user hears who declined.
func TestAccessDenyClosesByCode(t *testing.T) {
	h := accessRequested(t, nil)
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: accessView(mcpclient.StatusDenied)}
	h.ownerClick(ilseSlack, false)

	calls := h.tools.Calls()
	last := calls[len(calls)-1]
	if last.Tool != "ticket_update" || last.Args["state"] != "closed" || last.Args["note"] != agent.AccessDeniedNote ||
		last.Requester.Initiator != mcpclient.InitiatorCode || last.Requester.TicketID != 42 {
		t.Errorf("last call = %+v", last)
	}
	if slices.Contains(h.tools.Names(), "escalate") || len(h.itPosts()) != 0 {
		t.Errorf("a deny escalated: %v, %v", h.tools.Names(), h.itPosts())
	}
	h.wantStatus(agent.StatusResolved)
	h.wantPosts(agent.TextAck, answerWith(accessAnswer, agent.FooterOpen),
		"Ilse Smit declined your request for SG-Finance. If you think you need it, talk to them directly.")
	cards := h.poster.Cards()
	if lines := cards[len(cards)-1].Card.Lines; lines[len(lines)-1] != "Denied by you." {
		t.Errorf("outcome = %q", lines[len(lines)-1])
	}
}

// covers: spec 0013 AC-10, AC-11, AC-15, AC-19. An owner with no Slack
// account (a leaver) cancels the request at the first post and settles it
// as owner unreachable: no card, an escalation with no_group_owner and the
// fixed summary, and access_no_owner for the user.
func TestAccessOwnerNotInSlackCancels(t *testing.T) {
	h := accessRequested(t, func(h *harness) { h.users.Set(ilseSlack, agenttest.Staff("someone.else@noord.example")) })

	if c := h.appr.Cancels(); len(c) != 1 || c[0] != approvalID {
		t.Fatalf("cancels = %v", c)
	}
	if len(h.poster.Cards()) != 0 {
		t.Errorf("cards posted: %+v", h.poster.Cards())
	}
	esc := h.lastEscalation()
	if esc.Args["reason"] != "no_group_owner" || esc.Args["title"] != agent.AccessEscalationTitle ||
		esc.Args["summary"] != agent.AccessOwnerUnreachableSummary || esc.Requester.Initiator != mcpclient.InitiatorCode {
		t.Errorf("escalate = %+v", esc)
	}
	h.wantStatus(agent.StatusEscalated)
	// The settle waits for the turn on the thread's worker, so it follows the reply.
	h.wantPosts(agent.TextAck, answerWith(accessAnswer, agent.FooterOpen),
		"I could not reach the owner of SG-Finance, so I have passed your request to IT.")
	if it := h.itPosts(); len(it) != 1 || !strings.Contains(it[0], "no group owner") {
		t.Errorf("IT posts = %q", it)
	}
}

// covers: spec 0013 AC-10. A deleted, bot, or unconfirmed owner account
// cancels; so does an owner the directory shows without mail.
func TestAccessOwnerUnreachableCases(t *testing.T) {
	for name, change := range map[string]func(h *harness){
		"deleted": func(h *harness) {
			u := agenttest.Staff(ilseEmail)
			u.Deleted = true
			h.users.Set(ilseSlack, u)
		},
		"bot": func(h *harness) {
			u := agenttest.Staff(ilseEmail)
			u.IsBot = true
			h.users.Set(ilseSlack, u)
		},
		"no mail in the directory": func(h *harness) {
			v := accessView(mcpclient.StatusPending)
			v.ApproverEmail = ""
			h.appr.Views[approvalID] = v
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := accessRequested(t, change)
			if c := h.appr.Cancels(); len(c) != 1 || len(h.poster.Cards()) != 0 {
				t.Errorf("cancels %v, cards %v", c, h.poster.Cards())
			}
		})
	}
}

// covers: spec 0013 AC-10. A Slack hiccup or an unread owner never cancels:
// the attempt is used up, approval_card_post_failed is logged, and the card
// waits for the tick.
func TestAccessLookupHiccupWaits(t *testing.T) {
	for name, change := range map[string]func(h *harness){
		"rate limited": func(h *harness) { h.users.LookupErr = errors.New("slack_rate_limited") },
		"owner unread": func(h *harness) {
			v := accessView(mcpclient.StatusPending)
			v.ApproverEmail, v.ApproverName = "", ""
			h.appr.Views[approvalID] = v
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := accessRequested(t, change)
			cards := h.store.Cards()
			if len(h.appr.Cancels()) != 0 || len(h.poster.Cards()) != 0 || len(cards) != 1 || cards[0].PostAttempts != 1 {
				t.Errorf("cancels %v, posts %v, cards %+v", h.appr.Cancels(), h.poster.Cards(), cards)
			}
			if !strings.Contains(h.logs.String(), "approval_card_post_failed") {
				t.Error("no approval_card_post_failed log")
			}
			h.wantStatus(agent.StatusOpen)
		})
	}
}

// covers: spec 0013 AC-15, AC-17. An expired request escalates with
// approval_expired under the access title and headline, and names the owner.
func TestAccessExpiredEscalates(t *testing.T) {
	h := accessRequested(t, nil)
	h.appr.SetView(accessView(mcpclient.StatusExpired))
	h.a.Tick()
	h.a.Wait()

	esc := h.lastEscalation()
	if esc.Args["reason"] != "approval_expired" || esc.Args["title"] != agent.AccessEscalationTitle ||
		esc.Args["summary"] != agent.AccessExpiredSummary {
		t.Errorf("escalate = %+v", esc.Args)
	}
	if it := h.itPosts(); len(it) != 1 || !strings.Contains(it[0], "Access request not decided in time") || strings.Contains(it[0], "MFA") {
		t.Errorf("IT posts = %q", it)
	}
	h.wantStatus(agent.StatusEscalated)
	h.wantPosts(agent.TextAck, answerWith(accessAnswer, agent.FooterOpen),
		"Ilse Smit did not decide on SG-Finance in time, so I have passed your request to IT.")
	cards := h.poster.Cards()
	if lines := cards[len(cards)-1].Card.Lines; lines[len(lines)-1] != "Expired with no decision. Passed to IT." {
		t.Errorf("outcome = %q", lines[len(lines)-1])
	}
}

// covers: spec 0013 AC-15. A failed add escalates with its reason.
func TestAccessFailedEscalates(t *testing.T) {
	h := accessRequested(t, nil)
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: accessView(mcpclient.StatusFailed)}
	h.ownerClick(ilseSlack, true)
	if esc := h.lastEscalation(); esc.Args["reason"] != "approval_failed" ||
		esc.Args["summary"] != "The approved access request did not finish: add not confirmed." {
		t.Errorf("escalate = %+v", esc.Args)
	}
	h.wantPosts(agent.TextAck, answerWith(accessAnswer, agent.FooterOpen),
		"Something went wrong adding you to SG-Finance. IT has the details and will follow up.")
	cards := h.poster.Cards()
	if lines := cards[len(cards)-1].Card.Lines; lines[len(lines)-1] != "Approved, but the add did not finish (add not confirmed). Passed to IT." {
		t.Errorf("outcome = %q", lines[len(lines)-1])
	}
}

// covers: spec 0013 AC-17. The tick adopts a lost access request with no
// channel and posts it to the owner's DM, never to the IT channel.
func TestTickAdoptsAnAccessRequest(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	knownThread(t, h)
	h.users.Set(ilseSlack, agenttest.Staff(ilseEmail))
	h.appr.Views[approvalID] = accessView(mcpclient.StatusPending)
	h.a.Tick()
	h.a.Wait()
	cards := h.store.Cards()
	if len(cards) != 1 || cards[0].ChannelID != agenttest.DMChannel(ilseSlack) || cards[0].ApproverSlackID != ilseSlack {
		t.Fatalf("cards = %+v", cards)
	}
	if posted := h.poster.Cards(); len(posted) != 1 || posted[0].Channel != ilseSlack {
		t.Errorf("posts = %+v", posted)
	}
}
