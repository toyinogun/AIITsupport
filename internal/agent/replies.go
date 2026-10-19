package agent

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
