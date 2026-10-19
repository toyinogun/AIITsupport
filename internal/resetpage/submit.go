package resetpage

import (
	"context"
	"errors"
	"net"
	"net/http"
	"unicode/utf8"

	goldap "github.com/go-ldap/ldap/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/cause"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/resetlink"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

const tracerName = "github.com/toyinogun/AIITsupport/internal/resetpage"

// ToolPasswordReset is the POST's audit tool name. It is not a registry
// tool: no MCP call can reach it.
const ToolPasswordReset = "password_reset"

// Outcomes of the reset_submit log line and the reset.submit span.
const (
	outcomeOK           = "ok"
	outcomeInvalidLink  = "invalid_link"
	outcomePaused       = "paused"
	outcomeBlocked      = "blocked"
	outcomePolicy       = "policy"
	outcomeFailed       = "failed"
	outcomeTryLater     = "try_later"
	outcomeRevertFailed = "revert_failed"
)

// Fixed audit reasons for the POST's refusals and failures.
const (
	reasonPaused     = "account changes are paused"
	reasonNotMatched = "requester not matched"
	reasonDisabled   = "account disabled"
	reasonProtected  = "group is protected"
	reasonPolicy     = "password rejected by policy"
)

// Ticket notes on success (AC-11).
const (
	noteReset    = "The requester set a new password with the reset link."
	noteUnlocked = "The account lockout was cleared."
)

// serveSubmit is the POST (AC-8 to AC-14). Field errors return before any
// write; every later step logs one reset_submit line in a reset.submit span.
func (p *Page) serveSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	parseErr := r.ParseForm() // the body first, before any other work
	token := r.PathValue("token")
	if !resetlink.WellFormed(token) {
		p.message(w, http.StatusGone, textInvalid)
		return
	}
	link, err := p.cfg.Links.Valid(r.Context(), token)
	if err != nil {
		p.message(w, http.StatusGone, textInvalid)
		return
	}
	password := r.PostForm.Get("password")
	if parseErr != nil || !p.fieldsOK(password, r.PostForm.Get("confirm")) {
		p.form(r.Context(), w, http.StatusUnprocessableEntity, link.Account, textFields)
		return
	}

	// A new trace: nothing upstream of the browser is ours.
	ctx, span := otel.Tracer(tracerName).Start(context.WithoutCancel(r.Context()), "reset.submit", trace.WithNewRoot())
	defer span.End()
	page, outcome, why := p.submit(ctx, link, token, password)
	span.SetAttributes(attribute.String("outcome", outcome))
	p.cfg.Logger.InfoContext(ctx, "reset_submit", "outcome", outcome, "cause", causeOf(why), "trace_id", traceID(ctx))
	page(w, r)
}

// fieldsOK is the AC-8 check: both set, equal, at most 256 characters, and
// at least the cached minPwdLength when known.
func (p *Page) fieldsOK(password, confirm string) bool {
	n := utf8.RuneCountInString(password)
	return password != "" && password == confirm && n <= maxPasswordRunes && n >= p.policy.minLength()
}

// pageFunc writes one response.
type pageFunc func(http.ResponseWriter, *http.Request)

func (p *Page) messagePage(status int, text string) pageFunc {
	return func(w http.ResponseWriter, _ *http.Request) { p.message(w, status, text) }
}

func (p *Page) formPage(account, text string) pageFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.form(r.Context(), w, http.StatusUnprocessableEntity, account, text)
	}
}

