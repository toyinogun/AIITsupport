package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

const itLead = "U0ITLEAD01"

var (
	approvalID = uuid.MustParse("6f1c2c4e-8d0a-4f53-9a51-0c3e5b7d9a10")
	expiresAt  = now.Add(30 * time.Minute)
)

const mfaAnswer = "IT needs to approve clearing your sign in methods. You'll get a message here when they decide, within 30 minutes. Nothing has changed yet."

func mfaScript() *agenttest.Model {
	return agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "New phone", "body": "Authenticator gone after a new phone."}),
		agenttest.CallTool("t2", "mfa_clear", map[string]any{}),
		agenttest.Answer(mfaAnswer),
	)
}

func pendingResult(ticketURL string) mcpclient.Result {
	data := map[string]any{
		"code": "pending_approval", "approval_id": approvalID.String(), "expires_at": expiresAt.Format(time.RFC3339),
		"expires_in_minutes": float64(30), "methods": "Authenticator app (1), phone <2>", "method_count": float64(3),
		"ticket_id": float64(42), "reused": false, "ticket_noted": true,
	}
	if ticketURL != "" {
		data["ticket_url"] = ticketURL
	}
	return mcpclient.Result{Text: "This request is waiting for IT approval.", Data: data, Code: "pending_approval"}
}

func mfaTools(ticketURL string) *agenttest.Tools {
	tools := agenttest.NewTools(janUser, false)
	tools.Set("mfa_clear", agenttest.Respond(pendingResult(ticketURL)))
	return tools
}

// view is the approval as the tool server would show it after status.
func view(status string) mcpclient.ApprovalView {
	decided := now.Add(time.Minute)
	v := mcpclient.ApprovalView{
		ID: approvalID, ToolName: "mfa_clear", Status: status, ExpiresAt: expiresAt, CreatedAt: now,
		RequesterSlackID: janSlack, RequesterAccount: janUser, SlackChannelID: janDM, SlackThreadTS: firstTS,
		TicketID: 42, TicketURL: "http://zammad.test/#ticket/zoom/42",
	}
	if status != mcpclient.StatusExpired {
		v.DecidedBy, v.DecidedAt = itLead, &decided
	}
	switch status {
	case mcpclient.StatusExecuted:
		v.Execution = &mcpclient.ApprovalExecution{Outcome: "ok", Result: map[string]any{
			"cleared_authenticator": float64(1), "cleared_phone": float64(2), "ticket_closed": true,
		}}
	case mcpclient.StatusFailed:
		v.Execution = &mcpclient.ApprovalExecution{Outcome: "error", Reason: "method delete failed"}
	}
	return v
}

// requested runs the request turn and checks the card went out.
func requested(t *testing.T, opts ...option) *harness {
	t.Helper()
	h := newHarness(t, mfaScript(), append([]option{withTools(mfaTools("http://zammad.test/#ticket/zoom/42"))}, opts...)...)
	h.send(dm("Ev1", "I got a new phone and my Authenticator is gone"))
	return h
}

func (h *harness) click(clicker string, approve bool) {
	h.t.Helper()
	h.a.Click(agent.Click{EventID: "Env1", ApprovalID: approvalID, UserID: clicker, Approve: approve, ChannelID: itChannel})
	h.a.Wait()
}

// covers: spec 0012 AC-6, AC-7. The pending result stores the card row in
// its transaction; the card goes to the IT channel as a new message with
// the fixed lines, escaped values, and the buttons; the conversation stays
// open and the model's reply goes out.
func TestMFARequestPostsTheCard(t *testing.T) {
	h := requested(t)

	h.wantTools("user_lookup", "ticket_create", "mfa_clear")
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen))
	h.wantStatus(agent.StatusOpen)
	cards := h.store.Cards()
	if len(cards) != 1 || cards[0].ApprovalID != approvalID || cards[0].ChannelID != itChannel || cards[0].PostAttempts != 1 ||
		cards[0].CardTS == "" || !cards[0].ExpiresAt.Equal(expiresAt) || cards[0].ConversationID != h.conv().ID {
		t.Fatalf("cards = %+v", cards)
	}
	posted := h.poster.Cards()
	if len(posted) != 1 || posted[0].Channel != itChannel || posted[0].Update {
		t.Fatalf("card posts = %+v", posted)
	}
	c := posted[0].Card
	want := []string{
		"MFA reset request",
		"Requester: <@" + janSlack + "> (" + janUser + ")",
		"Ticket: <http://zammad.test/#ticket/zoom/42|open in helpdesk>",
		"Methods: Authenticator app (1), phone &lt;2&gt;",
		"Expires: " + expiresAt.Format("15:04") + " UTC",
		"Confirm with the person by phone or in person before approving.",
	}
	if strings.Join(c.Lines, "\n") != strings.Join(want, "\n") || c.ApprovalID != approvalID.String() ||
		c.Fallback != "MFA reset request for <@"+janSlack+">" {
		t.Errorf("card = %#v", c)
	}
	if !strings.Contains(h.logs.String(), "approval_card_posted") {
		t.Error("no approval_card_posted log")
	}
}

