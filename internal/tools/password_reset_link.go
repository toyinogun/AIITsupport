package tools

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// ResetLinkSentText is the success text the model reads (spec 0011).
const ResetLinkSentText = "Reset link sent to the requester."

// ResetURLKey is the result key that holds the link. Agent code removes it
// before anything is stored or shown to the model (spec 0011, AC-5), and the
// audit row never keeps it.
const ResetURLKey = "reset_url"

// resetPath is where the reset page serves a link: RESET_PUBLIC_URL + resetPath + token.
const resetPath = "/reset/"

// noteTimeLayout is how the "link sent" note shows the expiry.
const noteTimeLayout = "2006-01-02 15:04 UTC"

func passwordResetLinkTool() Tool {
	return Tool{
		Name: ToolPasswordResetLink,
		Description: "Send the requester a single use link to set a new password themselves; it also clears a lockout. " +
			"Use it when they forgot their password or it expired, or when they report a lockout but the account is not " +
			"locked. Needs this conversation's ticket first (call ticket_create). The assistant's code posts the link; " +
			"never write or repeat a URL yourself. Ends the conversation.",
		Tier:        audit.TierLow,
		Target:      TargetRequester,
		StoreResult: []string{"link_id", "expires_at", "expires_in_minutes", "voided_previous", "ticket_noted"},
		Handler:     ResetHandler(passwordResetLink),
	}
}

// passwordResetLink issues a reset link for the requester's own account
// (spec 0011, AC-3 and AC-4). Every audit read comes before any Zammad call.
func passwordResetLink(ctx context.Context, d ResetDeps, in Input) (Result, error) {
	if in.Requester.TicketID == 0 {
		return refused(refuseNoTicket), nil
	}
	if r, err := resetEscalationState(ctx, d.History, in.Requester); err != nil || r != nil {
		return refusedOrFailed(r, err)
	}
	if r, err := resetTicketCheck(ctx, d.Helpdesk, in); err != nil || r != nil {
		return refusedOrFailed(r, err)
	}
	// A second layer: identify matches only enabled accounts, so this does
	// not fire today (spec 0011, the AC-3 amendment).
	if !in.Account.Enabled {
		return refused(refuseAccountDisabled), nil
	}
	if err := d.Accounts.CheckAccount(ctx, in.Account.DN); errors.Is(err, ErrGroupProtected) {
		return refused(refuseProtected), nil
	} else if err != nil {
		return Result{}, err
	}
	n, err := d.Links.CountRecent(ctx, in.Account.Username, d.Settings.Window)
	if err != nil {
		return Result{}, failed(err)
	}
	if n >= int64(d.Settings.LinkLimit) {
		return refused(refuseRateLimited), nil
	}

	callID, _ := audit.CallIDFrom(ctx) // Run always sets it
	issued, err := d.Links.Issue(ctx, resetlink.Issue{
		Account:          in.Account.Username,
		RequesterSlackID: in.Requester.SlackID,
		ChannelID:        in.Requester.ChannelID,
		ThreadTS:         in.Requester.ThreadTS,
		TicketID:         in.Requester.TicketID,
		IssueCallID:      callID,
		TTL:              d.Settings.TTL,
	})
	if err != nil {
		return Result{}, failed(err)
	}
	noted := d.Helpdesk.addInternalNote(ctx, int(in.Requester.TicketID),
		"Password reset link sent to the requester. Single use, valid until "+
			issued.ExpiresAt.UTC().Format(noteTimeLayout)+".") == nil
	return Result{
		Result: resultData(map[string]any{
			"link_id":            issued.ID.String(),
			"expires_at":         issued.ExpiresAt.UTC().Format(time.RFC3339),
			"expires_in_minutes": int64(d.Settings.TTL / time.Minute),
			"voided_previous":    issued.Voided,
			"ticket_noted":       noted,
			ResetURLKey:          d.Settings.PublicURL + resetPath + issued.Token,
		}),
		Text: ResetLinkSentText,
	}, nil
}

// resetEscalationState refuses a thread IT already holds, or one with a
// refused privileged request still owed an escalation.
func resetEscalationState(ctx context.Context, h History, r Requester) (*refusal, error) {
	escalated, err := h.ThreadHas(ctx, ToolEscalate, audit.OutcomeOK, r.ChannelID, r.ThreadTS)
	if err != nil {
		return nil, err
	}
	if escalated {
		return &refuseAlreadyEscalated, nil
	}
	blocked, err := h.ThreadHas(ctx, ToolPrivilegedAccessChange, audit.OutcomeDeniedPolicy, r.ChannelID, r.ThreadTS)
	if err != nil {
		return nil, err
	}
	if blocked {
		return &refuseEscalateFirst, nil
	}
	return nil, nil
}

// resetTicketCheck: the requester has a Zammad user, the _meta ticket is
// theirs, and it is neither closed nor merged.
func resetTicketCheck(ctx context.Context, h *Helpdesk, in Input) (*refusal, error) {
	customer, ok, err := h.customer(ctx, in.Account.Username)
	if err != nil {
		return nil, helpdeskFailure(err)
	}
	if !ok {
		return &refuseNoHelpdeskAccount, nil
	}
	ticket, err := h.api.GetTicket(ctx, int(in.Requester.TicketID))
	if zammad.Status(err) == http.StatusNotFound {
		return &refuseNoTicket, nil
	}
	if err != nil {
		return nil, helpdeskFailure(err)
	}
	if ticket.CustomerID != customer.ID {
		return &refuseNotYours, nil
	}
	closed, err := h.isClosed(ctx, ticket)
	if err != nil {
		return nil, helpdeskFailure(err)
	}
	if closed {
		return &refuseTicketClosed, nil
	}
	return nil, nil
}

// refusedOrFailed ends a call on a refusal, or on a failure for err (already
// classified, or failed when it is not).
func refusedOrFailed(r *refusal, err error) (Result, error) {
	if err != nil {
		if _, ok := errors.AsType[*failure](err); ok {
			return Result{}, err
		}
		return Result{}, failed(err)
	}
	return refused(*r), nil
}

// isClosed reports whether a ticket is closed or merged.
func (h *Helpdesk) isClosed(ctx context.Context, t zammad.Ticket) (bool, error) {
	_, byID, err := h.states(ctx)
	if err != nil {
		return false, err
	}
	name := byID[t.StateID]
	return name == zammadClosed || name == zammadMerged, nil
}

// addInternalNote adds one internal note to a ticket, leaving its state.
func (h *Helpdesk) addInternalNote(ctx context.Context, id int, body string) error {
	_, err := h.api.UpdateTicket(ctx, id, zammad.TicketUpdate{Article: internalNote(body)}, "")
	return err
}

// CloseWithNote adds an internal note and closes the ticket in one update
// (the reset page, spec 0011 AC-11). It does nothing to a ticket that is
// already closed or merged and reports closed false.
func (h *Helpdesk) CloseWithNote(ctx context.Context, id int, body string) (closed bool, err error) {
	ticket, err := h.api.GetTicket(ctx, id)
	if err != nil {
		return false, err
	}
	done, err := h.isClosed(ctx, ticket)
	if err != nil || done {
		return false, err
	}
	if _, err := h.api.UpdateTicket(ctx, id, zammad.TicketUpdate{State: zammadClosed, Article: internalNote(body)}, ""); err != nil {
		return false, err
	}
	return true, nil
}

func internalNote(body string) *zammad.NewArticle {
	return &zammad.NewArticle{
		Body:        body,
		ContentType: "text/plain",
		Type:        zammad.ArticleNote,
		Sender:      zammad.SenderAgent,
		Internal:    true,
	}
}
