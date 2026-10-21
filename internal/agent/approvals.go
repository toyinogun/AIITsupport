package agent

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// Approvals (spec 0012): the model can only ask (mfa_clear); the card, the
// click, the decision, the outcome, and every message about it are code.

const (
	toolMFAClear        = "mfa_clear"
	codePendingApproval = "pending_approval"
	// maxCardPosts is how many times one card may be posted (AC-14).
	maxCardPosts = 3
)

// pendingCard is the card row a pending_approval result of mfa_clear or
// group_access_request owes (spec 0012 AC-6, spec 0013 AC-9), or nil. A
// result that does not parse owes none here. A group owner card has no
// channel until its first DM post.
func (a *Agent) pendingCard(tool string, r mcpclient.Result) *ApprovalCard {
	if (tool != toolMFAClear && tool != toolGroupAccessRequest) || r.IsError || r.Code != codePendingApproval {
		return nil
	}
	idText, _ := r.Data["approval_id"].(string)
	expText, _ := r.Data["expires_at"].(string)
	id, err := uuid.Parse(idText)
	if err != nil {
		return nil
	}
	expires, err := time.Parse(time.RFC3339, expText)
	if err != nil {
		return nil
	}
	card := &ApprovalCard{ApprovalID: id, ToolName: tool, ExpiresAt: expires}
	if tool == toolMFAClear {
		card.ChannelID = a.cfg.ITChannel
	}
	return card
}

// postFirstCard posts the card a new pending result owes (spec 0012 AC-7,
// spec 0013 AC-10): to the IT channel for an IT approval, as a DM to the
// group owner for an access request.
func (a *Agent) postFirstCard(parent context.Context, conv Conversation, card ApprovalCard, data map[string]any) {
	card.ConversationID = conv.ID // the row's, so a cancel can settle on this thread
	if card.ToolName == toolGroupAccessRequest {
		a.postOwnerCard(parent, card)
		return
	}
	a.postCard(parent, card.ApprovalID, requestCard(factsFromResult(conv, data, card.ExpiresAt), stringOf(data["methods"]), card.ApprovalID))
}

// cardFacts are the values every form of the card shows (AC-7).
type cardFacts struct {
	slackID, account string
	ticketURL        string
	ticketID         int64
	expires          time.Time
}

// factsFromResult takes the requester from the conversation row and the
// rest from the pending result: the first post.
func factsFromResult(c Conversation, data map[string]any, expires time.Time) cardFacts {
	f := cardFacts{slackID: c.RequesterSlackID, account: c.DirectoryAccount, expires: expires}
	f.ticketURL, _ = data["ticket_url"].(string)
	f.ticketID, _ = wholeNumber(data["ticket_id"])
	return f
}

// factsFromView takes everything from the approval view: a repost or an update.
func factsFromView(v mcpclient.ApprovalView) cardFacts {
	return cardFacts{
		slackID: v.RequesterSlackID, account: v.RequesterAccount,
		ticketURL: v.TicketURL, ticketID: v.TicketID, expires: v.ExpiresAt,
	}
}

func (f cardFacts) requester() string {
	return cardRequester + "<@" + slackEscape(f.slackID) + "> (" + slackEscape(f.account) + ")"
}

func (f cardFacts) ticket() string {
	if f.ticketURL != "" {
		return cardTicket + "<" + slackEscape(f.ticketURL) + "|" + cardTicketLinkLabel + ">"
	}
	return cardTicket + "id " + strconv.FormatInt(f.ticketID, 10)
}

func (f cardFacts) expiry() string { return cardExpires + f.expires.UTC().Format("15:04") + " UTC" }

func (f cardFacts) fallback() string { return cardFallback + "<@" + slackEscape(f.slackID) + ">" }

