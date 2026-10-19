package agent

import (
	"strings"
	"unicode/utf8"
)

// failurePath ends a turn that hit a limit or an outage (AC-10). After the
// conversation was resolved in this turn it only says so (AC-6); after an ok
// escalate in this turn IT already has it (spec 0009, AC-11). Otherwise it
// escalates through the tool server's escalate, sets the conversation
// escalated, and tells the user whether IT got a ticket.
func (t *turn) failurePath(f *failure) string {
	if t.resolved {
		t.post(TextResolvedNoSummary, "resolved_no_summary")
		return OutcomeResolved
	}
	t.cause = f.cause
	if t.escalated {
		t.post(TextEscalatedWithTicket, "escalated_with_ticket")
		return OutcomeEscalated
	}
	t.reason = f.reason
	var o escalationOutcome
	if t.requester != nil {
		calls := t.a.callsOrNone(t.ctx, t.conv.ID) // a failed read escalates as assistant_failure
		reason := escalateFailure
		if blocked, _ := blockedUnescalated(calls); blocked {
			reason = escalatePrivileged
		}
		e := codeEscalation{
			reason:    reason,
			title:     EscalationTitle,
			summary:   escalationBody(t.in.Text) + "\n\n" + escalationNote(f.reason, f.unknown),
			itSummary: noteOpenings[f.reason],
		}
		o = t.a.escalateByCode(t.ctx, *t.requester, e)
		t.toolCalls++
		t.setStatus(StatusChange{Status: StatusEscalated, TicketID: t.newTicket(o.ticketID())})
		t.a.announce(t.ctx, t.in.EventID, o, e, t.requester.SlackID, t.accountName(), calls)
	} else {
		t.setStatus(StatusChange{Status: StatusEscalated})
	}
	t.a.logEscalated(t.ctx, t.in.EventID, f.reason, o.exists(), f.cause)
	if o.exists() {
		t.post(TextEscalatedWithTicket, "escalated_with_ticket")
	} else {
		t.post(TextEscalatedNoTicket, "escalated_no_ticket")
	}
	return OutcomeEscalated
}

// backstop escalates a blocked privileged request the model left
// unescalated, once the loop ended with text and the conversation is still
// open (spec 0009, AC-10). It is the one deliberate second call: it runs
// even when the model's own escalate was refused or failed. It returns the
// reply text, footer, and outcome.
func (t *turn) backstop(answer string) (text, footer, outcome string) {
	calls, err := t.a.listCalls(t.ctx, t.conv.ID)
	if err != nil {
		t.a.deps.Logger.Warn("agent_backstop_failed", "event_id", t.in.EventID, "code", "unknown", "cause", cause(err))
		return answer, FooterOpen, OutcomeOpen
	}
	if blocked, escalated := blockedUnescalated(calls); !blocked || escalated {
		return answer, FooterOpen, OutcomeOpen
	}
	e := codeEscalation{reason: escalatePrivileged, title: BackstopTitle, summary: BackstopSummary, itSummary: BackstopSummary}
	o := t.a.escalateByCode(t.ctx, *t.requester, e)
	t.toolCalls++
	if !o.exists() {
		t.a.deps.Logger.Warn("agent_backstop_failed", "event_id", t.in.EventID, "code", o.code, "cause", o.cause)
		return answer + "\n\n" + TextBackstopFailed, FooterOpen, OutcomeOpen
	}
	t.reason = ReasonBackstop
	t.setStatus(StatusChange{Status: StatusEscalated, TicketID: t.newTicket(o.ticketID())})
	t.a.logEscalated(t.ctx, t.in.EventID, ReasonBackstop, true, cause(nil))
	t.a.announce(t.ctx, t.in.EventID, o, e, t.requester.SlackID, t.accountName(), calls)
	return answer, FooterEscalated, OutcomeEscalated
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
