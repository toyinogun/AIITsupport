package tools

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// Escalation reasons (spec 0009, AC-2; spec 0012 adds manual_action for the
// model and four approval reasons for code). The model may use the first
// three and manual_action; the rest are code only.
const (
	ReasonPrivilegedRequest = "privileged_request"
	ReasonOtherPerson       = "other_person"
	ReasonSecurityConcern   = "security_concern"
	ReasonAssistantFailure  = "assistant_failure"
	ReasonRestart           = "restart"
	ReasonManualAction      = "manual_action"
	ReasonApprovalDenied    = "approval_denied"
	ReasonApprovalExpired   = "approval_expired"
	ReasonApprovalFailed    = "approval_failed"
	// ReasonApprovalCancelled is a requester who denied their own request
	// (spec 0012 AC-15, amended 2026-10-20).
	ReasonApprovalCancelled = "approval_cancelled"
	// ReasonNoGroupOwner is an access request no owner can decide (spec 0013,
	// AC-19), for the model and for code. It tags the ticket escalated only.
	ReasonNoGroupOwner = "no_group_owner"
)

var (
	escalationReasons = []string{
		ReasonPrivilegedRequest, ReasonOtherPerson, ReasonSecurityConcern, ReasonAssistantFailure, ReasonRestart,
		ReasonManualAction, ReasonApprovalDenied, ReasonApprovalExpired, ReasonApprovalFailed, ReasonApprovalCancelled,
		ReasonNoGroupOwner,
	}
	codeOnlyReasons = []string{ReasonAssistantFailure, ReasonRestart, ReasonApprovalDenied, ReasonApprovalExpired, ReasonApprovalFailed, ReasonApprovalCancelled}
)

// Tags an escalation adds (spec 0009, AC-4).
const (
	tagEscalated  = "escalated"
	tagPrivileged = "privileged-request"
)

// escalationNotePrefix opens the internal note; the reason and a newline follow.
const escalationNotePrefix = "Escalated by the Noord assistant ("

// Initiators as the result reports them.
const (
	initiatorModel = "model"
	initiatorCode  = InitiatorCode
)

// checkEscalate: a code only reason needs the _meta initiator code (AC-2).
func checkEscalate(a Args, env CheckEnv) (string, bool) {
	if slices.Contains(codeOnlyReasons, a["reason"]) && env.Initiator != InitiatorCode {
		return "reason", false
	}
	return "", true
}

// escalation is what escalate learned from the audit log before any Zammad
// call (AC-4: every audit read comes first).
type escalation struct {
	privileged bool
	attempts   int64
	ticketID   int64 // the _meta ticket, else the thread's ticket_create, else 0
}

// escalate tags and notes this thread's ticket, or opens one, so IT picks the
// conversation up (spec 0009, the escalate handler order).
func escalate(ctx context.Context, d Deps, in Input) (Result, error) {
	esc, refusal, err := readEscalation(ctx, d, in)
	if err != nil {
		return Result{}, failed(err)
	}
	if refusal != nil {
		return refused(*refusal), nil
	}
	customer, ok, err := d.Helpdesk.customer(ctx, in.Account.Username)
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	if !ok {
		return refused(refuseNoHelpdeskAccount), nil
	}
	ticket, created, r, err := escalationTarget(ctx, d, in, esc, customer.ID)
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	if r != nil {
		return refused(*r), nil
	}
	return Result{Result: resultData(escalationData(d, in, esc, ticket, created)), Text: "Escalated to IT."}, nil
}

// escalationTarget tags and notes the thread's ticket when it is usable,
// else opens one with the tags (AC-4); created reports which. A ticket that
// is gone, not the requester's, or closed for a model escalation refuses.
func escalationTarget(ctx context.Context, d Deps, in Input, esc escalation, customerID int) (zammad.Ticket, bool, *refusal, error) {
	reason, code := in.Args["reason"], in.Requester.Initiator == InitiatorCode
	tags := []string{tagEscalated}
	if esc.privileged || reason == ReasonPrivilegedRequest {
		tags = append(tags, tagPrivileged)
	}
	if esc.ticketID != 0 {
		found, usable, r, err := d.Helpdesk.escalationTicket(ctx, int(esc.ticketID), customerID, esc.privileged || code)
		if err != nil || r != nil {
			return zammad.Ticket{}, false, r, err
		}
		if usable {
			return found, false, nil, d.Helpdesk.tagAndNote(ctx, found.ID, tags, reason, in.Args["summary"])
		}
	}
	ticket, err := d.Helpdesk.createEscalation(ctx, customerID, tags, in.Args["title"], in.Args["summary"])
	return ticket, true, nil, err
}