// submit runs the POST after the field checks: the in flight slot, the
// deadline, the kill switch, the account recheck, the claim, then the
// audited write. why is the error behind a non ok outcome, for its cause.
func (p *Page) submit(ctx context.Context, link resetlink.Link, token, password string) (page pageFunc, outcome string, why error) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return p.messagePage(http.StatusServiceUnavailable, textTryLater), outcomeTryLater, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.cfg.CallTimeout)
	defer cancel()
	call := auditCall(link, traceID(ctx))

	if !p.cfg.KillSwitch.AccountChangesAllowed() {
		return p.refuse(ctx, call, audit.OutcomeReadOnly, reasonPaused, http.StatusServiceUnavailable, textPaused, outcomePaused)
	}
	user, refusal, err := p.recheck(ctx, link.Account)
	if err != nil {
		return p.messagePage(http.StatusBadGateway, textFailed), outcomeFailed, err
	}
	if refusal != nil {
		return p.refuse(ctx, call, refusal.outcome, refusal.reason, http.StatusForbidden, textBlocked, outcomeBlocked)
	}
	claimed, err := p.cfg.Links.Claim(ctx, token)
	if err != nil {
		return p.messagePage(http.StatusGone, textInvalid), outcomeInvalidLink, ignoreNotValid(err)
	}
	return p.write(ctx, call, claimed, user, password)
}

type recheckRefusal struct {
	outcome audit.Outcome
	reason  string
}

// recheck reads the account again at submit time (AC-10): found, under an
// allowed user OU, enabled, and in no protected group.
func (p *Page) recheck(ctx context.Context, account string) (ldap.User, *recheckRefusal, error) {
	user, found, err := p.cfg.Directory.FindUserByAccount(ctx, account)
	if errors.Is(err, ldap.ErrTooManyMatches) || (err == nil && !found) {
		return ldap.User{}, &recheckRefusal{audit.OutcomeDeniedIdentity, reasonNotMatched}, nil
	}
	if err != nil {
		return ldap.User{}, nil, err
	}
	// A DN outside the user OUs, or one that does not parse, counts as protected.
	if under, ouErr := ldap.UnderAny(user.DN, p.cfg.UserOUs, p.cfg.BaseDN); ouErr != nil || !under {
		return ldap.User{}, &recheckRefusal{audit.OutcomeDeniedPolicy, reasonProtected}, nil //nolint:nilerr // a bad DN refuses, it is not a failure
	}
	if !user.Enabled {
		return ldap.User{}, &recheckRefusal{audit.OutcomeDeniedPolicy, reasonDisabled}, nil
	}
	if err := p.cfg.Accounts.CheckAccount(ctx, user.DN); errors.Is(err, tools.ErrGroupProtected) {
		return ldap.User{}, &recheckRefusal{audit.OutcomeDeniedPolicy, reasonProtected}, nil
	} else if err != nil {
		return ldap.User{}, nil, err
	}
	return user, nil, nil
}

// refuse writes the audit refusal pair and picks the page; a failed audit
// write gives try_later instead.
func (p *Page) refuse(ctx context.Context, call audit.Call, o audit.Outcome, reason string, status int, text, outcome string) (pageFunc, string, error) {
	if err := p.cfg.Recorder.Refuse(context.WithoutCancel(ctx), call, o, reason); err != nil {
		return p.messagePage(http.StatusServiceUnavailable, textTryLater), outcomeTryLater, err
	}
	return p.messagePage(status, text), outcome, nil
}

// actionEnd is how the audited action ended.
type actionEnd int

const (
	endFailed actionEnd = iota
	endOK
	endPaused
	endPolicy
)