// requestCard is the card with its buttons (AC-7). methods is the result's
// method summary on the first post, empty on a card the tick posts.
func requestCard(f cardFacts, methods string, id uuid.UUID) Card {
	methodsLine := cardMethodsInNote
	if methods != "" {
		methodsLine = cardMethods + slackEscape(methods)
	}
	return Card{
		Fallback:   f.fallback(),
		Lines:      []string{cardHeader, f.requester(), f.ticket(), methodsLine, f.expiry(), cardConfirm},
		ApprovalID: id.String(),
		Confirm:    cardConfirmMFA,
	}
}

// outcomeCard replaces the card once the approval ended (AC-16): the facts,
// no methods line, no buttons, and one outcome line.
func outcomeCard(v mcpclient.ApprovalView) Card {
	f := factsFromView(v)
	return Card{Fallback: f.fallback(), Lines: []string{cardHeader, f.requester(), f.ticket(), f.expiry(), outcomeLine(v)}}
}

func outcomeLine(v mcpclient.ApprovalView) string {
	by := "<@" + slackEscape(v.DecidedBy) + ">"
	switch v.Status {
	case mcpclient.StatusExecuted:
		var res map[string]any
		if v.Execution != nil {
			res = v.Execution.Result
		}
		ticket := "Ticket left open."
		if ticketClosed(v) {
			ticket = "Ticket closed."
		}
		if res == nil {
			// The outcome row is missing (ErrAuditIncomplete): no counts to show.
			return "Approved by " + by + ". Cleared. " + ticket
		}
		auth, _ := wholeNumber(res["cleared_authenticator"])
		phone, _ := wholeNumber(res["cleared_phone"])
		return "Approved by " + by + ". Cleared " + strconv.FormatInt(auth, 10) + " Authenticator and " +
			strconv.FormatInt(phone, 10) + " phone methods. " + ticket
	case mcpclient.StatusDenied:
		if isCancel(v) {
			return "Cancelled by the requester " + by + ". Escalated."
		}
		return "Denied by " + by + ". Escalated."
	case mcpclient.StatusExpired:
		return "Expired with no decision. Escalated."
	default:
		return "Approved by " + by + ", but the clear did not finish (" + slackEscape(executionReason(v)) +
			"). Escalated, check the account by hand."
	}
}

func executionReason(v mcpclient.ApprovalView) string {
	if v.Execution == nil || v.Execution.Reason == "" {
		return "execution not confirmed"
	}
	return v.Execution.Reason
}

// postCard posts a card as a new IT channel message: it claims an attempt
// first, so a post is never doubled and at most maxCardPosts are sent, then
// stores the card's ts. A failed post leaves card_ts empty for the tick.
func (a *Agent) postCard(parent context.Context, id uuid.UUID, card Card) {
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	defer cancel()
	ok, err := a.deps.Store.RecordCardPostAttempt(ctx, id, maxCardPosts, 2*a.cfg.ApprovalPollInterval)
	if err != nil || !ok {
		if err != nil {
			a.deps.Logger.Warn("approval_card_post_failed", "approval_id", id.String(), "cause", cause(err))
		}
		return
	}
	pctx, pcancel := a.detached(parent, a.cfg.PostTimeout)
	channel, ts, err := a.deps.Poster.PostCard(pctx, a.cfg.ITChannel, card)
	pcancel()
	if err != nil {
		a.deps.Logger.Warn("approval_card_post_failed", "approval_id", id.String(), "cause", cause(err))
		return
	}
	sctx, scancel := a.detached(parent, a.cfg.ToolTimeout)
	defer scancel()
	if err := a.deps.Store.SetCardPosted(sctx, id, ts, channel, ""); err != nil {
		a.deps.Logger.Warn("approval_card_post_failed", "approval_id", id.String(), "cause", cause(err))
		return
	}
	a.deps.Logger.Info("approval_card_posted", "approval_id", id.String())
}

// Click takes one Approve or Deny click. It never blocks: the listener
// calls it on its event loop (AC-8).
func (a *Agent) Click(c Click) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopping {
		return
	}
	a.wg.Go(func() { a.click(c) })
}

