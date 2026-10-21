package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// storedCall is one tool call the model made, as stored in the
// conversation: its name and how its result started.
type storedCall struct {
	name    string
	code    string // "ok", the refusal code, or "unknown"
	skipped bool   // answered with oneToolPerStep: it never ran
}

// refusalCode is the "<code>:" a refusal's result text starts with.
var refusalCode = regexp.MustCompile(`^([a-z_]+): `)

// storedCalls lists the model's tool calls in a stored history, in order,
// each with its result's code (spec 0009, the Ran: list and the backstop).
func storedCalls(msgs []StoredMessage) []storedCall {
	var calls []storedCall
	byID := map[string]int{}
	for _, m := range msgs {
		var blocks []anthropic.ContentBlockParamUnion
		if m.Content == nil || json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch {
			case b.OfToolUse != nil:
				byID[b.OfToolUse.ID] = len(calls)
				calls = append(calls, storedCall{name: b.OfToolUse.Name, code: "unknown"})
			case b.OfToolResult != nil:
				i, ok := byID[b.OfToolResult.ToolUseID]
				if !ok {
					continue
				}
				calls[i].code, calls[i].skipped = resultCode(b.OfToolResult)
			}
		}
	}
	return calls
}

func resultCode(r *anthropic.ToolResultBlockParam) (code string, skipped bool) {
	var text string
	for _, c := range r.Content {
		if c.OfText != nil {
			text = c.OfText.Text
			break
		}
	}
	if !r.IsError.Value {
		return "ok", false
	}
	if text == oneToolPerStep {
		return "", true
	}
	if m := refusalCode.FindStringSubmatch(text); m != nil {
		return m[1], false
	}
	return "unknown", false
}

// blockedUnescalated reports whether the calls hold a blocked
// privileged_access_change and no ok escalate (AC-10, AC-11).
func blockedUnescalated(calls []storedCall) (blocked, escalated bool) {
	for _, c := range calls {
		switch {
		case c.name == toolPrivilegedAccess && c.code == codeBlocked:
			blocked = true
		case c.name == toolEscalate && c.code == "ok":
			escalated = true
		}
	}
	return blocked, escalated
}

// ranLine is the IT post's Ran: list; code calls are never stored, so a
// code escalation is appended (AC-9).
func ranLine(calls []storedCall, codeEscalated bool) string {
	var parts []string
	for _, c := range calls {
		if !c.skipped {
			parts = append(parts, c.name+" "+c.code)
		}
	}
	if codeEscalated {
		parts = append(parts, toolEscalate+" ok")
	}
	return strings.Join(parts, ", ")
}

// itPost is everything one IT channel message about an ok escalation shows.
type itPost struct {
	slackID string
	account string
	ran     string
	result  map[string]any // the ok escalate result
	summary string         // already safe: modelSummary or a fixed text
	// headline replaces the reason's headline when set (an access request's
	// approval endings, spec 0013).
	headline string
}

// escalateReasons are the reasons escalate accepts (spec 0009, AC-2).
var escalateReasons = []string{
	escalatePrivileged, escalateOther, escalateSecurity, escalateFailure, escalateRestart,
	escalateManual, escalateApprovalDenied, escalateApprovalExpired, escalateApprovalFailed, escalateApprovalCancelled,
	escalateNoGroupOwner,
}

// resultReason is an ok escalate result's reason, or security_concern when
// the result holds none escalate accepts, so the headline and the
// agent_escalated reason are always a fixed token; ok is false on the
// fallback. The tool server echoes a validated reason, so a fallback means
// the result changed shape.
func resultReason(result map[string]any) (reason string, ok bool) {
	reason, _ = result["reason"].(string)
	if slices.Contains(escalateReasons, reason) {
		return reason, true
	}
	return escalateSecurity, false
}

// text builds the post in its fixed order (AC-9). Only the summary can hold
// model text, and it comes last, on one line, escaped.
func (p itPost) text() string {
	reason, _ := resultReason(p.result)
	privileged, _ := p.result["privileged"].(bool)
	if privileged {
		reason = escalatePrivileged
	}
	headline := itHeadlines[reason]
	if p.headline != "" && !privileged {
		headline = p.headline
	}
	lines := []string{headline, requesterLine(p.slackID, p.account)}
	attempts, _ := wholeNumber(p.result["privileged_attempts"])
	if privileged && attempts >= 2 {
		hours, _ := wholeNumber(p.result["privileged_window_hours"])
		lines = append(lines, fmt.Sprintf("%s%dh: %d", itAttemptsLabel, hours, attempts))
	}
	lines = append(lines, itRanLabel+p.ran, itTicketLabel+ticketLink(p.result), itSummaryLabel+p.summary)
	return strings.Join(lines, "\n")
}

// itShort is the post for a code call refused already_escalated (AC-9);
// headline replaces the reason's own when set.
func itShort(reason, headline, slackID, account string) string {
	if headline == "" {
		headline = itHeadlines[reason]
	}
	return strings.Join([]string{headline, requesterLine(slackID, account), itAlreadyEscalated}, "\n")
}

// itNotMatched is the post for a person code refused as not_matched or
// ambiguous (AC-12).
func itNotMatched(slackID, code string) string {
	return strings.Join([]string{
		itHeadlines[codeNotMatched], requesterLine(slackID, ""), itReasonLabel + code, itNoTicket,
	}, "\n")
}