// covers: spec 0012 AC-7. Without a ticket link the card names the id.
func TestMFACardWithoutTicketURL(t *testing.T) {
	h := newHarness(t, mfaScript(), withTools(mfaTools("")))
	h.send(dm("Ev1", "new phone"))
	if c := h.poster.Cards(); len(c) != 1 || c[0].Card.Lines[2] != "Ticket: id 42" {
		t.Errorf("cards = %+v", c)
	}
}

// covers: spec 0012 AC-6. A failed post leaves card_ts empty and logs
// approval_card_post_failed; the turn goes on.
func TestMFACardPostFails(t *testing.T) {
	h := newHarness(t, mfaScript(), withTools(mfaTools("")))
	h.poster.CardErr = errors.New("slack down")
	h.send(dm("Ev1", "new phone"))
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen))
	if c := h.store.Cards(); len(c) != 1 || c[0].CardTS != "" || c[0].PostAttempts != 1 {
		t.Errorf("cards = %+v", c)
	}
	if !strings.Contains(h.logs.String(), "approval_card_post_failed") {
		t.Error("no approval_card_post_failed log")
	}
}

// covers: spec 0012 AC-9, AC-15, AC-16, AC-17. An approve that executed is
// settled once: the card shows the outcome, the user gets mfa_cleared, and
// the conversation is resolved, with no model call.
func TestMFAApproveSettles(t *testing.T) {
	h := requested(t)
	modelCalls := len(h.model.Calls())
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: view(mcpclient.StatusExecuted)}

	h.click(itLead, true)

	if c := h.appr.Clicks(); len(c) != 1 || c[0].Clicker != itLead || !c[0].Approve {
		t.Fatalf("clicks = %+v", c)
	}
	h.wantStatus(agent.StatusResolved)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFACleared)
	cards := h.poster.Cards()
	last := cards[len(cards)-1]
	if !last.Update || last.TS != h.store.Cards()[0].CardTS || last.Card.ApprovalID != "" ||
		last.Card.Lines[len(last.Card.Lines)-1] != "Approved by <@"+itLead+">. Cleared 1 Authenticator and 2 phone methods. Ticket closed." {
		t.Errorf("update = %+v", last)
	}
	for _, line := range last.Card.Lines {
		if strings.HasPrefix(line, "Methods:") || strings.HasPrefix(line, "Confirm") {
			t.Errorf("the outcome card keeps %q", line)
		}
	}
	if c := h.store.Cards()[0]; !c.Settled || c.Outcome != "executed" {
		t.Errorf("card row = %+v", c)
	}
	if len(h.model.Calls()) != modelCalls || len(h.itPosts()) != 0 {
		t.Errorf("model calls %d, IT posts %v", len(h.model.Calls())-modelCalls, h.itPosts())
	}

	// A second settle of the same approval announces nothing more.
	h.click(itLead, true)
	if n := len(h.poster.Cards()); n != len(cards) {
		t.Errorf("a second settle updated the card again: %d", n)
	}
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFACleared)
}

// covers: spec 0012 AC-15, AC-16, AC-17. A deny escalates by code first
// with approval_denied, then settles: the card says who denied, the user
// gets mfa_denied, IT gets the escalation post, the conversation is escalated.
func TestMFADenyEscalates(t *testing.T) {
	h := requested(t)
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: view(mcpclient.StatusDenied)}

	h.click(itLead, false)

	esc := h.lastEscalation()
	if esc.Args["reason"] != "approval_denied" || esc.Args["title"] != agent.MFAEscalationTitle ||
		esc.Args["summary"] != agent.MFADeniedSummary || esc.Requester.Initiator != mcpclient.InitiatorCode || esc.Requester.TicketID != 42 {
		t.Errorf("escalate = %+v", esc)
	}
	h.wantStatus(agent.StatusEscalated)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFADenied)
	if it := h.itPosts(); len(it) != 1 || !strings.Contains(it[0], "MFA reset denied") || !strings.Contains(it[0], agent.MFADeniedSummary) {
		t.Errorf("IT posts = %q", it)
	}
	cards := h.poster.Cards()
	if last := cards[len(cards)-1].Card.Lines; last[len(last)-1] != "Denied by <@"+itLead+">. Escalated." {
		t.Errorf("outcome = %q", last[len(last)-1])
	}
}