// click sends the decision to the tool server and answers the clicker
// (AC-9). A move that happened is settled on the conversation's worker.
func (a *Agent) click(c Click) {
	ctx, cancel := a.detached(a.workCtx, a.cfg.ToolTimeout)
	card, err := a.deps.Store.GetApprovalCard(ctx, c.ApprovalID)
	cancel()
	switch {
	case errors.Is(err, ErrNoCard):
		a.ephemeral(c, TextApprovalUnknown)
		return
	case err != nil:
		a.ephemeral(c, TextApprovalUnavailable)
		return
	}
	var email string
	if card.ToolName == toolGroupAccessRequest {
		var refusal string
		if email, refusal = a.ownerClick(c, card); refusal != "" {
			a.ephemeral(c, refusal)
			return
		}
	}
	dctx, dcancel := context.WithTimeout(a.workCtx, a.cfg.ApprovalDecideTimeout)
	res, err := a.deps.Approvals.Decide(dctx, c.ApprovalID, c.UserID, email, c.Approve)
	dcancel()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		// The decision may have been recorded; the tick settles it.
		a.ephemeral(c, TextApprovalProcessing)
	case errors.Is(err, mcpclient.ErrApprovalNotFound):
		a.ephemeral(c, TextApprovalUnknown)
	case err != nil:
		a.ephemeral(c, TextApprovalUnavailable)
	case !res.Moved:
		a.deps.Logger.Info("approval_click_refused", "approval_id", c.ApprovalID.String(), "reason", res.Reason)
		a.ephemeral(c, refusedClickText(res.Reason))
	default:
		a.settleOnThread(card, res.Approval)
	}
}

func refusedClickText(reason string) string {
	switch reason {
	case mcpclient.ReasonNotAllowed:
		return TextApprovalNotAllowed
	case mcpclient.ReasonSelf:
		return TextApprovalSelf
	case mcpclient.ReasonExpired:
		return TextApprovalExpired
	default:
		return TextApprovalDecided
	}
}

// ephemeral answers the clicker only, in the channel they clicked in.
func (a *Agent) ephemeral(c Click, text string) {
	ctx, cancel := a.detached(a.workCtx, a.cfg.PostTimeout)
	defer cancel()
	if err := a.deps.Poster.Ephemeral(ctx, c.ChannelID, c.UserID, text); err != nil {
		a.deps.Logger.Warn("agent_post_failed", "event_id", c.EventID, "kind", "approval_ephemeral", "cause", cause(err))
	}
}

// settleOnThread queues the settle on the conversation's worker, so it
// never races a model turn (AC-15). At most one settle per approval is
// queued at a time, so a busy thread does not collect one from every tick.
func (a *Agent) settleOnThread(card ApprovalCard, v mcpclient.ApprovalView) {
	if !a.claimSettle(card.ApprovalID) {
		return // already queued: it reads the outcome when it runs
	}
	ctx, cancel := a.detached(a.workCtx, a.cfg.ToolTimeout)
	conv, err := a.deps.Store.GetConversation(ctx, card.ConversationID)
	cancel()
	if err != nil {
		a.releaseSettle(card.ApprovalID)
		a.deps.Logger.Warn("agent_history_read_failed", "conversation_id", card.ConversationID, "cause", cause(err))
		return
	}
	run := func() {
		defer a.releaseSettle(card.ApprovalID)
		a.settle(a.workCtx, card, conv, v)
	}
	if !a.onThread(conv.ChannelID, conv.ThreadTS, run) {
		a.releaseSettle(card.ApprovalID)
		a.deps.Logger.Info("approval_settle_deferred", "approval_id", card.ApprovalID.String())
	}
}

func (a *Agent) claimSettle(id uuid.UUID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settling[id] {
		return false
	}
	a.settling[id] = true
	return true
}