func requesterLine(slackID, account string) string {
	line := itRequesterLabel + "<@" + slackID + ">"
	if account != "" {
		line += " (" + account + ")"
	}
	return line
}

// ticketLink is a Slack link to the ticket labelled with its number, or the
// number alone without ZAMMAD_PUBLIC_URL.
func ticketLink(result map[string]any) string {
	number, _ := result["ticket_number"].(string)
	label := "#" + slackEscape(number)
	if url, _ := result["ticket_url"].(string); url != "" {
		return "<" + slackEscape(url) + "|" + label + ">"
	}
	return label
}

// modelSummary is the model's summary made safe for the post: every
// whitespace run becomes one space, the requester's display name and
// department are replaced by the name rule, it is cut to 1500 runes, then
// &, <, and > are escaped, so it can neither name the person, fake a line,
// nor fire a mention or link (spec 0009, AC-9).
func modelSummary(s, displayName, department string) string {
	return slackEscape(cutRunes(redactRequester(collapse(s), displayName, department), maxITSummaryRunes))
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func slackEscape(s string) string { return slackEscaper.Replace(s) }

// wholeNumber reads an integer from decoded JSON (a float64 from the MCP
// client, an int or int64 from a fake).
func wholeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), n == float64(int64(n))
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// postIT sends one message to SLACK_IT_CHANNEL on a detached context with
// the reply timeout. A failure is logged with its cause, never retried.
func (a *Agent) postIT(parent context.Context, eventID, kind, text string) {
	a.postTo(parent, a.cfg.ITChannel, "", eventID, kind, text)
}

// codeEscalation is one escalate call agent code makes by itself: the
// backstop, the failure path, or the sweep (AC-10, AC-11).
type codeEscalation struct {
	reason    string
	title     string
	summary   string // the call's argument; may hold the user's message
	itSummary string // what the IT post shows instead: a fixed text
	headline  string // the IT post's headline when not the reason's own (spec 0013)
}

// escalationOutcome is what a code escalation got back.
type escalationOutcome struct {
	called  bool
	ok      bool
	already bool // refused already_escalated: a ticket exists, IT already knows
	result  map[string]any
	code    string // the refusal code, or "unknown" with no answer
	cause   string
}

// exists reports whether IT has a ticket afterwards (AC-11).
func (o escalationOutcome) exists() bool { return o.ok || o.already }

// ticketID is the ok result's ticket id, 0 otherwise.
func (o escalationOutcome) ticketID() int64 {
	if !o.ok {
		return 0
	}
	id, _ := ticketID(o.result["ticket_id"])
	return id
}

// escalateByCode calls escalate with the code initiator, on a context
// detached from the turn and bounded by one AGENT_TOOL_TIMEOUT. It is never
// stored as a message; the tool server's audit pair records it.
func (a *Agent) escalateByCode(parent context.Context, r mcpclient.Requester, e codeEscalation) escalationOutcome {
	r.Initiator = mcpclient.InitiatorCode
	res, err := a.callTool(parent, r, toolEscalate, map[string]any{
		"reason": e.reason, "title": e.title, "summary": e.summary,
	})
	out := escalationOutcome{called: true, result: res.Data, cause: cause(err)}
	switch {
	case err != nil:
		out.code = "unknown"
	case !res.IsError:
		out.ok = true
	default:
		out.code = res.Code
		out.already = res.Code == codeAlreadyEscalated
	}
	return out
}

// announce posts to IT after a code escalation: the full post for an ok
// result, the short one for already_escalated, nothing otherwise (AC-9).
func (a *Agent) announce(parent context.Context, eventID string, o escalationOutcome, e codeEscalation, slackID, account string, calls []storedCall) {
	switch {
	case o.ok:
		post := itPost{
			slackID: slackID, account: account, ran: ranLine(calls, true), result: o.result, summary: e.itSummary, headline: e.headline,
		}
		a.postIT(parent, eventID, kindITEscalation, post.text())
	case o.already:
		a.postIT(parent, eventID, kindITEscalation, itShort(e.reason, e.headline, slackID, account))
	}
}

// Post kinds for agent_post_failed on the IT channel.
const (
	kindITEscalation = "it_escalation"
	kindITNotMatched = "it_not_matched"
)

// callsOrNone reads the conversation's stored tool calls for an escalation
// that goes ahead either way. A failed read logs agent_history_read_failed
// and is an empty list: the Ran: line is empty and a blocked call is missed,
// though the tool server still tags the ticket from its audit log.
func (a *Agent) callsOrNone(parent context.Context, id int64) []storedCall {
	calls, err := a.listCalls(parent, id)
	if err != nil {
		a.deps.Logger.Warn("agent_history_read_failed", "conversation_id", id, "cause", cause(err))
	}
	return calls
}

// listCalls reads the conversation's stored tool calls on a detached
// context; a failed read is an empty list.
func (a *Agent) listCalls(parent context.Context, id int64) ([]storedCall, error) {
	ctx, cancel := a.detached(parent, a.cfg.ToolTimeout)
	defer cancel()
	msgs, err := a.deps.Store.ListMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	return storedCalls(msgs), nil
}