// covers: spec 0012 AC-16. A failed clear names its reason on the card.
func TestMFAFailedOutcomeLine(t *testing.T) {
	h := requested(t)
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: view(mcpclient.StatusFailed)}
	h.click(itLead, true)
	cards := h.poster.Cards()
	lines := cards[len(cards)-1].Card.Lines
	if lines[len(lines)-1] != "Approved by <@"+itLead+">, but the clear did not finish (method delete failed). Escalated, check the account by hand." {
		t.Errorf("outcome = %q", lines[len(lines)-1])
	}
	if esc := h.lastEscalation(); esc.Args["reason"] != "approval_failed" ||
		esc.Args["summary"] != "The approved MFA reset did not finish: method delete failed." {
		t.Errorf("escalate = %+v", esc.Args)
	}
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAFailed)
}

// covers: spec 0012 AC-9. Refused and failed clicks get the clicker an
// ephemeral text and change nothing.
func TestMFARefusedClicks(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		want  string
	}{
		{"not an approver", func(h *harness) {
			h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Reason: mcpclient.ReasonNotAllowed, Approval: view("pending")}
		}, agent.TextApprovalNotAllowed},
		{"self", func(h *harness) {
			h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Reason: mcpclient.ReasonSelf, Approval: view("pending")}
		}, agent.TextApprovalSelf},
		{"late", func(h *harness) {
			h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Reason: mcpclient.ReasonExpired, Approval: view("expired")}
		}, agent.TextApprovalExpired},
		{"already decided", func(h *harness) {
			h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Reason: mcpclient.ReasonDecided, Approval: view("denied")}
		}, agent.TextApprovalDecided},
		{"timeout", func(h *harness) { h.appr.Block = true }, agent.TextApprovalProcessing},
		{"tool server down", func(h *harness) { h.appr.Err = &mcpclient.StatusError{Status: 502} }, agent.TextApprovalUnavailable},
		{"gone on the server", func(h *harness) { h.appr.Err = mcpclient.ErrApprovalNotFound }, agent.TextApprovalUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := requested(t)
			c.setup(h)
			h.click(itLead, true)
			eph := h.poster.Ephemerals()
			if len(eph) != 1 || eph[0].Text != c.want || eph[0].User != itLead || eph[0].Channel != itChannel {
				t.Errorf("ephemerals = %+v, want %q", eph, c.want)
			}
			h.wantStatus(agent.StatusOpen)
			if card := h.store.Cards()[0]; card.Settled {
				t.Error("a refused click settled the card")
			}
		})
	}
}

// covers: spec 0012 AC-9. A click on an approval with no card row is
// answered approval_unknown without calling the tool server.
func TestMFAClickUnknownCard(t *testing.T) {
	h := newHarness(t, agenttest.NewModel())
	h.click(itLead, true)
	if eph := h.poster.Ephemerals(); len(eph) != 1 || eph[0].Text != agent.TextApprovalUnknown || len(h.appr.Clicks()) != 0 {
		t.Errorf("ephemerals %+v, clicks %+v", eph, h.appr.Clicks())
	}
}

// covers: spec 0012 AC-15, AC-17. When the requester cannot be rebuilt the
// settle waits; past the limit, IT gets itNotMatched and the user
// mfa_ended_contact_it, with no escalate call.
func TestMFASettleRequesterGone(t *testing.T) {
	h := requested(t)
	h.users.Set(janSlack, agent.SlackUser{Deleted: true})
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: view(mcpclient.StatusDenied)}
	h.click(itLead, false)
	if card := h.store.Cards()[0]; card.Settled {
		t.Fatal("settled although the requester could not be rebuilt")
	}

	late := view(mcpclient.StatusDenied)
	late.CreatedAt, late.ExpiresAt = now.Add(-2*time.Hour), now.Add(-90*time.Minute)
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: late}
	h.click(itLead, false)
	if card := h.store.Cards()[0]; !card.Settled {
		t.Fatal("not settled past the limit")
	}
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAEndedContactIT)
	if it := h.itPosts(); len(it) != 1 || !strings.Contains(it[0], "not matched") {
		t.Errorf("IT posts = %q", it)
	}
	for _, c := range h.tools.Calls() {
		if c.Tool == "escalate" {
			t.Error("escalate was called for a requester who could not be rebuilt")
		}
	}
}