func (a *Agent) releaseSettle(id uuid.UUID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.settling, id)
}

// settleEscalations are the code only escalate reasons and fixed summaries
// for an approval that did not execute (spec 0012, AC-15).
var settleEscalations = map[string]string{
	mcpclient.StatusDenied:  escalateApprovalDenied,
	mcpclient.StatusExpired: escalateApprovalExpired,
	mcpclient.StatusFailed:  escalateApprovalFailed,
}

// settlePolicy is one tool's row of the settle table (spec 0013, AC-15):
// the user's fixed text, the escalation an ending owes, the code close a
// deny owes, and the card's outcome form.
type settlePolicy struct {
	// text is the user's one fixed text; ok is false while not ended.
	text func(v mcpclient.ApprovalView) (text string, ok bool)
	// escalation is the code only reason and fixed summary owed, if any.
	escalation func(v mcpclient.ApprovalView) (reason, summary string, owes bool)
	// headline is the IT post's headline for a reason, "" for the default.
	headline func(reason string) string
	// closeNote is the note of a code ticket close owed, "" for none.
	closeNote   func(v mcpclient.ApprovalView) string
	title       string // the escalation ticket's title when one is opened
	endedText   string // the user's text when no escalation could be made
	outcomeCard func(v mcpclient.ApprovalView) Card
}

// mfaSettle is mfa_clear's row (spec 0012, AC-15 to AC-17).
var mfaSettle = settlePolicy{
	text: settleText,
	escalation: func(v mcpclient.ApprovalView) (string, string, bool) {
		reason, owes := settleReason(v)
		return reason, settleSummary(v), owes
	},
	headline:    func(string) string { return "" },
	closeNote:   func(mcpclient.ApprovalView) string { return "" },
	title:       MFAEscalationTitle,
	endedText:   TextMFAEndedContactIT,
	outcomeCard: outcomeCard,
}

// settlePolicyFor is the settle table, keyed by the card's tool.
func settlePolicyFor(tool string) settlePolicy {
	if tool == toolGroupAccessRequest {
		return accessSettle
	}
	return mfaSettle
}

// settle announces one ended approval once (spec 0012 AC-15 to AC-17, spec
// 0013 AC-15). An ending that owes an escalation escalates first, at least
// once, and a denied access request closes its ticket by code; then, in one
// transaction, the card row and the conversation settle; only after that
// commit do the card update, the user's fixed text, and the IT post go out,
// at most once each.
func (a *Agent) settle(ctx context.Context, card ApprovalCard, conv Conversation, v mcpclient.ApprovalView) {
	p := settlePolicyFor(card.ToolName)
	userText, ok := p.text(v)
	if !ok {
		return // not ended yet
	}
	status := StatusResolved
	var (
		escalated  *escalationOutcome
		esc        codeEscalation
		account    string
		notMatched bool
	)
	if reason, summary, owes := p.escalation(v); owes {
		status = StatusEscalated
		r, acct, _, matched := a.reidentify(ctx, conv)
		switch {
		case !matched && !a.pastSettleLimit(v):
			return // tried again on a later tick
		case !matched:
			notMatched, userText = true, p.endedText
		default:
			account = acct
			esc = codeEscalation{
				reason: reason, title: p.title, summary: summary, itSummary: slackEscape(summary), headline: p.headline(reason),
			}
			o := a.escalateByCode(ctx, r, esc)
			a.logEscalated(ctx, "", Reason(reason), o.exists(), o.cause)
			if !o.exists() {
				return // escalation is at least once: tried again on a later tick
			}
			escalated = &o
		}
	} else if note := p.closeNote(v); note != "" {
		a.closeByCode(ctx, conv, note)
	}
	sctx, cancel := a.detached(ctx, a.cfg.ToolTimeout)
	won, err := a.deps.Store.SettleCard(sctx, card.ApprovalID, v.Status, status)
	cancel()
	if err != nil {
		a.deps.Logger.Warn("agent_status_write_failed", "conversation_id", conv.ID, "cause", cause(err))
		return
	}
	if !won {
		return // another settle won and announced it
	}
	a.updateCard(ctx, card, v)
	a.postTo(ctx, conv.ChannelID, conv.ThreadTS, "", "approval_outcome", userText)
	switch {
	case escalated != nil:
		a.announce(ctx, "", *escalated, esc, conv.RequesterSlackID, account, a.callsOrNone(ctx, conv.ID))
	case notMatched:
		a.postIT(ctx, "", kindITNotMatched, itNotMatched(conv.RequesterSlackID, codeNotMatched))
	}
	a.deps.Logger.Info("approval_settled", "approval_id", card.ApprovalID.String(), "outcome", v.Status)
}

