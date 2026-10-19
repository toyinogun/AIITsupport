package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// failurePath ends a turn that hit a limit or an outage (AC-10). After the
// conversation was resolved in this turn it only says so (AC-6). Otherwise
// it tries to leave IT a ticket, sets the conversation escalated, and tells
// the user which of the two happened.
func (t *turn) failurePath(f *failure) string {
	if t.resolved {
		t.post(TextResolvedNoSummary, "resolved_no_summary")
		return OutcomeResolved
	}
	t.reason, t.cause = f.reason, f.cause
	var tk ticketOutcome
	if t.requester != nil {
		tk = t.a.ensureTicket(t.ctx, *t.requester, t.in.Text, escalationNote(f.reason, f.unknown))
		t.toolCalls += tk.calls
	}
	t.setStatus(StatusChange{Status: StatusEscalated, TicketID: tk.created})
	t.a.logEscalated(t.ctx, t.in.EventID, f.reason, tk.exists, f.cause)
	if tk.exists {
		t.post(TextEscalatedWithTicket, "escalated_with_ticket")
	} else {
		t.post(TextEscalatedNoTicket, "escalated_no_ticket")
	}
	return OutcomeEscalated
}

// ensureTicket leaves IT a ticket for a conversation that stopped (AC-10),
// on a context detached from the turn and bounded by one AGENT_TOOL_TIMEOUT
// for both calls. No call is retried.
func (a *Agent) ensureTicket(parent context.Context, r mcpclient.Requester, lastMessage, note string) ticketOutcome {
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	defer cancel()
	if r.TicketID != 0 {
		res, err := a.deps.Tools.Call(ctx, r, toolTicketUpdate, map[string]any{"note": note})
		closed := err == nil && res.IsError && res.Code == codeTicketClosed
		return ticketOutcome{exists: !closed, calls: 1}
	}
	res, err := a.deps.Tools.Call(ctx, r, toolTicketCreate, map[string]any{
		"title": EscalationTitle,
		"body":  escalationBody(lastMessage),
	})
	if err != nil {
		return ticketOutcome{calls: 1}
	}
	if res.IsError {
		// has_ticket: a ticket exists but its id is unknown, so no note.
		return ticketOutcome{exists: res.Code == codeHasTicket, calls: 1}
	}
	id, ok := ticketID(res.Data["ticket_id"])
	if !ok {
		return ticketOutcome{exists: true, calls: 1}
	}
	r.TicketID = id
	_, _ = a.deps.Tools.Call(ctx, r, toolTicketUpdate, map[string]any{"note": note}) // the ticket exists even if the note fails
	return ticketOutcome{exists: true, created: id, calls: 2}
}

// ticketOutcome is what ensureTicket did: whether a ticket exists afterwards,
// the id of one it created (0 if none), and how many tool calls it made.
type ticketOutcome struct {
	exists  bool
	created int64
	calls   int
}

// escalationBody is the fixed intro, a blank line, and the user's latest
// message cut to 9000 runes.
func escalationBody(lastMessage string) string {
	return EscalationIntro + "\n\n" + cutRunes(strings.TrimSpace(lastMessage), maxEscalationRunes)
}

// escalationNote is the reason's opening, the unknown outcome sentence when
// a tool call got no answer, and the pointer to the audit log.
func escalationNote(r Reason, unknown bool) string {
	parts := []string{noteOpenings[r]}
	if unknown {
		parts = append(parts, escalationUnknown)
	}
	parts = append(parts, escalationAuditRef)
	return strings.Join(parts, " ")
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