// write is the audited part (AC-11, AC-12, AC-14). The link is already used;
// anything short of success reverts it while it is unexpired.
func (p *Page) write(ctx context.Context, call audit.Call, link resetlink.Link, user ldap.User, password string) (pageFunc, string, error) {
	end := endFailed
	var ticketErr error
	_, runErr := p.cfg.Recorder.Run(ctx, call, func(ctx context.Context) (audit.Result, error) {
		if !p.cfg.KillSwitch.AccountChangesAllowed() {
			end = endPaused
			return audit.Result{Outcome: audit.OutcomeReadOnly, Reason: reasonPaused}, nil
		}
		lock, err := p.cfg.Directory.Lock(ctx, user.DN)
		if err != nil {
			return audit.Result{}, audit.WithReason(tools.DirectoryReason(err), err)
		}
		if err := p.setPassword(ctx, user.DN, password, link); err != nil {
			if ldap.IsPolicyRefusal(err) {
				end = endPolicy
				return audit.Result{}, audit.WithReason(reasonPolicy, err)
			}
			return audit.Result{}, audit.WithReason(tools.DirectoryReason(err), err)
		}
		end = endOK
		var closed bool
		closed, ticketErr = p.cfg.Tickets.CloseWithNote(ctx, int(link.TicketID), closeNote(lock.Locked))
		return audit.Result{Outcome: audit.OutcomeOK, Data: map[string]any{"was_locked": lock.Locked, "ticket_closed": closed}}, nil
	})

	switch {
	case end == endOK: // an incomplete outcome row does not undo a set password
		return p.messagePage(http.StatusOK, textDone), outcomeOK, ticketErr
	case errors.Is(runErr, audit.ErrAuditUnavailable):
		return p.revert(ctx, link, p.messagePage(http.StatusServiceUnavailable, textTryLater), outcomeTryLater, runErr)
	case end == endPaused:
		return p.revert(ctx, link, p.messagePage(http.StatusServiceUnavailable, textPaused), outcomePaused, nil)
	case end == endPolicy:
		return p.revert(ctx, link, p.formPage(link.Account, textPolicy), outcomePolicy, runErr)
	default:
		return p.revert(ctx, link, p.messagePage(http.StatusBadGateway, textFailed), outcomeFailed, runErr)
	}
}

// setPassword makes the one modify. A timeout or lost connection after it
// was sent rereads pwdLastSet: newer than the claim means it took (AC-12).
func (p *Page) setPassword(ctx context.Context, dn, password string, link resetlink.Link) error {
	err := p.cfg.Writer.SetPassword(ctx, dn, password)
	if err == nil || !ambiguous(err) {
		return err
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.CallTimeout)
	defer cancel()
	set, readErr := p.cfg.Directory.PasswordLastSet(readCtx, dn)
	if readErr == nil && set.After(link.UsedAt) {
		return nil
	}
	return err
}

// ambiguous is a failure after which the modify may or may not have landed.
func ambiguous(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) || goldap.IsErrorWithCode(err, goldap.ErrorNetwork)
}

// revert puts the claim back (unexpired only). A failed revert leaves the
// link used, fail closed, and logs its cause.
func (p *Page) revert(ctx context.Context, link resetlink.Link, page pageFunc, outcome string, why error) (pageFunc, string, error) {
	revertCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.CallTimeout)
	defer cancel()
	if _, err := p.cfg.Links.Revert(revertCtx, link); err != nil {
		return page, outcomeRevertFailed, err
	}
	return page, outcome, why
}

func closeNote(wasLocked bool) string {
	if wasLocked {
		return noteReset + "\n" + noteUnlocked
	}
	return noteReset
}

// auditCall is the POST's audit call (spec 0011, the POST audit call table).
func auditCall(l resetlink.Link, traceID string) audit.Call {
	return audit.Call{
		Tool:             ToolPasswordReset,
		Tier:             audit.TierLow,
		RequesterSlackID: l.RequesterSlackID,
		RequesterAccount: l.Account,
		ChannelID:        l.ChannelID,
		ThreadTS:         l.ThreadTS,
		TicketID:         l.TicketID,
		Target:           l.Account,
		Args:             map[string]any{"link_id": l.ID.String()},
		Store:            []string{"link_id"},
		StoreResult:      []string{"was_locked", "ticket_closed"},
		TraceID:          traceID,
	}
}

// causeOf is the tool layer's token for LDAP and Zammad errors, else
// cause.Of's (Postgres, deadline, network).
func causeOf(err error) string {
	if c := tools.Cause(err); c != "other" {
		return c
	}
	return cause.Of(err)
}

func ignoreNotValid(err error) error {
	if errors.Is(err, resetlink.ErrNotValid) {
		return nil
	}
	return err
}

func traceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
