package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/ldap"
)

const tracerName = "github.com/toyinogun/AIITsupport/internal/tools"

// Config is the dispatcher's settings, from internal/config.
type Config struct {
	BaseDN            string        // LDAP_BASE_DN
	UserOUs           []string      // TOOL_USER_OUS, relative to BaseDN
	GroupOUs          []string      // TOOL_GROUP_OUS, relative to BaseDN
	DeniedGroups      []string      // TOOL_DENIED_GROUPS, on top of the code floor
	CallTimeout       time.Duration // TOOL_CALL_TIMEOUT
	WriteLimit        int           // TOOL_WRITE_LIMIT
	WriteWindow       time.Duration // TOOL_WRITE_WINDOW
	TicketCreateLimit int           // TICKET_CREATE_LIMIT
	TicketMaxPending  time.Duration // TICKET_MAX_PENDING
	PrivilegedWindow  time.Duration // PRIVILEGED_ATTEMPT_WINDOW
}

// Check refuses an OU list that is empty or holds an entry that is empty,
// does not parse as a DN, or names a DC= component (AC-16).
func (c Config) Check() error {
	var errs []error
	if _, err := goldap.ParseDN(c.BaseDN); err != nil || c.BaseDN == "" {
		errs = append(errs, errors.New("LDAP_BASE_DN must be a DN"))
	}
	for name, ous := range map[string][]string{"TOOL_USER_OUS": c.UserOUs, "TOOL_GROUP_OUS": c.GroupOUs} {
		if len(ous) == 0 {
			errs = append(errs, fmt.Errorf("%s must name at least one OU", name))
		}
		for i, ou := range ous {
			if err := ldap.CheckRelativeOU(ou); err != nil {
				errs = append(errs, fmt.Errorf("%s entry %d: %w", name, i+1, err))
			}
		}
	}
	if c.CallTimeout <= 0 || c.WriteWindow <= 0 || c.TicketMaxPending <= 0 || c.WriteLimit < 1 || c.TicketCreateLimit < 1 ||
		c.PrivilegedWindow < time.Hour {
		errs = append(errs, errors.New("tool layer timeouts and limits must be positive, and the privileged attempt window at least 1h"))
	}
	return errors.Join(errs...)
}

// Systems are the clients the dispatcher hands to handlers.
type Systems struct {
	Directory  Directory
	Writer     DirectoryWriter
	Helpdesk   *Helpdesk
	Recorder   *audit.Recorder
	KillSwitch KillSwitch
	// Reset is the reset link store and settings; required exactly when the
	// registry holds password_reset_link (spec 0011).
	Reset *ResetSystem
}

// ResetSystem is what the reset link handler needs beyond Systems.
type ResetSystem struct {
	Links    ResetLinks
	Settings ResetSettings
	// DeniedGroups is RESET_DENIED_GROUPS: members never get a link.
	DeniedGroups []string
}

// Dispatcher runs every tool call through the same ordered checks, then the
// handler inside the audit recorder (spec 0007, the dispatcher pipeline).
type Dispatcher struct {
	reg   *Registry
	cfg   Config
	sys   Systems
	fence *Fence
	// accounts guards reset link targets; nil without the reset system.
	accounts *AccountFence
	logger   *slog.Logger
	tracer   trace.Tracer
	now      func() time.Time
	// beforeHandler runs after every check and before the handler. Tests
	// use it to flip the kill switch between its two reads.
	beforeHandler func()
}

