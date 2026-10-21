package agent

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// Access requests (spec 0013): the model asks with group_access_request; the
// owner's DM card, who may click, the cancel when the owner cannot be
// reached, and every outcome message are code.

const toolGroupAccessRequest = "group_access_request"

// maxCardReasonRunes cuts the requester's reason on the card (AC-12).
const maxCardReasonRunes = 200

// maxCardNestedGroups is how many groups the card's "Also gives" line names
// before it says how many more (AC-12), so the line stays well inside
// Slack's 3000 character section limit.
const maxCardNestedGroups = 10

// amsterdam is the zone the owner's card shows the expiry in (AC-12); the
// agent binary embeds the zone database (time/tzdata), so it loads in a
// bare container. UTC is the fallback should it ever fail.
var amsterdam = sync.OnceValue(func() *time.Location {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		return time.UTC
	}
	return loc
})

// ownerCard is the group owner's card with its buttons (AC-12), built only
// from the approval view, every value escaped.
func ownerCard(v mcpclient.ApprovalView) Card {
	f := factsFromView(v)
	lines := []string{accessCardHeader, f.requester(), accessCardGroup + slackEscape(v.Target)}
	if len(v.NestedInto) > 0 {
		lines = append(lines, accessCardAlsoGives+slackEscape(nestedLine(v.NestedInto)))
	}
	lines = append(lines,
		accessCardReason+slackEscape(cutRunes(v.Reason, maxCardReasonRunes)),
		f.ticket(),
		cardExpires+v.ExpiresAt.In(amsterdam()).Format("2006-01-02 15:04")+accessCardAmsterdam,
		accessCardConfirm,
	)
	return Card{
		Fallback:   accessCardFallback + slackEscape(v.Target),
		Lines:      lines,
		ApprovalID: v.ID.String(),
		Confirm:    "Add " + v.RequesterAccount + " to " + v.Target + "?",
	}
}

// nestedLine joins the first maxCardNestedGroups names and counts the rest.
func nestedLine(names []string) string {
	if len(names) <= maxCardNestedGroups {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:maxCardNestedGroups], ", ") + " and " + strconv.Itoa(len(names)-maxCardNestedGroups) + " more"
}

// accessOutcomeCard replaces the owner's card once the request ended
// (AC-15): the fact lines, no reason, no expiry, no buttons, and one outcome
// line that says "you" for the owner.
func accessOutcomeCard(v mcpclient.ApprovalView) Card {
	f := factsFromView(v)
	return Card{
		Fallback: accessCardFallback + slackEscape(v.Target),
		Lines:    []string{accessCardHeader, f.requester(), accessCardGroup + slackEscape(v.Target), f.ticket(), accessOutcomeLine(v)},
	}
}

func accessOutcomeLine(v mcpclient.ApprovalView) string {
	switch v.Status {
	case mcpclient.StatusExecuted:
		return "Approved by you. " + slackEscape(v.RequesterAccount) + " added to " + slackEscape(v.Target) + "."
	case mcpclient.StatusDenied:
		return "Denied by you."
	case mcpclient.StatusExpired:
		return "Expired with no decision. Passed to IT."
	default:
		return "Approved, but the add did not finish (" + slackEscape(executionReason(v)) + "). Passed to IT."
	}
}