// covers: spec 0012 AC-15. A settle waits for a running turn in the same
// thread: it is queued on the thread's worker.
func TestMFASettleRunsOnTheThreadWorker(t *testing.T) {
	model := agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "New phone", "body": "Authenticator gone."}),
		agenttest.CallTool("t2", "mfa_clear", map[string]any{}),
		agenttest.Answer(mfaAnswer),
		agenttest.Answer("Still waiting on IT."),
	)
	h := newHarness(t, model, withTools(mfaTools("")))
	h.send(dm("Ev1", "new phone"))
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: view(mcpclient.StatusExecuted)}
	started := make(chan struct{})
	release := make(chan struct{})
	h.tools.Set("user_lookup", func(context.Context, agenttest.ToolCall) (mcpclient.Result, error) {
		close(started)
		<-release
		return agenttest.LookupResult(janUser, false), nil
	})
	in := dm("Ev2", "any news?")
	h.a.Submit(in)
	<-started
	h.a.Click(agent.Click{EventID: "Env1", ApprovalID: approvalID, UserID: itLead, Approve: true, ChannelID: itChannel})
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(strings.Join(h.poster.Texts(), "\n"), agent.TextMFACleared) {
		t.Fatal("the settle ran while a turn held the thread")
	}
	close(release)
	h.a.Wait()
	texts := h.poster.Texts()
	if texts[len(texts)-1] != agent.TextMFACleared {
		t.Errorf("last post = %q, want the settle after the turn", texts[len(texts)-1])
	}
}

// covers: spec 0012 AC-15 to AC-17 (amended 2026-10-20). A requester who
// denies their own request cancels it: it escalates by code with
// approval_cancelled and the cancel summary, the card says the requester
// cancelled, and the user gets mfa_cancelled, never mfa_denied.
func TestMFACancelByTheRequester(t *testing.T) {
	h := requested(t)
	cancelled := view(mcpclient.StatusDenied)
	cancelled.DecidedBy = janSlack
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: cancelled}

	h.click(janSlack, false)

	esc := h.lastEscalation()
	if esc.Args["reason"] != "approval_cancelled" || esc.Args["summary"] != agent.MFACancelledSummary ||
		esc.Requester.Initiator != mcpclient.InitiatorCode {
		t.Errorf("escalate = %+v", esc)
	}
	h.wantStatus(agent.StatusEscalated)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFACancelled)
	if it := h.itPosts(); len(it) != 1 || !strings.Contains(it[0], "MFA reset cancelled by the requester") ||
		!strings.Contains(it[0], agent.MFACancelledSummary) {
		t.Errorf("IT posts = %q", it)
	}
	cards := h.poster.Cards()
	if last := cards[len(cards)-1].Card.Lines; last[len(last)-1] != "Cancelled by the requester <@"+janSlack+">. Escalated." {
		t.Errorf("outcome = %q", last[len(last)-1])
	}
	if c := h.store.Cards()[0]; c.Outcome != "denied" {
		t.Errorf("card outcome = %q, want denied", c.Outcome)
	}
}

// covers: spec 0012 AC-16, AC-17. An executed clear that could not close
// the ticket says so on the card and in the user's text.
func TestMFAOutcomeTicketLeftOpen(t *testing.T) {
	h := requested(t)
	v := view(mcpclient.StatusExecuted)
	v.Execution.Result["ticket_closed"] = false
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: v}

	h.click(itLead, true)

	cards := h.poster.Cards()
	lines := cards[len(cards)-1].Card.Lines
	if lines[len(lines)-1] != "Approved by <@"+itLead+">. Cleared 1 Authenticator and 2 phone methods. Ticket left open." {
		t.Errorf("outcome = %q", lines[len(lines)-1])
	}
	h.wantStatus(agent.StatusResolved)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAClearedTicketOpen)
}

// covers: spec 0012 AC-16, AC-17. An executed approval whose outcome row is
// missing shows no counts and does not claim the ticket is closed.
func TestMFAExecutedWithoutAnExecution(t *testing.T) {
	h := requested(t)
	v := view(mcpclient.StatusExecuted)
	v.Execution = nil
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: v}

	h.click(itLead, true)

	cards := h.poster.Cards()
	lines := cards[len(cards)-1].Card.Lines
	if lines[len(lines)-1] != "Approved by <@"+itLead+">. Cleared. Ticket left open." {
		t.Errorf("outcome = %q", lines[len(lines)-1])
	}
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAClearedTicketOpen)
}

