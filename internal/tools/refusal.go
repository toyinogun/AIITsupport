package tools

import (
	"context"
	"errors"
	"net"
	"strconv"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/graph"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// Code is what the model sees in a refusal or failure (spec 0007, refusal table).
type Code string

// Codes, one per row of the refusal table.
const (
	CodeBlocked           Code = "blocked"
	CodeInvalidArguments  Code = "invalid_arguments"
	CodeNotMatched        Code = "not_matched"
	CodeAmbiguous         Code = "ambiguous"
	CodeNoHelpdeskAccount Code = "no_helpdesk_account"
	CodeReadOnly          Code = "read_only"
	CodeProtected         Code = "protected"
	CodeRateLimited       Code = "rate_limited"
	CodeHasTicket         Code = "has_ticket"
	CodeNoTicket          Code = "no_ticket"
	CodeNotYours          Code = "not_yours"
	CodeTicketClosed      Code = "ticket_closed"
	CodeUnavailable       Code = "unavailable"
	CodeNotConfirmed      Code = "not_confirmed"
	CodeFailed            Code = "failed"
	CodePendingApproval   Code = "pending_approval"
	CodeAlreadyEscalated  Code = "already_escalated"
	CodeEscalateFirst     Code = "escalate_first"
	CodeAccountDisabled   Code = "account_disabled"
	// Spec 0012.
	CodeAlreadyPending  Code = "already_pending"
	CodeNotInEntra      Code = "not_in_entra"
	CodeNothingToClear  Code = "nothing_to_clear"
	CodeOtherMethods    Code = "other_methods"
	CodeApprovalPending Code = "approval_pending"
	// Spec 0013.
	CodeAlreadyMember    Code = "already_member"
	CodeNoOwner          Code = "no_owner"
	CodeOwnerIsRequester Code = "owner_is_requester"
	CodeNotRequestable   Code = "not_requestable"
	CodeTooManyPending   Code = "too_many_pending"
	CodeOnePerThread     Code = "one_per_thread"
)

// Reasons stored in audit_log.error. Fixed text only, never request data.
const (
	reasonBlocked             = "tool is blocked"
	reasonInvalidArguments    = "invalid arguments"
	reasonNotMatched          = "requester not matched"
	reasonAmbiguous           = "requester ambiguous"
	reasonNoHelpdeskAccount   = "no helpdesk account"
	reasonReadOnly            = "account changes are paused"
	reasonProtected           = "group is protected"
	reasonRateLimited         = "rate limited"
	reasonHasTicket           = "thread already has a ticket"
	reasonNoTicket            = "no ticket for this thread"
	reasonNotYours            = "ticket belongs to someone else"
	reasonTicketClosed        = "ticket is closed"
	reasonDirectoryDown       = "directory unavailable"
	reasonHelpdeskDown        = "helpdesk unavailable"
	reasonUnlockNotConfirmed  = "unlock not confirmed"
	reasonActionFailed        = "action failed"
	reasonAlreadyEscalated    = "thread already escalated"
	reasonEscalateFirst       = "escalate first"
	reasonAccountDisabled     = "account disabled"
	accountDisabledSentence   = "This account is disabled, so no reset link can be sent."
	reasonAlreadyPending      = "approval already pending"
	reasonNotInEntra          = "not in entra"
	reasonNothingToClear      = "nothing to clear"
	reasonOtherMethods        = "other methods"
	reasonApprovalPending     = "approval pending"
	reasonAccountNotEligible  = "account not eligible"
	reasonMethodDeleteFailed  = "method delete failed"
	reasonEntraDown           = "entra unavailable"
	entraDownSentence         = "Entra cannot be reached right now."
	alreadyPendingSentence    = "An MFA reset for this account is already waiting for IT approval in another conversation."
	notInEntraSentence        = "This account has no cloud sign in that the assistant can change, so IT needs to handle it."
	nothingToClearSentence    = "This account has no phone or Authenticator app sign in method to clear."
	otherMethodsSentence      = "This account has other sign in methods the assistant can't clear, so IT needs to handle it."
	approvalPendingSentence   = "This ticket is waiting on an IT approval and closes by itself when IT decides."
	alreadyEscalatedSentence  = "This conversation is already with IT."
	escalateFirstSentence     = "A refused privileged request in this conversation has not been escalated yet; call escalate before closing the ticket."
	pendingApprovalSentence   = "This request is waiting for IT approval."
	invalidArgumentsSentence  = "The arguments did not match the tool's schema"
	directoryDownSentence     = "The directory cannot be reached right now."
	helpdeskDownSentence      = "The helpdesk cannot be reached right now."
	failedSentence            = "The action failed."
	unlockNotConfirmedPhrase  = "The unlock was sent but the account still shows as locked."
	ticketClosedSentence      = "That ticket is already closed; open a new conversation for a new problem."
	blockedSentence           = "Privileged changes are never made by the assistant. Escalate to IT."
	hasTicketSentence         = "This conversation already has a ticket; update it instead."
	noTicketSentence          = "This conversation has no ticket yet."
	notYoursSentence          = "That ticket belongs to another user."
	rateLimitedSentence       = "Too many recent requests of this kind for this user."
	protectedSentence         = "That group is outside what the assistant may change."
	readOnlySentence          = "Account changes are paused by IT right now."
	noHelpdeskAccountSentence = "This account has no helpdesk profile."
	ambiguousSentence         = "More than one Noord account matches this Slack user's email."
	notMatchedSentence        = "No single active Noord account matches this Slack user's email."

	// Spec 0013.
	reasonAlreadyMember         = "already a member"
	reasonNoOwner               = "no group owner"
	reasonOwnerIsRequester      = "owner is requester"
	reasonNotRequestable        = "group not requestable"
	reasonTooManyPending        = "too many pending"
	reasonOnePerThread          = "one request per conversation"
	reasonAccessPending         = "access request pending"
	reasonRecheckFailed         = "recheck failed"
	reasonAddNotConfirmed       = "add not confirmed"
	alreadyMemberSentence       = "The requester already has this access; tell them to sign out and back in, then close the ticket."
	noOwnerSentence             = "This group has no owner who can approve, so IT needs to handle it."
	ownerIsRequesterSentence    = "The requester owns this group, so IT needs to confirm the change."
	notRequestableSentence      = "This is not a group the assistant can request for this person."
	tooManyPendingSentence      = "This person already has the maximum number of access requests waiting; they can ask again once one is decided."
	onePerThreadSentence        = "This conversation already has an access request waiting. Ask for another group in a new conversation."
	groupAlreadyPendingSentence = "A request for this group is already waiting for its owner in another conversation."
	accessPendingSentence       = "This ticket is waiting on the group owner's decision; the assistant's code handles it when they decide."
	groupPendingSentence        = "This request is waiting for the group owner's approval."
)

// refusal is one refusal table row a call can end on without failing.
type refusal struct {
	code     Code
	outcome  audit.Outcome
	reason   string
	sentence string
}

var (
	refuseBlocked           = refusal{CodeBlocked, audit.OutcomeDeniedPolicy, reasonBlocked, blockedSentence}
	refuseNotMatched        = refusal{CodeNotMatched, audit.OutcomeDeniedIdentity, reasonNotMatched, notMatchedSentence}
	refuseAmbiguous         = refusal{CodeAmbiguous, audit.OutcomeDeniedIdentity, reasonAmbiguous, ambiguousSentence}
	refuseNoHelpdeskAccount = refusal{CodeNoHelpdeskAccount, audit.OutcomeDeniedIdentity, reasonNoHelpdeskAccount, noHelpdeskAccountSentence}
	refuseReadOnly          = refusal{CodeReadOnly, audit.OutcomeReadOnly, reasonReadOnly, readOnlySentence}
	refuseProtected         = refusal{CodeProtected, audit.OutcomeDeniedPolicy, reasonProtected, protectedSentence}
	refuseRateLimited       = refusal{CodeRateLimited, audit.OutcomeDeniedPolicy, reasonRateLimited, rateLimitedSentence}
	refuseHasTicket         = refusal{CodeHasTicket, audit.OutcomeDeniedPolicy, reasonHasTicket, hasTicketSentence}
	refuseNoTicket          = refusal{CodeNoTicket, audit.OutcomeDeniedPolicy, reasonNoTicket, noTicketSentence}
	refuseNotYours          = refusal{CodeNotYours, audit.OutcomeDeniedPolicy, reasonNotYours, notYoursSentence}
	refuseTicketClosed      = refusal{CodeTicketClosed, audit.OutcomeDeniedPolicy, reasonTicketClosed, ticketClosedSentence}
	refuseAlreadyEscalated  = refusal{CodeAlreadyEscalated, audit.OutcomeDeniedPolicy, reasonAlreadyEscalated, alreadyEscalatedSentence}
	refuseEscalateFirst     = refusal{CodeEscalateFirst, audit.OutcomeDeniedPolicy, reasonEscalateFirst, escalateFirstSentence}
	refuseAccountDisabled   = refusal{CodeAccountDisabled, audit.OutcomeDeniedPolicy, reasonAccountDisabled, accountDisabledSentence}
	refuseAlreadyPending    = refusal{CodeAlreadyPending, audit.OutcomeDeniedPolicy, reasonAlreadyPending, alreadyPendingSentence}
	refuseNotInEntra        = refusal{CodeNotInEntra, audit.OutcomeDeniedPolicy, reasonNotInEntra, notInEntraSentence}
	refuseNothingToClear    = refusal{CodeNothingToClear, audit.OutcomeDeniedPolicy, reasonNothingToClear, nothingToClearSentence}
	refuseOtherMethods      = refusal{CodeOtherMethods, audit.OutcomeDeniedPolicy, reasonOtherMethods, otherMethodsSentence}
	refuseApprovalPending   = refusal{CodeApprovalPending, audit.OutcomeDeniedPolicy, reasonApprovalPending, approvalPendingSentence}
	// refuseNotEligible ends an approved execution whose account no longer
	// passes the recheck (spec 0012, AC-11); the model never sees it.
	refuseNotEligible = refusal{CodeProtected, audit.OutcomeDeniedPolicy, reasonAccountNotEligible, protectedSentence}

	// Spec 0013.
	refuseAlreadyMember       = refusal{CodeAlreadyMember, audit.OutcomeDeniedPolicy, reasonAlreadyMember, alreadyMemberSentence}
	refuseNoOwner             = refusal{CodeNoOwner, audit.OutcomeDeniedPolicy, reasonNoOwner, noOwnerSentence}
	refuseOwnerIsRequester    = refusal{CodeOwnerIsRequester, audit.OutcomeDeniedPolicy, reasonOwnerIsRequester, ownerIsRequesterSentence}
	refuseNotRequestable      = refusal{CodeNotRequestable, audit.OutcomeDeniedPolicy, reasonNotRequestable, notRequestableSentence}
	refuseTooManyPending      = refusal{CodeTooManyPending, audit.OutcomeDeniedPolicy, reasonTooManyPending, tooManyPendingSentence}
	refuseOnePerThread        = refusal{CodeOnePerThread, audit.OutcomeDeniedPolicy, reasonOnePerThread, onePerThreadSentence}
	refuseGroupAlreadyPending = refusal{CodeAlreadyPending, audit.OutcomeDeniedPolicy, reasonAlreadyPending, groupAlreadyPendingSentence}
	// refuseAccessPending is the close guard's refusal while an access
	// request is open (spec 0013, AC-18): approval_pending with this tool's
	// sentence, stored under its own reason so the two sentences stay apart.
	refuseAccessPending = refusal{CodeApprovalPending, audit.OutcomeDeniedPolicy, reasonAccessPending, accessPendingSentence}
	// refuseRecheckFailed ends an approved add whose group, owner, or
	// requester no longer passes (spec 0013, AC-14); the model never sees it.
	refuseRecheckFailed = refusal{CodeProtected, audit.OutcomeDeniedPolicy, reasonRecheckFailed, protectedSentence}
)

// alreadyPendingFor is the already_pending refusal in tool's words.
func alreadyPendingFor(tool string) refusal {
	if tool == ToolGroupAccessRequest {
		return refuseGroupAlreadyPending
	}
	return refuseAlreadyPending
}

// handlerRefusals are the refusals a handler may return, found again by
// their stored reason when the dispatcher builds the response.
var handlerRefusals = map[string]refusal{}

func init() {
	for _, r := range []refusal{
		refuseNoHelpdeskAccount, refuseReadOnly, refuseProtected, refuseHasTicket,
		refuseNoTicket, refuseNotYours, refuseTicketClosed, refuseAlreadyEscalated, refuseEscalateFirst,
		refuseAccountDisabled, refuseRateLimited,
		refuseAlreadyPending, refuseNotInEntra, refuseNothingToClear, refuseOtherMethods, refuseNotEligible,
		refuseApprovalPending, refuseAccessPending,
	} {
		handlerRefusals[r.reason] = r
	}
}

// refused is a handler's way to end a call on a refusal.
func refused(r refusal) Result {
	return Result{Result: audit.Result{Outcome: r.outcome, Reason: r.reason}}
}

// failure is a handler error with the code and fixed reason it ends on. The
// wrapped error never reaches the model, a log, or the audit log.
type failure struct {
	code     Code
	reason   string
	sentence string
	err      error
}

func (f *failure) Error() string {
	if f.err == nil {
		return f.reason
	}
	return f.reason + ": " + f.err.Error()
}

func (f *failure) Unwrap() error { return f.err }

func failed(err error) *failure {
	return &failure{code: CodeFailed, reason: reasonActionFailed, sentence: failedSentence, err: err}
}

// directoryFailure classifies an LDAP error (AC-19): network errors,
// timeouts, and the call's deadline are "directory unavailable"; anything
// else, such as insufficient access, is "action failed".
func directoryFailure(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || isNetErr(err) ||
		goldap.IsErrorAnyOf(err, goldap.ErrorNetwork, goldap.LDAPResultUnavailable, goldap.LDAPResultBusy,
			goldap.LDAPResultTimeLimitExceeded) {
		return &failure{code: CodeUnavailable, reason: reasonDirectoryDown, sentence: directoryDownSentence, err: err}
	}
	return failed(err)
}

// helpdeskFailure classifies a Zammad error (AC-19): a 5xx, a network error,
// or a timeout is "helpdesk unavailable"; a 4xx no AC names is "action failed".
func helpdeskFailure(err error) error {
	status := zammad.Status(err)
	if status >= 500 || (status == 0 && !errors.Is(err, zammad.ErrUserWrite)) {
		return &failure{code: CodeUnavailable, reason: reasonHelpdeskDown, sentence: helpdeskDownSentence, err: err}
	}
	return failed(err)
}

// Cause is cause for the reset page, which shares the tool layer's tokens.
func Cause(err error) string { return cause(err) }

// DirectoryReason is the fixed audit reason directoryFailure gives err:
// "directory unavailable" or "action failed".
func DirectoryReason(err error) string { return asFailure(directoryFailure(err)).reason }

// cause names where an error came from as a fixed token that is safe to log
// and holds no request data: an LDAP result code, a Zammad or Graph HTTP
// status, or a class. An operator reads it to tell an outage from a permission problem.
func cause(err error) string {
	var le *goldap.Error
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, audit.ErrAuditUnavailable):
		return "audit"
	case errors.Is(err, zammad.ErrUserWrite):
		return "zammad_user_write"
	case zammad.Status(err) > 0:
		return "zammad_" + strconv.Itoa(zammad.Status(err))
	case errors.As(err, &le):
		return "ldap_" + strconv.Itoa(int(le.ResultCode))
	case errors.Is(err, graph.ErrNotManaged):
		return "graph_not_managed"
	case errors.Is(err, graph.ErrNotFound):
		return "graph_not_found"
	case graph.Status(err) > 0:
		return "graph_" + strconv.Itoa(graph.Status(err))
	case isNetErr(err):
		return "network"
	}
	return "other"
}

func isNetErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne)
}