// postOwnerCard posts a group owner card as a DM to the owner (AC-10): the
// first post, the tick's repost, and an adopted card alike. It reads the
// view, claims an attempt, finds the owner in Slack by email, and posts, or
// cancels the request when the owner cannot be reached. A failed read of
// the view or of the owner is tried again next tick and uses up no attempt;
// a Slack hiccup uses up the attempt, and after three the next call cancels.
func (a *Agent) postOwnerCard(parent context.Context, card ApprovalCard) {
	if card.CardTS != "" {
		return
	}
	if card.PostAttempts >= maxCardPosts {
		a.cancelOwnerCard(parent, card)
		return
	}
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	defer cancel()
	v, err := a.deps.Approvals.Get(ctx, card.ApprovalID)
	switch {
	case err != nil:
		a.cardPostFailed(card.ApprovalID, err)
		return
	case v.Status != mcpclient.StatusPending:
		return // ended meanwhile: the tick settles it
	case v.OwnerUnread:
		a.cardPostFailed(card.ApprovalID, errOwnerUnread)
		return
	case v.ApproverEmail == "":
		a.cancelOwnerCard(parent, card) // the owner lost their mail or was disabled
		return
	}
	ok, err := a.deps.Store.RecordCardPostAttempt(ctx, card.ApprovalID, maxCardPosts, 2*a.cfg.ApprovalPollInterval)
	if err != nil || !ok {
		if err != nil {
			a.cardPostFailed(card.ApprovalID, err)
		}
		return
	}
	owner, err := a.deps.Slack.LookupByEmail(ctx, v.ApproverEmail)
	switch {
	case errors.Is(err, ErrSlackUserNotFound):
		a.cancelOwnerCard(parent, card)
		return
	case err != nil:
		a.cardPostFailed(card.ApprovalID, err)
		return
	case owner.ID == "" || !passesGate(owner, a.cfg.TeamID):
		a.cancelOwnerCard(parent, card)
		return
	}
	pctx, pcancel := a.detached(parent, a.cfg.PostTimeout)
	channel, ts, err := a.deps.Poster.PostCard(pctx, owner.ID, ownerCard(v))
	pcancel()
	if err != nil {
		a.cardPostFailed(card.ApprovalID, err)
		return
	}
	// A fresh context, as postCard: the reads above may have used up ctx,
	// and a card that went out must be stored or the tick posts it again.
	sctx, scancel := a.detached(parent, a.cfg.ToolTimeout)
	defer scancel()
	if err := a.deps.Store.SetCardPosted(sctx, card.ApprovalID, ts, channel, owner.ID); err != nil {
		a.cardPostFailed(card.ApprovalID, err)
		return
	}
	a.deps.Logger.Info("approval_card_posted", "approval_id", card.ApprovalID.String())
}

// errOwnerUnread is the cause logged when the tool server could not read
// the owner.
var errOwnerUnread = errors.New("agent: the tool server could not read the group owner")

func (a *Agent) cardPostFailed(id uuid.UUID, err error) {
	a.deps.Logger.Warn("approval_card_post_failed", "approval_id", id.String(), "cause", cause(err))
}

// cancelOwnerCard ends a request whose owner cannot be reached (AC-10,
// AC-11) and settles it as owner unreachable. A 409 means the approval
// already ended another way; the tick settles what it became.
func (a *Agent) cancelOwnerCard(parent context.Context, card ApprovalCard) {
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	v, err := a.deps.Approvals.Cancel(ctx, card.ApprovalID)
	cancel()
	var status *mcpclient.StatusError
	switch {
	case errors.As(err, &status) && status.Status == http.StatusConflict:
		return
	case err != nil:
		a.deps.Logger.Warn("approval_cancel_failed", "approval_id", card.ApprovalID.String(), "cause", cause(err))
		return
	}
	a.deps.Logger.Info("approval_cancelled", "approval_id", card.ApprovalID.String())
	a.settleOnThread(card, v)
}

// ownerClick checks a click on a group owner card (AC-13): it must come from
// the Slack account the card was posted to, and that account must still pass
// the spec 0008 gate. It returns the clicker's confirmed email for the tool
// server, or the refusal text.
func (a *Agent) ownerClick(c Click, card ApprovalCard) (email, refusal string) {
	if card.ApproverSlackID == "" || c.UserID != card.ApproverSlackID {
		return "", TextApprovalNotAllowed
	}
	ctx, cancel := a.detached(a.workCtx, a.cfg.PostTimeout)
	defer cancel()
	user, err := a.deps.Slack.UserInfo(ctx, c.UserID)
	switch {
	case err != nil:
		return "", TextApprovalUnavailable
	case !passesGate(user, a.cfg.TeamID):
		return "", TextApprovalNotAllowed
	}
	return strings.TrimSpace(user.Email), ""
}

