package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"strings"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
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
	// The approved execution (spec 0012 AC-11, moved here by spec 0013 AC-4
	// so Execute works without the GRAPH_* settings).
	ExecuteTimeout time.Duration // APPROVAL_EXECUTE_TIMEOUT
	ExecuteGrace   time.Duration // APPROVAL_EXECUTE_GRACE
	// AccessMaxPending is ACCESS_MAX_PENDING (spec 0013, AC-5).
	AccessMaxPending int
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
		c.PrivilegedWindow < time.Hour || c.ExecuteTimeout <= 0 || c.ExecuteGrace <= 0 || c.AccessMaxPending < 1 {
		errs = append(errs, errors.New("tool layer timeouts and limits must be positive, and the privileged attempt window at least 1h"))
	}
	return errors.Join(errs...)
}

// Systems are the clients the dispatcher hands to handlers.
type Systems struct {
	Directory Directory
	// Groups and Reader are the group reads and the account lookup the
	// access request needs (spec 0013); *ldap.Client is both.
	Groups     GroupReader
	Reader     AccountReader
	Writer     DirectoryWriter
	Helpdesk   *Helpdesk
	Recorder   *audit.Recorder
	KillSwitch KillSwitch
	// Reset is the reset link store and settings; required exactly when the
	// registry holds password_reset_link (spec 0011).
	Reset *ResetSystem
	// MFA is what mfa_clear needs; required exactly when the registry holds
	// it (spec 0012).
	MFA *MFASystem
}

// MFASystem is what the MFA tool needs beyond Systems.
type MFASystem struct {
	Graph  GraphAPI
	Reader AccountReader
	// DeniedGroups is MFA_DENIED_GROUPS: members never get a clear.
	DeniedGroups []string
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
	// mfaAccounts guards MFA clears; nil without the MFA system.
	mfaAccounts *AccountFence
	logger      *slog.Logger
	tracer      trace.Tracer
	now         func() time.Time
	// beforeHandler runs after every check and before the handler. Tests
	// use it to flip the kill switch between its two reads.
	beforeHandler func()
}

