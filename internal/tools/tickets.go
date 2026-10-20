package tools

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// assistantTag marks every ticket the assistant opens.
const assistantTag = "noord-assistant"

// notePrefix starts every internal note the assistant adds.
const notePrefix = "Added by the Noord assistant: "

// ticketCreate opens this thread's one ticket for the requester (AC-10).
func ticketCreate(ctx context.Context, d Deps, in Input) (Result, error) {
	if in.Requester.TicketID != 0 {
		return refused(refuseHasTicket), nil
	}
	has, err := d.History.ThreadHasOK(ctx, ToolTicketCreate, in.Requester.ChannelID, in.Requester.ThreadTS)
	if err != nil {
		return Result{}, failed(err)
	}
	if has {
		return refused(refuseHasTicket), nil
	}
	customer, ok, err := d.Helpdesk.customer(ctx, in.Account.Username)
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	if !ok {
		return refused(refuseNoHelpdeskAccount), nil
	}
	title := in.Args["title"]
	ticket, err := d.Helpdesk.api.CreateTicket(ctx, zammad.NewTicket{
		Title:      title,
		Group:      d.Helpdesk.group,
		CustomerID: customer.ID,
		Tags:       []string{assistantTag},
		Article: zammad.NewArticle{
			Subject:     title,
			Body:        in.Args["body"],
			ContentType: "text/plain",
			Type:        zammad.ArticleNote,
			Sender:      zammad.SenderCustomer,
			Internal:    false,
		},
	}, "")
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	return Result{
		Result: resultData(map[string]any{"ticket_id": ticket.ID, "ticket_number": ticket.Number}),
		Text:   "Ticket created.",
	}, nil
}

// ourStates maps the tool's state values to Zammad's state names.
var ourStates = map[string]string{
	stateOpen:    zammadOpen,
	statePending: zammadPendingReminder,
	stateClosed:  zammadClosed,
}

// ticketUpdate changes only the ticket in _meta, only when the requester is
// its customer and it is not closed (AC-11). State and note are one request.
// A close waits until a blocked privileged request is escalated (spec 0009,
// AC-6), and never runs while the thread has an open MFA approval: the
// execution closes the ticket itself (spec 0012, AC-24).
func ticketUpdate(ctx context.Context, d Deps, in Input) (Result, error) {
	if in.Requester.TicketID == 0 {
		return refused(refuseNoTicket), nil
	}
	if in.Args["state"] == stateClosed {
		owed, err := escalationOwed(ctx, d.History, in.Requester)
		if err != nil {
			return Result{}, failed(err)
		}
		if owed {
			return refused(refuseEscalateFirst), nil
		}
		open, err := d.History.ThreadHasOpenApproval(ctx, ToolMFAClear, in.Requester.ChannelID, in.Requester.ThreadTS)
		if err != nil {
			return Result{}, failed(err)
		}
		if open {
			return refused(refuseApprovalPending), nil
		}
	}
	customer, ok, err := d.Helpdesk.customer(ctx, in.Account.Username)
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	if !ok {
		return refused(refuseNoHelpdeskAccount), nil
	}
	id := int(in.Requester.TicketID)
	ticket, err := d.Helpdesk.api.GetTicket(ctx, id)
	if zammad.Status(err) == http.StatusNotFound {
		return refused(refuseNoTicket), nil
	}
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	if ticket.CustomerID != customer.ID {
		return refused(refuseNotYours), nil
	}
	byName, byID, err := d.Helpdesk.states(ctx)
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	if name := byID[ticket.StateID]; name == zammadClosed || name == zammadMerged {
		return refused(refuseTicketClosed), nil
	}

	update, err := buildUpdate(in.Args, byName)
	if err != nil {
		return Result{}, err
	}
	updated, err := d.Helpdesk.api.UpdateTicket(ctx, id, update, "")
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	return Result{
		Result: resultData(map[string]any{"ticket_id": updated.ID, "state": stateFor(byID[updated.StateID])}),
		Text:   "Ticket updated.",
	}, nil
}

// escalationOwed reports whether the thread holds a blocked privileged
// request and no ok escalation (the close guard, spec 0009 AC-6).
func escalationOwed(ctx context.Context, h History, r Requester) (bool, error) {
	blocked, err := h.ThreadHas(ctx, ToolPrivilegedAccessChange, audit.OutcomeDeniedPolicy, r.ChannelID, r.ThreadTS)
	if err != nil || !blocked {
		return false, err
	}
	escalated, err := h.ThreadHas(ctx, ToolEscalate, audit.OutcomeOK, r.ChannelID, r.ThreadTS)
	return !escalated, err
}

func buildUpdate(a Args, byName map[string]int) (zammad.TicketUpdate, error) {
	var u zammad.TicketUpdate
	if state, ok := a["state"]; ok {
		name := ourStates[state]
		if _, known := byName[name]; !known {
			return u, failed(nil)
		}
		u.State = name
	}
	if raw, ok := a["pending_until"]; ok {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil { // Check already parsed it; this cannot happen
			return u, failed(err)
		}
		t = t.UTC()
		u.PendingTime = &t
	}
	if note, ok := a["note"]; ok {
		u.Article = &zammad.NewArticle{
			Body:        notePrefix + note,
			ContentType: "text/plain",
			Type:        zammad.ArticleNote,
			Sender:      zammad.SenderAgent,
			Internal:    true,
		}
	}
	return u, nil
}

// stateFor reports a Zammad state name in the tool's words where it has one.
func stateFor(zammadName string) string {
	for ours, theirs := range ourStates {
		if theirs == zammadName {
			return ours
		}
	}
	return zammadName
}

// checkTicketUpdate: at least one of state or note; pending_until present
// exactly when state is pending, RFC 3339, in the future, and at most
// TICKET_MAX_PENDING ahead.
func checkTicketUpdate(a Args, env CheckEnv) (string, bool) {
	state, hasState := a["state"]
	_, hasNote := a["note"]
	if !hasState && !hasNote {
		return "state", false
	}
	raw, hasPending := a["pending_until"]
	if hasPending != (state == statePending) {
		return "pending_until", false
	}
	if !hasPending {
		return "", true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil || !t.After(env.Now) || t.After(env.Now.Add(env.MaxPending)) {
		return "pending_until", false
	}
	return "", true
}

// ticketTarget is the audit target for a ticket tool: the _meta ticket id.
func ticketTarget(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