// isOwnerUnreachable reports a request cancelled because its owner could not
// be reached: expired with the cancel reason, never a timeout (AC-15).
func isOwnerUnreachable(v mcpclient.ApprovalView) bool {
	return v.Status == mcpclient.StatusExpired && v.CancelReason == mcpclient.CancelOwnerUnreachable
}

// accessSettle is the access request's row of the settle table (AC-15, AC-16).
var accessSettle = settlePolicy{
	text: func(v mcpclient.ApprovalView) (string, bool) {
		group, owner := slackEscape(v.Target), slackEscape(ownerName(v))
		switch {
		case v.Status == mcpclient.StatusExecuted && ticketClosed(v):
			return AccessGrantedText(group), true
		case v.Status == mcpclient.StatusExecuted:
			return AccessGrantedText(group) + " " + accessTicketOpen, true
		case v.Status == mcpclient.StatusDenied:
			return owner + " declined your request for " + group + ". If you think you need it, talk to them directly.", true
		case isOwnerUnreachable(v):
			return "I could not reach the owner of " + group + ", so I have passed your request to IT.", true
		case v.Status == mcpclient.StatusExpired:
			return owner + " did not decide on " + group + " in time, so I have passed your request to IT.", true
		case v.Status == mcpclient.StatusFailed:
			return "Something went wrong adding you to " + group + ". IT has the details and will follow up.", true
		}
		return "", false
	},
	escalation: func(v mcpclient.ApprovalView) (reason, summary string, owes bool) {
		switch {
		case isOwnerUnreachable(v):
			return escalateNoGroupOwner, AccessOwnerUnreachableSummary, true
		case v.Status == mcpclient.StatusExpired:
			return escalateApprovalExpired, AccessExpiredSummary, true
		case v.Status == mcpclient.StatusFailed:
			return escalateApprovalFailed, AccessFailedSummaryPrefix + executionReason(v) + ".", true
		}
		return "", "", false
	},
	headline: func(reason string) string { return accessHeadlines[reason] },
	closeNote: func(v mcpclient.ApprovalView) string {
		if v.Status == mcpclient.StatusDenied {
			return AccessDeniedNote
		}
		return ""
	},
	title:       AccessEscalationTitle,
	endedText:   TextAccessEndedContactIT,
	outcomeCard: accessOutcomeCard,
}

// ownerName is the owner as the user's texts name them (AC-16).
func ownerName(v mcpclient.ApprovalView) string {
	if v.ApproverName != "" {
		return v.ApproverName
	}
	return v.ApproverAccount
}

// closeByCode closes the thread's ticket after a denied access request
// (AC-15): ticket_update with the code initiator, state closed, and a fixed
// note. A refused or failed close is logged and never blocks the settle.
func (a *Agent) closeByCode(ctx context.Context, conv Conversation, note string) {
	r, _, _, matched := a.reidentify(ctx, conv)
	if !matched || conv.TicketID == 0 {
		a.deps.Logger.Warn("agent_ticket_close_failed", "conversation_id", conv.ID, "cause", "not_matched")
		return
	}
	r.Initiator = mcpclient.InitiatorCode
	res, err := a.callTool(ctx, r, toolTicketUpdate, map[string]any{"state": "closed", "note": note})
	switch {
	case err != nil:
		a.deps.Logger.Warn("agent_ticket_close_failed", "conversation_id", conv.ID, "cause", cause(err))
	case res.IsError:
		a.deps.Logger.Warn("agent_ticket_close_failed", "conversation_id", conv.ID, "cause", res.Code)
	}
}