// NewDispatcher checks cfg and sys, so the tool server refuses to start on a
// bad OU list or a missing client.
func NewDispatcher(reg *Registry, cfg Config, sys Systems, logger *slog.Logger) (*Dispatcher, error) {
	err := cfg.Check()
	if reg == nil || sys.Directory == nil || sys.Groups == nil || sys.Reader == nil || sys.Writer == nil || sys.Helpdesk == nil ||
		sys.Recorder == nil || sys.KillSwitch == nil {
		err = errors.Join(err, errors.New("the dispatcher needs a registry, a directory with its group reads and account lookup, "+
			"a writer, a helpdesk, a recorder, and a kill switch"))
	}
	if reg != nil {
		if _, ok := reg.Lookup(ToolPasswordResetLink); ok && (sys.Reset == nil || sys.Reset.Links == nil) {
			err = errors.Join(err, errors.New("password_reset_link needs the reset link store"))
		}
		if _, ok := reg.Lookup(ToolMFAClear); ok && (sys.MFA == nil || sys.MFA.Graph == nil || sys.MFA.Reader == nil) {
			err = errors.Join(err, errors.New("mfa_clear needs Graph and the account reader"))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("tool layer: %w", err)
	}
	var accounts, mfaAccounts *AccountFence
	if sys.Reset != nil {
		accounts = NewAccountFence(sys.Directory, sys.Reset.DeniedGroups, logger)
	}
	if sys.MFA != nil {
		mfaAccounts = NewAccountFence(sys.Directory, sys.MFA.DeniedGroups, logger)
	}
	return &Dispatcher{
		reg: reg, cfg: cfg, sys: sys, logger: logger, accounts: accounts, mfaAccounts: mfaAccounts,
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
	args, lists, bad := t.parseArgs(req.Args, CheckEnv{Now: now, MaxPending: d.cfg.TicketMaxPending, Initiator: req.Requester.Initiator})
	if bad != nil {
		return d.refuseInvalid(ctx, call, bad.field)
	}
	call.Args = anyArgs(args, lists)

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
	in := Input{Requester: req.Requester, Account: account, Args: args, Lists: lists, Now: now}
	// 9. A medium tool waits for a human; its handler never runs here.
	if t.Tier == audit.TierMedium {
		return d.deferCall(ctx, t, call, in)
	}
	// 10. The handler, inside the recorder.
	return d.run(ctx, t, call, in)
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

// deferCall is step 9 (spec 0012 AC-3 to AC-5, spec 0013 AC-5 to AC-7): the
// tool's precheck, then a refusal, a reuse of this thread's pending
// approval, or a new approval and its ticket note. The handler never runs here.
func (d *Dispatcher) deferCall(ctx context.Context, t *Tool, call audit.Call, in Input) (Response, string) {
	call.PendingResult = t.PendingResult
	pre, err := d.precheck(ctx, t, in)
	if t.Target == TargetGroup {
		call.Target = pre.Target // empty until the group resolved
	}
	switch {
	case err != nil:
		return d.fail(ctx, call, err)
	case pre.Refusal != nil:
		return d.refuse(ctx, call, *pre.Refusal)
	case pre.Reuse != nil:
		return d.reuse(ctx, t, call, *pre.Reuse, pre, in.Now)
	}
	if pre.Args != nil {
		call.Args = anyArgs(pre.Args, nil)
	}
	req, err := t.Approval(ctx, call, pre, in.Now)
	if err != nil {
		return d.fail(ctx, call, failed(err))
	}
	data := map[string]any{"expires_at": req.ExpiresAt.UTC().Format(time.RFC3339), "reused": false}
	maps.Copy(data, pre.Data)
	approval, err := d.sys.Recorder.Defer(recordCtx(ctx), call, req, withApprover(data, pre))
	if errors.Is(err, audit.ErrApprovalPending) {
		// Another thread's request passed the precheck at the same moment.
		return d.refuse(ctx, call, alreadyPendingFor(t.Name))
	}
	if err != nil {
		return d.auditUnavailable(ctx)
	}
	noted := false
	if pre.Note != nil && call.TicketID != 0 {
		noted = d.sys.Helpdesk.addInternalNote(ctx, int(call.TicketID), pre.Note(req.ExpiresAt)) == nil
	}
	data["ticket_noted"] = noted
	return d.pendingResponse(t, approval.ID.String(), req.ExpiresAt, in.Now, call.TicketID, data), string(audit.OutcomePendingApproval)
}

// precheck runs the tool's one precheck family (spec 0013, AC-4).
func (d *Dispatcher) precheck(ctx context.Context, t *Tool, in Input) (Prechecked, error) {
	if t.GroupPrecheck != nil {
		return t.GroupPrecheck(ctx, d.groupDeps(), in)
	}
	return t.Precheck(ctx, d.mfaDeps(), in)
}

// withApprover adds the approver account to what the pending audit row may
// keep; the model's response never carries it.
func withApprover(data map[string]any, pre Prechecked) map[string]any {
	if pre.Owner.Account == "" {
		return data
	}
	kept := maps.Clone(data)
	kept["approver_account"] = pre.Owner.Account
	return kept
}

// reuse answers a repeat request in the thread that already holds a pending
// approval (spec 0012, AC-5): an audit pair, no new approval, no note.
func (d *Dispatcher) reuse(ctx context.Context, t *Tool, call audit.Call, a queries.Approval, pre Prechecked, now time.Time) (Response, string) {
	data := map[string]any{"expires_at": a.ExpiresAt.UTC().Format(time.RFC3339), "reused": true}
	maps.Copy(data, pre.Data)
	if err := d.sys.Recorder.Reuse(recordCtx(ctx), call, a.ID, withApprover(data, pre)); err != nil {
		return d.auditUnavailable(ctx)
	}
	data["ticket_noted"] = false
	return d.pendingResponse(t, a.ID.String(), a.ExpiresAt, now, a.ZammadTicketID.Int64, data), string(audit.OutcomePendingApproval)
}

// pendingResponse is the pending result (spec 0012 AC-4, spec 0013 AC-7):
// not an error, so the model reads it as "wait". An access request counts
// its window in hours, an IT approval in minutes.
func (d *Dispatcher) pendingResponse(t *Tool, approvalID string, expiresAt, now time.Time, ticketID int64, data map[string]any) Response {
	out := map[string]any{
		"code":        string(CodePendingApproval),
		"approval_id": approvalID,
		"ticket_id":   ticketID,
	}
	text := pendingApprovalSentence
	if t.Name == ToolGroupAccessRequest {
		out["expires_in_hours"] = int64(math.Ceil(expiresAt.Sub(now).Hours()))
		text = groupPendingSentence
	} else {
		out["expires_in_minutes"] = int64(math.Ceil(expiresAt.Sub(now).Minutes()))
	}
	maps.Copy(out, data)
	if u := d.sys.Helpdesk.ticketURL(int(ticketID)); u != "" {
		out["ticket_url"] = u
	}
	return Response{Code: CodePendingApproval, Text: text, Data: out}
}

func (d *Dispatcher) deps() Deps {
	return Deps{
		Directory: d.sys.Directory,
		Groups:    d.sys.Groups,
		Helpdesk:  d.sys.Helpdesk,
		History:   d.sys.Recorder,
		Fence:     d.fence,
		Limits:    Limits{TicketMaxPending: d.cfg.TicketMaxPending, PrivilegedWindow: d.cfg.PrivilegedWindow},
		Scope:     Scope{BaseDN: d.cfg.BaseDN, UserOUs: d.cfg.UserOUs, GroupOUs: d.cfg.GroupOUs},
	}
}

// groupDeps is GroupDeps (spec 0013, AC-4).
func (d *Dispatcher) groupDeps() GroupDeps {
	return GroupDeps{
		Deps: d.deps(), Reader: d.sys.Reader, Writer: d.sys.Writer, Approvals: d.sys.Recorder,
		Switch: d.sys.KillSwitch, MaxPending: d.cfg.AccessMaxPending,
	}
}

// mfaDeps is MFADeps; registry construction and NewDispatcher make sure the
// MFA system is there whenever a medium tool is.
func (d *Dispatcher) mfaDeps() MFADeps {
	md := MFADeps{
		Deps: d.deps(), Approvals: d.sys.Recorder, Accounts: d.mfaAccounts, Switch: d.sys.KillSwitch,
		UserOUs: d.cfg.UserOUs, BaseDN: d.cfg.BaseDN,
	}
	if d.sys.MFA != nil {
		md.Graph, md.Reader = d.sys.MFA.Graph, d.sys.MFA.Reader
	}
	return md
}

func (d *Dispatcher) run(ctx context.Context, t *Tool, call audit.Call, in Input) (Response, string) {
	deps := d.deps()
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
		default: // an MFAHandler or GroupHandler runs only from Execute
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

func anyArgs(a Args, l Lists) map[string]any {
	out := make(map[string]any, len(a)+len(l))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range l {
		out[k] = v
	}
	return out
}

// ResolveDecider maps a clicker's confirmed Slack email to their directory
// account by the requester rule (spec 0013, AC-13): exactly one enabled
// account under TOOL_USER_OUS with that mail. ok is false when none or more
// than one match. The approval routes hold no directory access of their own.
func (d *Dispatcher) ResolveDecider(ctx context.Context, email string) (account string, ok bool, err error) {
	u, found, err := d.identify(ctx, email)
	if err != nil || found != matched {
		return "", false, err
	}
	return u.Username, true, nil
}

// ExecuteGrace is APPROVAL_EXECUTE_GRACE, for the approval routes' abandon.
func (d *Dispatcher) ExecuteGrace() time.Duration { return d.cfg.ExecuteGrace }

func traceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}