// NewDispatcher checks cfg and sys, so the tool server refuses to start on a
// bad OU list or a missing client.
func NewDispatcher(reg *Registry, cfg Config, sys Systems, logger *slog.Logger) (*Dispatcher, error) {
	err := cfg.Check()
	if reg == nil || sys.Directory == nil || sys.Writer == nil || sys.Helpdesk == nil || sys.Recorder == nil || sys.KillSwitch == nil {
		err = errors.Join(err, errors.New("the dispatcher needs a registry, a directory, a writer, a helpdesk, a recorder, and a kill switch"))
	}
	if reg != nil {
		if _, ok := reg.Lookup(ToolPasswordResetLink); ok && (sys.Reset == nil || sys.Reset.Links == nil) {
			err = errors.Join(err, errors.New("password_reset_link needs the reset link store"))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("tool layer: %w", err)
	}
	var accounts *AccountFence
	if sys.Reset != nil {
		accounts = NewAccountFence(sys.Directory, sys.Reset.DeniedGroups, logger)
	}
	return &Dispatcher{
		reg: reg, cfg: cfg, sys: sys, logger: logger, accounts: accounts,
		fence: &Fence{
			dir: sys.Directory, groupOUs: cfg.GroupOUs, base: cfg.BaseDN,
			denied: cfg.DeniedGroups, logger: logger,
		},
		tracer: otel.Tracer(tracerName),
		now:    time.Now,
	}, nil
}

// Registry is the registry the dispatcher serves.
func (d *Dispatcher) Registry() *Registry { return d.reg }

// Fence is the group guard, for group tools (#15).
func (d *Dispatcher) Fence() *Fence { return d.fence }

// Response is what the model gets back. Data is the structured content; on
// a refusal or failure it holds only "code".
type Response struct {
	Code    Code // empty on success
	Text    string
	IsError bool
	Data    map[string]any
}

// Call runs one tool call under TOOL_CALL_TIMEOUT, in one tool.call span, and
// logs one tool_call line with only the tool, tier, outcome, and trace id.
func (d *Dispatcher) Call(ctx context.Context, req Request) Response {
	t, ok := d.reg.Lookup(req.Tool)
	if !ok { // mcpserver registers only registry tools, so this is a wiring bug
		return failureResponse(failed(nil))
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.CallTimeout)
	defer cancel()
	ctx, span := d.tracer.Start(ctx, "tool.call", trace.WithAttributes(
		attribute.String("tool", t.Name), attribute.String("tier", string(t.Tier))))
	defer span.End()

	resp, outcome := d.dispatch(ctx, t, req)
	span.SetAttributes(attribute.String("outcome", outcome))
	d.logger.InfoContext(ctx, "tool_call", "tool", t.Name, "tier", string(t.Tier), "outcome", outcome, "trace_id", traceID(ctx))
	return resp
}

// dispatch is the pipeline from step 3 on (steps 1 and 2, the token and
// _meta, are mcpserver's). The order is part of the contract.
func (d *Dispatcher) dispatch(ctx context.Context, t *Tool, req Request) (Response, string) {
	now := d.now()
	call := audit.Call{
		Tool: t.Name, Tier: t.Tier,
		RequesterSlackID: req.Requester.SlackID,
		ChannelID:        req.Requester.ChannelID,
		ThreadTS:         req.Requester.ThreadTS,
		TicketID:         req.Requester.TicketID,
		Store:            t.Store,
		StoreResult:      t.StoreResult,
		TraceID:          traceID(ctx),
	}
	if t.Target == TargetTicket {
		call.Target = ticketTarget(req.Requester.TicketID)
	}

	// 3. A blocked tool never runs and never reaches LDAP or Zammad.
	if t.Tier == audit.TierBlocked {
		return d.refuse(ctx, call, refuseBlocked)
	}
	// 4. Arguments, before identity or any other system. Nothing is stored
	// from a call that fails here.
	args, bad := t.parseArgs(req.Args, CheckEnv{Now: now, MaxPending: d.cfg.TicketMaxPending, Initiator: req.Requester.Initiator})
	if bad != nil {
		return d.refuseInvalid(ctx, call, bad.field)
	}
	call.Args = anyArgs(args)

	// 5 and 6. Identity.
	account, found, err := d.identify(ctx, req.Requester.Email)
	if err != nil {
		return d.fail(ctx, call, err)
	}
	if found != matched {
		return d.refuse(ctx, call, map[match]refusal{notMatched: refuseNotMatched, ambiguous: refuseAmbiguous}[found])
	}
	call.RequesterAccount = account.Username
	if t.Target == TargetRequester {
		call.Target = account.Username
	}

	// 7. The kill switch, for account changes only.
	if t.accountChange() && !d.sys.KillSwitch.AccountChangesAllowed() {
		return d.refuse(ctx, call, refuseReadOnly)
	}
	// 8. The rate limit.
	if limit := d.limitFor(t); limit > 0 {
		n, err := d.sys.Recorder.CountRecentOK(recordCtx(ctx), t.Name, req.Requester.SlackID, now.Add(-d.cfg.WriteWindow))
		if err != nil {
			return d.auditUnavailable(ctx)
		}
		if n >= int64(limit) {
			return d.refuse(ctx, call, refuseRateLimited)
		}
	}
	// The checks may have used up the call's budget. The handler would then
	// fail anyway, but its intent insert would fail first and leave no pair.
	if err := ctx.Err(); err != nil {
		return d.fail(ctx, call, directoryFailure(err))
	}
	// 9. A medium tool waits for a human; its handler never runs here.
	if t.Tier == audit.TierMedium {
		return d.deferCall(ctx, t, call)
	}
	// 10. The handler, inside the recorder.
	return d.run(ctx, t, call, Input{Requester: req.Requester, Account: account, Args: args, Now: now})
}

type match int

const (
	matched match = iota
	notMatched
	ambiguous
)

// identify finds the requester's one enabled account under an allowed user
// OU whose mail equals the email, ignoring case (AC-4).
func (d *Dispatcher) identify(ctx context.Context, email string) (ldap.User, match, error) {
	users, err := d.sys.Directory.FindUsersByMail(ctx, email)
	if errors.Is(err, ldap.ErrTooManyMatches) {
		return ldap.User{}, ambiguous, nil
	}
	if err != nil {
		return ldap.User{}, notMatched, directoryFailure(err)
	}
	var keep []ldap.User
	for _, u := range users {
		if !u.Enabled || !strings.EqualFold(u.Mail, email) {
			continue
		}
		if under, err := ldap.UnderAny(u.DN, d.cfg.UserOUs, d.cfg.BaseDN); err != nil || !under {
			continue
		}
		keep = append(keep, u)
	}
	switch len(keep) {
	case 0:
		return ldap.User{}, notMatched, nil
	case 1:
		return keep[0], matched, nil
	default:
		return ldap.User{}, ambiguous, nil
	}
}

func (d *Dispatcher) limitFor(t *Tool) int {
	switch {
	case t.accountChange():
		return d.cfg.WriteLimit
	case t.TicketLimited:
		return d.cfg.TicketCreateLimit
	}
	return 0
}

func (d *Dispatcher) deferCall(ctx context.Context, t *Tool, call audit.Call) (Response, string) {
	approval, err := t.Approval(ctx, call)
	if err != nil {
		return d.fail(ctx, call, failed(err))
	}
	if _, err := d.sys.Recorder.Defer(recordCtx(ctx), call, approval); err != nil {
		return d.auditUnavailable(ctx)
	}
	return Response{
		Code: CodePendingApproval, Text: pendingApprovalSentence,
		Data: map[string]any{"code": string(CodePendingApproval)},
	}, string(audit.OutcomePendingApproval)
}

func (d *Dispatcher) run(ctx context.Context, t *Tool, call audit.Call, in Input) (Response, string) {
	deps := Deps{
		Directory: d.sys.Directory,
		Helpdesk:  d.sys.Helpdesk,
		History:   d.sys.Recorder,
		Fence:     d.fence,
		Limits:    Limits{TicketMaxPending: d.cfg.TicketMaxPending, PrivilegedWindow: d.cfg.PrivilegedWindow},
	}
	var res Result
	var handlerErr error
	_, err := d.sys.Recorder.Run(ctx, call, func(ctx context.Context) (audit.Result, error) {
		if d.beforeHandler != nil {
			d.beforeHandler()
		}
		switch h := t.Handler.(type) {
		case AccountHandler:
			res, handlerErr = h(ctx, AccountDeps{Deps: deps, Writer: d.sys.Writer, Switch: d.sys.KillSwitch}, in)
		case ReadHandler:
			res, handlerErr = h(ctx, deps, in)
		case ResetHandler:
			res, handlerErr = h(ctx, ResetDeps{Deps: deps, Links: d.sys.Reset.Links, Settings: d.sys.Reset.Settings, Accounts: d.accounts}, in)
		default:
			handlerErr = failed(errors.New("unknown handler type"))
		}
		if handlerErr != nil {
			f := asFailure(handlerErr)
			return audit.Result{}, audit.WithReason(f.reason, f)
		}
		return res.Result, nil
	})

	// A handler error comes first: it can wrap ErrAuditUnavailable (a failed
	// history read) although its intent and outcome were written.
	switch {
	case handlerErr != nil:
		return d.failure(ctx, call.Tool, asFailure(handlerErr)), string(audit.OutcomeError)
	case errors.Is(err, audit.ErrAuditUnavailable):
		return d.auditUnavailable(ctx)
	case err != nil && !errors.Is(err, audit.ErrAuditIncomplete):
		return d.failure(ctx, call.Tool, failed(err)), string(audit.OutcomeError)
	}
	// An incomplete audit after a successful action still returns the real
	// result, so the model does not retry; the recorder logged it.
	if res.Outcome != "" && res.Outcome != audit.OutcomeOK {
		r, ok := handlerRefusals[res.Reason]
		if !ok {
			return d.failure(ctx, call.Tool, failed(nil)), string(audit.OutcomeError)
		}
		return refusalResponse(r.code, r.sentence), string(r.outcome)
	}
	return Response{Text: res.Text, Data: res.Data}, string(audit.OutcomeOK)
}

// recordCtx is the context for an audit write or read outside the handler:
// it keeps the span but not the call's deadline, so a call that ran out of
// time is still recorded. The recorder bounds each write by its own timeout.
func recordCtx(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

// refuse records a call that never runs and tells the model why.
func (d *Dispatcher) refuse(ctx context.Context, call audit.Call, r refusal) (Response, string) {
	if err := d.sys.Recorder.Refuse(recordCtx(ctx), call, r.outcome, r.reason); err != nil {
		return d.auditUnavailable(ctx)
	}
	return refusalResponse(r.code, r.sentence), string(r.outcome)
}

// refuseInvalid names the schema field that failed, never its value.
func (d *Dispatcher) refuseInvalid(ctx context.Context, call audit.Call, field string) (Response, string) {
	if err := d.sys.Recorder.Refuse(recordCtx(ctx), call, audit.OutcomeDeniedPolicy, reasonInvalidArguments); err != nil {
		return d.auditUnavailable(ctx)
	}
	sentence := invalidArgumentsSentence + "."
	if field != "" {
		sentence = invalidArgumentsSentence + " (field: " + field + ")."
	}
	return refusalResponse(CodeInvalidArguments, sentence), string(audit.OutcomeDeniedPolicy)
}

// fail records a call that failed before its handler (a directory error
// during identity, a broken approval hook) as an intent and an error outcome.
func (d *Dispatcher) fail(ctx context.Context, call audit.Call, err error) (Response, string) {
	f := asFailure(err)
	ran := false
	_, _ = d.sys.Recorder.Run(recordCtx(ctx), call, func(context.Context) (audit.Result, error) {
		ran = true
		return audit.Result{}, audit.WithReason(f.reason, f)
	})
	// Only an intent that was never written stops the action from running;
	// f itself can wrap ErrAuditUnavailable.
	if !ran {
		return d.auditUnavailable(ctx)
	}
	return d.failure(ctx, call.Tool, f), string(audit.OutcomeError)
}

// failure logs one tool_call_failed line with a fixed cause token (an LDAP
// result code, a Zammad status, or a class), never error text, and returns
// the failure's response.
func (d *Dispatcher) failure(ctx context.Context, tool string, f *failure) Response {
	d.logger.WarnContext(ctx, "tool_call_failed", "tool", tool, "code", string(f.code), "cause", cause(f.err), "trace_id", traceID(ctx))
	return failureResponse(f)
}

// auditUnavailable: the intent could not be written, so nothing ran.
func (d *Dispatcher) auditUnavailable(ctx context.Context) (Response, string) {
	d.logger.ErrorContext(ctx, "audit_unavailable")
	return failureResponse(failed(nil)), string(audit.OutcomeError)
}

func refusalResponse(code Code, sentence string) Response {
	return Response{
		Code: code, IsError: true,
		Text: string(code) + ": " + sentence,
		Data: map[string]any{"code": string(code)},
	}
}

func failureResponse(f *failure) Response {
	return refusalResponse(f.code, f.sentence)
}

func asFailure(err error) *failure {
	if f, ok := errors.AsType[*failure](err); ok {
		return f
	}
	return failed(err)
}

func anyArgs(a Args) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

func traceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