// covers: spec 0012 AC-15, AC-16. A failed approval whose view carries no
// execution reason is named "execution not confirmed" on the card and in
// the escalation summary.
func TestMFAFailedWithoutAReasonIsNotConfirmed(t *testing.T) {
	h := requested(t)
	v := view(mcpclient.StatusFailed)
	v.Execution = nil
	h.appr.Views[approvalID] = v

	h.a.Tick()

	cards := h.poster.Cards()
	lines := cards[len(cards)-1].Card.Lines
	if lines[len(lines)-1] != "Approved by <@"+itLead+">, but the clear did not finish (execution not confirmed). Escalated, check the account by hand." {
		t.Errorf("outcome = %q", lines[len(lines)-1])
	}
	if esc := h.lastEscalation(); esc.Args["reason"] != "approval_failed" ||
		esc.Args["summary"] != "The approved MFA reset did not finish: execution not confirmed." {
		t.Errorf("escalate = %+v", esc.Args)
	}
	h.wantStatus(agent.StatusEscalated)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAFailed)
}

// covers: spec 0012 AC-16. A card update that fails is logged and never
// undoes the settle: the row is settled and the user still gets the text.
func TestMFACardUpdateFailsKeepsTheSettle(t *testing.T) {
	h := requested(t)
	h.poster.CardErr = errors.New("slack down")
	h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: view(mcpclient.StatusExecuted)}

	h.click(itLead, true)

	if !strings.Contains(h.logs.String(), "approval_card_update_failed") {
		t.Errorf("no approval_card_update_failed log: %s", h.logs.String())
	}
	if c := h.store.Cards()[0]; !c.Settled || c.Outcome != "executed" {
		t.Errorf("card row = %+v", c)
	}
	h.wantStatus(agent.StatusResolved)
	h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFACleared)
}

// covers: spec 0012 AC-5, AC-6. A reused pending result in a later turn
// owes no new card: the card row exists, so nothing is posted again.
func TestMFAReusedResultPostsNoSecondCard(t *testing.T) {
	model := agenttest.NewModel(
		agenttest.CallTool("t1", "ticket_create", map[string]any{"title": "New phone", "body": "Authenticator gone."}),
		agenttest.CallTool("t2", "mfa_clear", map[string]any{}),
		agenttest.Answer(mfaAnswer),
		agenttest.CallTool("t3", "mfa_clear", map[string]any{}),
		agenttest.Answer("Still waiting on IT."),
	)
	tools := mfaTools("")
	reused := pendingResult("")
	reused.Data["reused"] = true
	delete(reused.Data, "methods")
	delete(reused.Data, "method_count")
	calls := 0
	tools.Set("mfa_clear", func(context.Context, agenttest.ToolCall) (mcpclient.Result, error) {
		calls++
		if calls == 1 {
			return pendingResult(""), nil
		}
		return reused, nil
	})
	h := newHarness(t, model, withTools(tools))

	h.send(dm("Ev1", "new phone"))
	h.send(dm("Ev2", "did you ask IT again?"))

	if calls != 2 {
		t.Fatalf("mfa_clear calls = %d, want 2", calls)
	}
	if posted := h.poster.Cards(); len(posted) != 1 {
		t.Errorf("card posts = %d, want 1", len(posted))
	}
	if c := h.store.Cards(); len(c) != 1 || c[0].PostAttempts != 1 {
		t.Errorf("cards = %+v", c)
	}
	h.wantStatus(agent.StatusOpen)
}

// covers: spec 0012 AC-15 (amended 2026-10-20). The settle limit for a
// requester who cannot be rebuilt is the approval's own timeout (expires_at
// minus created_at), never the agent's APPROVAL_TIMEOUT (30m here).
func TestMFASettleLimitIsTheApprovalsOwnTimeout(t *testing.T) {
	cases := []struct {
		name             string
		created, expires time.Duration // from now
		settled          bool
	}{
		// A 10m approval that expired 20m ago is past its limit, though 30m would not be.
		{"shorter than the setting", -30 * time.Minute, -20 * time.Minute, true},
		// A 2h approval that expired an hour ago is within its limit, though 30m would be past.
		{"longer than the setting", -3 * time.Hour, -time.Hour, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := requested(t)
			h.users.Set(janSlack, agent.SlackUser{Deleted: true})
			v := view(mcpclient.StatusDenied)
			v.CreatedAt, v.ExpiresAt = now.Add(c.created), now.Add(c.expires)
			h.appr.Decisions[approvalID] = mcpclient.DecisionResponse{Moved: true, Approval: v}

			h.click(itLead, false)

			if got := h.store.Cards()[0].Settled; got != c.settled {
				t.Errorf("settled = %v, want %v", got, c.settled)
			}
			if c.settled {
				h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen), agent.TextMFAEndedContactIT)
			} else {
				h.wantPosts(agent.TextAck, answerWith(mfaAnswer, agent.FooterOpen))
			}
		})
	}
}