// pastSettleLimit reports whether the approval ended more than its own
// timeout ago (expires_at minus created_at, the APPROVAL_TIMEOUT it was
// created with, never the agent's current setting), after which a requester
// who cannot be rebuilt is told to contact IT (AC-15).
func (a *Agent) pastSettleLimit(v mcpclient.ApprovalView) bool {
	timeout := v.ExpiresAt.Sub(v.CreatedAt)
	return a.deps.Now().After(v.ExpiresAt.Add(timeout))
}

// isCancel reports a denied approval decided by its own requester: a cancel
// (spec 0012 AC-15, amended 2026-10-20; AC-18 lets a listed requester deny).
func isCancel(v mcpclient.ApprovalView) bool {
	return v.Status == mcpclient.StatusDenied && v.DecidedBy != "" && v.DecidedBy == v.RequesterSlackID
}

// settleReason is the code only escalate reason an ended approval owes, if any.
func settleReason(v mcpclient.ApprovalView) (string, bool) {
	if isCancel(v) {
		return escalateApprovalCancelled, true
	}
	reason, owes := settleEscalations[v.Status]
	return reason, owes
}

// settleText is the user's one fixed text for an ended approval (AC-17).
func settleText(v mcpclient.ApprovalView) (string, bool) {
	switch {
	case isCancel(v):
		return TextMFACancelled, true
	case v.Status == mcpclient.StatusExecuted && !ticketClosed(v):
		return TextMFAClearedTicketOpen, true
	}
	text, ok := settleTexts[v.Status]
	return text, ok
}

// ticketClosed reports the execution's ticket_closed; a missing result is
// not closed.
func ticketClosed(v mcpclient.ApprovalView) bool {
	if v.Execution == nil {
		return false
	}
	closed, _ := v.Execution.Result["ticket_closed"].(bool)
	return closed
}

func settleSummary(v mcpclient.ApprovalView) string {
	switch {
	case isCancel(v):
		return MFACancelledSummary
	case v.Status == mcpclient.StatusDenied:
		return MFADeniedSummary
	case v.Status == mcpclient.StatusExpired:
		return MFAExpiredSummary
	}
	return MFAFailedSummaryPrefix + executionReason(v) + "."
}

// updateCard replaces the posted card with its outcome; a failure is
// logged and never undoes the settle (AC-16).
func (a *Agent) updateCard(parent context.Context, card ApprovalCard, v mcpclient.ApprovalView) {
	if card.CardTS == "" {
		ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
		fresh, err := a.deps.Store.GetApprovalCard(ctx, card.ApprovalID)
		cancel()
		if err != nil || fresh.CardTS == "" {
			return // never posted: nothing to update
		}
		card = fresh
	}
	ctx, cancel := a.detached(parent, a.cfg.PostTimeout)
	defer cancel()
	if err := a.deps.Poster.UpdateCard(ctx, card.ChannelID, card.CardTS, settlePolicyFor(card.ToolName).outcomeCard(v)); err != nil {
		a.deps.Logger.Warn("approval_card_update_failed", "approval_id", card.ApprovalID.String(), "cause", cause(err))
	}
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
