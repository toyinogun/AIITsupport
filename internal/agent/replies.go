package agent

import (
	"strconv"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// Fixed texts the agent posts (spec 0008, Fixed texts). The eval asserts on
// them, so change them only with the spec.
const (
	TextChannelRedirect     = "For account help, please send me a direct message. I don't handle account requests in channels."
	TextEmptyMessage        = "Please send your question as a text message."
	TextAck                 = "Looking into it now."
	TextClosedThread        = "This conversation is closed. Send me a new message (not in this thread) and I'll start fresh."
	TextTooMany             = "You've started a lot of conversations with me in the last hour, so I've paused here. Please contact IT directly if it's urgent."
	TextBusy                = "I'm still working through your earlier messages in this thread. Please wait for my reply, then send this again."
	TextShutdown            = "I'm restarting and didn't get to this message. Please send it again in a minute."
	TextSlackTryAgain       = "I couldn't check your Slack profile just now. Please try again in a minute."
	TextNotStaff            = "I can only help Noord staff with a confirmed work email in this workspace. Please contact IT directly."
	TextNotMatched          = "I couldn't match your Slack account to a single Noord account, so I can't make changes for you. Please contact IT directly."
	TextAccountChanged      = "Your Slack account now matches a different Noord account than earlier in this conversation, so I've stopped here. Please contact IT directly."
	TextEscalatedWithTicket = "I couldn't finish this one, so I've passed it to IT in a helpdesk ticket. They'll follow up."
	TextEscalatedNoTicket   = "I couldn't finish this one, and I couldn't open a ticket for it either. Please contact IT directly."
	TextResolvedNoSummary   = "Your request is done and its ticket is closed, but I couldn't write a summary here. Send me a new message if you still need help."
	TextRestart             = "Sorry, I restarted while working on this. I've passed it to IT; send me a new message (not in this thread) if you still need help."
	FooterOpen              = "_Reply in this thread to continue._"
	FooterResolved          = "_If you still need help, send me a new message (not in this thread)._"
)

// Password reset texts (spec 0011, Fixed texts).
const (
	TextResetLinkFailed       = "I made you a password reset link but couldn't post it here. Please send me a new message (not in this thread) to get a fresh one."
	TextResolvedLinkNoSummary = "I've sent your password reset link above, but I couldn't finish my reply. The ticket closes when you set your new password."
)

// ResetLinkText is the reset_link post: the link, which works once.
func ResetLinkText(minutes int64, url string) string {
	return "Here is your password reset link. It works once and expires in " + strconv.FormatInt(minutes, 10) + " minutes:\n" + url
}

// Escalation texts (spec 0009, Fixed texts).
const (
	FooterEscalated    = "_IT has this now and will follow up. For anything else, send me a new message (not in this thread)._"
	BackstopTitle      = "Privileged request refused by the Noord assistant"
	BackstopSummary    = "The user asked for a privileged change, which the assistant refused. The assistant did not escalate it itself, so this was escalated automatically. See the ticket and the audit log for this thread."
	TextBackstopFailed = "I couldn't notify IT about this, so please contact them directly."
)

// The IT channel post (spec 0009, AC-9 and AC-12): one headline per
// escalation reason, then fixed labels.
var itHeadlines = map[string]string{
	escalatePrivileged: ":rotating_light: *Privileged request refused*",
	escalateOther:      ":busts_in_silhouette: *Request to act for someone else*",
	escalateSecurity:   ":warning: *Security concern*",
	escalateFailure:    ":construction: *Assistant could not finish*",
	escalateRestart:    ":arrows_counterclockwise: *Interrupted by a restart*",
	// spec 0012
	escalateManual:            ":wrench: *Needs a manual fix*",
	escalateApprovalDenied:    ":no_entry_sign: *MFA reset denied*",
	escalateApprovalExpired:   ":hourglass: *MFA reset not approved in time*",
	escalateApprovalFailed:    ":construction: *MFA reset did not finish*",
	escalateApprovalCancelled: ":leftwards_arrow_with_hook: *MFA reset cancelled by the requester*",
	codeNotMatched:            ":grey_question: *Slack user not matched to a Noord account*",
}

const (
	itRequesterLabel   = "Requester: "
	itAttemptsLabel    = "Blocked privileged requests from this person in the last "
	itRanLabel         = "Ran: "
	itTicketLabel      = "Ticket: "
	itSummaryLabel     = "Summary: "
	itReasonLabel      = "Reason: "
	itAlreadyEscalated = "Ticket: already escalated; see the audit log for this thread."
	itNoTicket         = "No ticket: no single Noord account matches this Slack user."
	// maxITSummaryRunes bounds the model's summary in the IT post.
	maxITSummaryRunes = 1500
)

// The escalation ticket (AC-10, AC-11, AC-14).
const (
	EscalationTitle    = "Noord assistant could not finish this request"
	EscalationIntro    = "The Noord assistant stopped before finishing. The user's last message:"
	escalationUnknown  = "The outcome of the last tool call is unknown."
	escalationAuditRef = "Check the audit log for this thread to see what already ran."
	// maxEscalationRunes keeps the body inside ticket_create's 10000 rune bound.
	maxEscalationRunes = 9000
)

// UnknownToolResult is the stored result of a tool call the tool server
// never answered (AC-11).
const UnknownToolResult = "unknown: the tool server did not answer; the action may or may not have run"

// oneToolPerStep answers every tool_use block after the first in one message (AC-5).
const oneToolPerStep = "Not run: only one tool call runs per step. Call it again on its own if it is still needed."

// Reason is a fixed token for why a turn stopped (AC-10, AC-17).
type Reason string

// Failure path reasons, then the other ways a conversation is escalated.
const (
	ReasonTurnLimit             Reason = "turn_limit"
	ReasonMessageTimeout        Reason = "message_timeout"
	ReasonTokenBudget           Reason = "token_budget"
	ReasonModelUnavailable      Reason = "model_unavailable"
	ReasonModelUnusable         Reason = "model_unusable"
	ReasonToolServerUnavailable Reason = "toolserver_unavailable"
	ReasonDirectoryUnavailable  Reason = "directory_unavailable"
	ReasonStoreUnavailable      Reason = "store_unavailable"
	ReasonHistoryInvalid        Reason = "history_invalid"
	ReasonRestart               Reason = "restart"

	ReasonSlackProfile   Reason = "slack_profile"
	ReasonNotMatched     Reason = "not_matched"
	ReasonAmbiguous      Reason = "ambiguous"
	ReasonAccountChanged Reason = "account_changed"
	ReasonTooMany        Reason = "too_many"

	// ReasonBackstop is code escalating a blocked request the model left
	// unescalated (spec 0009, AC-10). A model escalation logs its own
	// reason (privileged_request, other_person, security_concern).
	ReasonBackstop Reason = "backstop"
)

// noteOpenings start the escalation note for each reason. directory_unavailable
// has none: a ticket needs a matched identity.
var noteOpenings = map[Reason]string{
	ReasonTurnLimit:             "Stopped after the turn limit for one message.",
	ReasonMessageTimeout:        "Stopped after the time limit for one message.",
	ReasonTokenBudget:           "Stopped: this conversation used its token budget.",
	ReasonModelUnavailable:      "Stopped: the model API could not be reached.",
	ReasonModelUnusable:         "Stopped: the model gave no usable answer.",
	ReasonToolServerUnavailable: "Stopped: the tool server could not be reached.",
	ReasonStoreUnavailable:      "Stopped: the assistant's database could not be reached.",
	ReasonHistoryInvalid:        "Stopped: the stored conversation could not be sent to the model.",
	ReasonRestart:               "Interrupted by an agent restart.",
}

// Outcome tokens for agent_turn_done and the agent.turn span.
const (
	OutcomeResolved     = "resolved"
	OutcomeOpen         = "open"
	OutcomeEscalated    = "escalated"
	OutcomeRedirected   = "redirected"
	OutcomeClosedThread = "closed_thread"
	OutcomeSlackRetry   = "slack_retry"
	OutcomeEmpty        = "empty"
)

// MFA re-registration texts (spec 0012, Fixed texts). The user's texts never
// name the approver.
const (
	TextMFACleared = "IT approved your request and your MFA sign in methods are cleared. Next time you sign in to Microsoft 365 you'll be asked to set up the Authenticator app again; the steps are in the KB article \"Set up the Authenticator app\". Your ticket is closed."
	// TextMFAClearedTicketOpen is mfa_cleared when the execution could not
	// close the ticket (ticket_closed false), so the user and the card agree.
	TextMFAClearedTicketOpen = "IT approved your request and your MFA sign in methods are cleared. Next time you sign in to Microsoft 365 you'll be asked to set up the Authenticator app again; the steps are in the KB article \"Set up the Authenticator app\". Your ticket is still open, and IT will close it."
	TextMFADenied            = "IT didn't approve clearing your MFA methods, so nothing changed. Your ticket is with IT and someone will follow up."
	TextMFAExpired           = "IT couldn't review your request in time, so nothing changed. Your ticket is with IT and someone will follow up."
	TextMFAFailed            = "Something went wrong while clearing your MFA methods. IT has your ticket and will check your account and follow up."
	TextMFACancelled         = "You cancelled your MFA reset request, so nothing changed. Your ticket is still with IT if you need more help."
	TextMFAEndedContactIT    = "Your MFA reset request has ended, but I couldn't hand it to IT. Please contact IT directly."

	TextApprovalProcessing  = "Your decision is being processed. The card will update when it's done."
	TextApprovalUnknown     = "I can't find this request anymore."
	TextApprovalNotAllowed  = "Only IT approvers can decide this request."
	TextApprovalSelf        = "You can't decide your own request."
	TextApprovalExpired     = "This request has expired."
	TextApprovalDecided     = "This request was already decided."
	TextApprovalUnavailable = "I couldn't record your decision. Please try again in a minute."
)

// settleTexts are the user's one fixed text per ended approval (AC-17).
var settleTexts = map[string]string{
	mcpclient.StatusExecuted: TextMFACleared,
	mcpclient.StatusDenied:   TextMFADenied,
	mcpclient.StatusExpired:  TextMFAExpired,
	mcpclient.StatusFailed:   TextMFAFailed,
}

// The MFA escalation (spec 0012, AC-15): the title escalate uses when it has
// to open a ticket, and the fixed summaries.
const (
	MFAEscalationTitle     = "MFA reset needs IT"
	MFADeniedSummary       = "IT denied the MFA reset request."
	MFAExpiredSummary      = "Nobody decided the MFA reset request before it expired."
	MFAFailedSummaryPrefix = "The approved MFA reset did not finish: "
	MFACancelledSummary    = "The requester cancelled the MFA reset request."
)

// The approval card's fixed lines (spec 0012, AC-7).
const (
	cardHeader          = "MFA reset request"
	cardRequester       = "Requester: "
	cardTicket          = "Ticket: "
	cardTicketLinkLabel = "open in helpdesk"
	cardMethods         = "Methods: "
	cardMethodsInNote   = "Methods: listed in the ticket note."
	cardExpires         = "Expires: "
	cardConfirm         = "Confirm with the person by phone or in person before approving."
	cardFallback        = "MFA reset request for "
)
