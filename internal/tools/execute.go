package tools

import (
	"context"
	"errors"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/ldap"
)

// Execute runs an approved call once (spec 0012, AC-11): through the
// registry entry of its tool, inside Recorder.Run with a new call id that
// names the approval and its approver, under APPROVAL_EXECUTE_TIMEOUT. The
// call is built from the approval row, never from _meta. Afterwards the
// approval is marked executed (outcome ok) or failed (anything else). It
// returns the outcome; nothing is ever retried. The row is read again
// first: one that is no longer approved never runs.
func (d *Dispatcher) Execute(ctx context.Context, approved queries.Approval) audit.Outcome {
	a, err := d.sys.Recorder.Approval(recordCtx(ctx), approved.ID)
	if err != nil || !executable(a) {
		d.logger.WarnContext(ctx, "approval_not_executable", "approval_id", approved.ID.String())
		return audit.OutcomeError
	}
	t, ok := d.reg.Lookup(a.ToolName)
	var handle func(context.Context, Input) (Result, error)
	if ok {
		handle, ok = d.approvedHandler(t)
	}
	if !ok {
		// Never happens: only registry tools are ever deferred.
		d.mark(ctx, a, false)
		return audit.OutcomeError
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.ExecuteTimeout)
	defer cancel()
	ctx, span := d.tracer.Start(ctx, "tool.call", trace.WithAttributes(
		attribute.String("tool", t.Name), attribute.String("tier", string(t.Tier)),
		attribute.String("approval_id", a.ID.String())))
	defer span.End()

	account := a.RequesterAccount.String
	call := audit.Call{
		Tool: t.Name, Tier: t.Tier,
		RequesterSlackID: a.RequesterSlackID,
		RequesterAccount: account,
		ChannelID:        a.SlackChannelID,
		ThreadTS:         a.SlackThreadTs,
		TicketID:         a.ZammadTicketID.Int64,
		Target:           account,
		Store:            t.Store,
		StoreResult:      t.StoreResult,
		TraceID:          traceID(ctx),
		ApprovalID:       a.ID,
		ApproverSlackID:  a.DeciderSlackID.String,
	}
	if t.Target == TargetGroup {
		call.Target = a.Target.String
	}
	now := d.now()
	args, lists, bad := t.parseArgs(a.Arguments, CheckEnv{Now: now, MaxPending: d.cfg.TicketMaxPending})
	call.Args = anyArgs(args, lists)
	in := Input{
		Requester: Requester{
			SlackID: a.RequesterSlackID, ChannelID: a.SlackChannelID, ThreadTS: a.SlackThreadTs, TicketID: a.ZammadTicketID.Int64,
		},
		Account: ldap.User{Username: account}, Args: args, Lists: lists, Now: now,
		ApproverSlackID: a.DeciderSlackID.String, Approver: a.DecidedBy.String,
	}

	var res Result
	var handlerErr error
	_, err = d.sys.Recorder.Run(ctx, call, func(ctx context.Context) (audit.Result, error) {
		if bad != nil || account == "" {
			handlerErr = failed(errors.New("the approved call does not parse"))
		} else {
			if d.beforeHandler != nil {
				d.beforeHandler()
			}
			res, handlerErr = handle(ctx, in)
		}
		if handlerErr != nil {
			f := asFailure(handlerErr)
			return audit.Result{}, audit.WithReason(f.reason, f)
		}
		return res.Result, nil
	})

	outcome := res.Outcome
	switch {
	case handlerErr != nil:
		f := asFailure(handlerErr)
		d.logger.WarnContext(ctx, "tool_call_failed", "tool", t.Name, "code", string(f.code), "cause", cause(f.err), "trace_id", traceID(ctx))
		outcome = audit.OutcomeError
	case errors.Is(err, audit.ErrAuditUnavailable):
		d.logger.ErrorContext(ctx, "audit_unavailable")
		outcome = audit.OutcomeError
	case outcome == "":
		outcome = audit.OutcomeOK
	}
	d.mark(ctx, a, outcome == audit.OutcomeOK)
	span.SetAttributes(attribute.String("outcome", string(outcome)))
	d.logger.InfoContext(ctx, "tool_call", "tool", t.Name, "tier", string(t.Tier), "outcome", string(outcome), "trace_id", traceID(ctx))
	return outcome
}

// approvedHandler is the tool's handler on its own family's deps (spec
// 0013, AC-4); ok is false for a tool that never runs behind an approval.
func (d *Dispatcher) approvedHandler(t *Tool) (func(context.Context, Input) (Result, error), bool) {
	switch h := t.Handler.(type) {
	case MFAHandler:
		if d.sys.MFA == nil {
			return nil, false
		}
		return func(ctx context.Context, in Input) (Result, error) { return h(ctx, d.mfaDeps(), in) }, true
	case GroupHandler:
		return func(ctx context.Context, in Input) (Result, error) { return h(ctx, d.groupDeps(), in) }, true
	}
	return nil, false
}

// executable repeats what the approve's SQL guard decided: approved, by a
// listed decider who is not the requester (by the kind's own key, spec 0013
// AC-8), with the clicker's Slack id recorded, before the expiry. A row that
// fails it never runs, whoever calls Execute.
func executable(a queries.Approval) bool {
	by := a.DecidedBy.String
	return a.Status == "approved" && by != "" && a.DeciderSlackID.String != "" && !audit.IsRequester(a, by) &&
		slices.Contains(a.AllowedApprovers, by) && a.DecidedAt.Valid && !a.DecidedAt.Time.After(a.ExpiresAt)
}

// mark ends the approval as executed or failed. When no approved row is
// left (an abandon got there first), the abandon's escalation stands.
func (d *Dispatcher) mark(ctx context.Context, a queries.Approval, ok bool) {
	found, err := d.sys.Recorder.MarkApproval(recordCtx(ctx), a.ID, ok)
	switch {
	case err != nil:
		d.logger.ErrorContext(ctx, "audit_unavailable")
	case !found:
		d.logger.WarnContext(ctx, "approval_mark_lost", "approval_id", a.ID.String())
	}
}

// TicketURL is the browser link to a ticket, empty without ZAMMAD_PUBLIC_URL
// or a ticket (the approval view, spec 0012 AC-13).
func (d *Dispatcher) TicketURL(id int64) string {
	if id == 0 {
		return ""
	}
	return d.sys.Helpdesk.ticketURL(int(id))
}