// escalationData is the ok result's data (AC-4, AC-5).
func escalationData(d Deps, in Input, esc escalation, ticket zammad.Ticket, created bool) map[string]any {
	initiator := initiatorModel
	if in.Requester.Initiator == InitiatorCode {
		initiator = initiatorCode
	}
	data := map[string]any{
		"ticket_id": ticket.ID, "ticket_number": ticket.Number,
		"reason": in.Args["reason"], "initiator": initiator,
		"privileged": esc.privileged, "created": created,
		"privileged_attempts":     esc.attempts,
		"privileged_window_hours": int64(d.Limits.PrivilegedWindow.Hours()),
	}
	if url := d.Helpdesk.ticketURL(ticket.ID); url != "" {
		data["ticket_url"] = url
	}
	return data
}

// readEscalation makes every audit read escalate needs (AC-3, AC-4, AC-5).
func readEscalation(ctx context.Context, d Deps, in Input) (escalation, *refusal, error) {
	ch, th := in.Requester.ChannelID, in.Requester.ThreadTS
	done, err := d.History.ThreadHas(ctx, ToolEscalate, audit.OutcomeOK, ch, th)
	if err != nil {
		return escalation{}, nil, err
	}
	if done {
		return escalation{}, &refuseAlreadyEscalated, nil
	}
	var esc escalation
	if esc.privileged, err = d.History.ThreadHas(ctx, ToolPrivilegedAccessChange, audit.OutcomeDeniedPolicy, ch, th); err != nil {
		return escalation{}, nil, err
	}
	since := in.Now.Add(-d.Limits.PrivilegedWindow)
	if esc.attempts, err = d.History.CountRecent(ctx, ToolPrivilegedAccessChange, in.Requester.SlackID, audit.OutcomeDeniedPolicy, since); err != nil {
		return escalation{}, nil, err
	}
	// A protected group in an access request is a privileged attempt too (spec 0013, AC-5).
	protected, err := d.History.CountRecentReason(ctx, ToolGroupAccessRequest, in.Requester.SlackID, audit.OutcomeDeniedPolicy, reasonProtected, since)
	if err != nil {
		return escalation{}, nil, err
	}
	esc.attempts += protected
	esc.ticketID = in.Requester.TicketID
	if esc.ticketID == 0 {
		if esc.ticketID, err = d.History.ThreadTicketID(ctx, ch, th); err != nil {
			return escalation{}, nil, err
		}
	}
	return esc, nil, nil
}

// escalationTicket reads the thread's ticket and checks it belongs to the
// requester. A closed or merged one refuses, unless openNew is set (a
// privileged or code escalation), when usable is false and a new ticket is
// opened instead (AC-4).
func (h *Helpdesk) escalationTicket(ctx context.Context, id, customerID int, openNew bool) (zammad.Ticket, bool, *refusal, error) {
	ticket, err := h.api.GetTicket(ctx, id)
	if zammad.Status(err) == http.StatusNotFound {
		return zammad.Ticket{}, false, &refuseNoTicket, nil
	}
	if err != nil {
		return zammad.Ticket{}, false, nil, err
	}
	if ticket.CustomerID != customerID {
		return zammad.Ticket{}, false, &refuseNotYours, nil
	}
	_, byID, err := h.states(ctx)
	if err != nil {
		return zammad.Ticket{}, false, nil, err
	}
	if name := byID[ticket.StateID]; name == zammadClosed || name == zammadMerged {
		if openNew {
			return zammad.Ticket{}, false, nil, nil
		}
		return zammad.Ticket{}, false, &refuseTicketClosed, nil
	}
	return ticket, true, nil, nil
}

// tagAndNote adds each tag, then one update with the internal note, leaving
// the state alone. A tag added before a failure stays.
func (h *Helpdesk) tagAndNote(ctx context.Context, id int, tags []string, reason, summary string) error {
	for _, tag := range tags {
		if err := h.api.AddTag(ctx, id, tag); err != nil {
			return err
		}
	}
	_, err := h.api.UpdateTicket(ctx, id, zammad.TicketUpdate{Article: &zammad.NewArticle{
		Body:        escalationNotePrefix + reason + "):\n" + summary,
		ContentType: "text/plain",
		Type:        zammad.ArticleNote,
		Sender:      zammad.SenderAgent,
		Internal:    true,
	}}, "")
	return err
}

// createEscalation opens a ticket like ticket_create does, with the
// escalation tags (AC-4).
func (h *Helpdesk) createEscalation(ctx context.Context, customerID int, tags []string, title, summary string) (zammad.Ticket, error) {
	return h.api.CreateTicket(ctx, zammad.NewTicket{
		Title:      title,
		Group:      h.group,
		CustomerID: customerID,
		Tags:       append([]string{assistantTag}, tags...),
		Article: zammad.NewArticle{
			Subject:     title,
			Body:        summary,
			ContentType: "text/plain",
			Type:        zammad.ArticleNote,
			Sender:      zammad.SenderCustomer,
			Internal:    false,
		},
	}, "")
}

// ticketURL is the browser link to a ticket, empty without ZAMMAD_PUBLIC_URL.
func (h *Helpdesk) ticketURL(id int) string {
	if h.publicURL == "" {
		return ""
	}
	return h.publicURL + "/#ticket/zoom/" + strconv.Itoa(id)
}
