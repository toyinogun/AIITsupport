package tools

import (
	"context"
	"errors"
	"net"
	"strconv"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/toyinogun/AIITsupport/internal/audit"
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
)

// handlerRefusals are the refusals a handler may return, found again by
// their stored reason when the dispatcher builds the response.
var handlerRefusals = map[string]refusal{}

func init() {
	for _, r := range []refusal{
		refuseNoHelpdeskAccount, refuseReadOnly, refuseProtected, refuseHasTicket,
		refuseNoTicket, refuseNotYours, refuseTicketClosed,
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

// cause names where an error came from as a fixed token that is safe to log
// and holds no request data: an LDAP result code, a Zammad HTTP status, or a
// class. An operator reads it to tell an outage from a permission problem.
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
	case isNetErr(err):
		return "network"
	}
	return "other"
}

func isNetErr(err error) bool {
	var ne net.Error
	return errors.As(err, &ne)
}
