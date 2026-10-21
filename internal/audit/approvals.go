package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// ApproverKind says who may approve a request.
type ApproverKind string

// Approver kinds, matching the approvals CHECK.
const (
	ApproverIT         ApproverKind = "it"
	ApproverGroupOwner ApproverKind = "group_owner"
)

// Tool names for the audit rows that record an approval's moves.
const (
	ToolApprovalApprove = "approval.approve"
	ToolApprovalDeny    = "approval.deny"
	ToolApprovalExpire  = "approval.expire"
	ToolApprovalAbandon = "approval.abandon"
	ToolApprovalCancel  = "approval.cancel"
)

// ReasonExecutionNotConfirmed is the stored reason of an approval.abandon
// pair (spec 0012, AC-12).
const ReasonExecutionNotConfirmed = "execution not confirmed"

// ReasonOwnerUnreachable is the stored reason of an approval.cancel pair
// (spec 0013, AC-11).
const ReasonOwnerUnreachable = "owner unreachable"

// ErrApprovalNotFound means no approval has that id; nothing is recorded.
var ErrApprovalNotFound = errors.New("approval not found")

// ErrApprovalPending means Defer found another pending approval of the same
// tool for the account (approvals_one_pending_idx): a request that passed
// the precheck at the same moment won. Nothing is recorded.
var ErrApprovalPending = errors.New("approval already pending")

// onePendingIndex is the unique index behind ErrApprovalPending.
const onePendingIndex = "approvals_one_pending_idx"

// ApprovalRequest says who may approve a deferred call and until when.
type ApprovalRequest struct {
	Kind ApproverKind
	// Approvers are snapshot now, so later changes don't widen them: Slack
	// ids for kind it, the group owner's AD account for kind group_owner.
	Approvers []string
	ExpiresAt time.Time
	// Target is the group a group_access_request adds to, by its canonical
	// name (spec 0013, AC-7); empty for every other tool.
	Target string
}

// Decider is who clicked: the Slack id from the interaction payload and, for
// a group_owner approval, the AD account their confirmed Slack email maps to
// (spec 0013, AC-13). Account is empty for kind it, and when no account
// matched.
type Decider struct {
	SlackID string
	Account string
}

// deciderFor is the value an approval's allowed_approvers and decided_by
// hold for this click: the AD account for kind group_owner, else the Slack id.
func deciderFor(a queries.Approval, by Decider) string {
	if a.ApproverKind == string(ApproverGroupOwner) {
		return by.Account
	}
	return by.SlackID
}

// IsRequester reports whether decider is the approval's requester, by the
// kind's own key: the AD account for group_owner, the Slack id for it (spec
// 0013, AC-8). Nobody approves their own request.
func IsRequester(a queries.Approval, decider string) bool {
	if a.ApproverKind == string(ApproverGroupOwner) {
		return decider == a.RequesterAccount.String
	}
	return decider == a.RequesterSlackID
}

// Decision is what Decide did with a click.
type Decision struct {
	Approval queries.Approval
	Moved    bool   // false when the click was refused
	Reason   string // why it was refused, when Moved is false
}

// Defer records a call that must wait for a human: in one transaction, the
// intent row, the approval holding the exact call, and an outcome row
// pending_approval that names the approval and keeps result's PendingResult
// keys. The original call_id is then closed; running the call after
// approval is a new Run with ApprovalID set.
func (r *Recorder) Defer(ctx context.Context, c Call, req ApprovalRequest, result map[string]any) (queries.Approval, error) {
	if err := validateDefer(c, req); err != nil {
		return queries.Approval{}, err
	}
	intent, err := r.intentParams(c)
	if err != nil {
		return queries.Approval{}, err
	}
	fullArgs := json.RawMessage(`{}`)
	if c.Args != nil {
		if fullArgs, err = json.Marshal(c.Args); err != nil {
			return queries.Approval{}, fmt.Errorf("%w: encode approval arguments: %w", ErrInvalidCall, err)
		}
	}

	var approval queries.Approval
	err = r.inTx(ctx, func(q Queries) error {
		if err := r.expireDueFor(ctx, q, c, req.Target); err != nil {
			return err
		}
		callID, err := r.insertIntent(ctx, q, intent)
		if err != nil {
			return err
		}
		approval, err = q.CreateApproval(ctx, queries.CreateApprovalParams{
			CallID:           callID,
			ToolName:         c.Tool,
			Arguments:        fullArgs,
			RiskTier:         string(c.Tier),
			RequesterSlackID: c.RequesterSlackID,
			RequesterAccount: text(c.RequesterAccount),
			SlackChannelID:   c.ChannelID,
			SlackThreadTs:    c.ThreadTS,
			ZammadTicketID:   optInt8(c.TicketID),
			ApproverKind:     string(req.Kind),
			AllowedApprovers: req.Approvers,
			ExpiresAt:        req.ExpiresAt,
			Target:           text(req.Target),
		})
		if isOnePendingViolation(err) {
			return ErrApprovalPending
		}
		if err != nil {
			return fmt.Errorf("create approval: %w", err)
		}
		return r.insertOutcome(ctx, q, queries.InsertAuditOutcomeParams{
			CallID:     callID,
			Outcome:    string(OutcomePendingApproval),
			ApprovalID: nullUUID(approval.ID),
			Result:     r.pendingResult(c, result),
		})
	})
	return approval, err
}

// expireDueFor expires the account's overdue pending approvals of c's tool
// and target, each with its approval.expire pair, so one the tick has not
// reached yet does not block the new approval. A call with no account has none.
func (r *Recorder) expireDueFor(ctx context.Context, q Queries, c Call, target string) error {
	if c.RequesterAccount == "" {
		return nil
	}
	expired, err := q.ExpireDueApprovalsFor(ctx, queries.ExpireDueApprovalsForParams{
		RequesterAccount: text(c.RequesterAccount), ToolName: c.Tool, Target: text(target),
	})
	if err != nil {
		return fmt.Errorf("expire the account's due approvals: %w", err)
	}
	return r.writeExpirePairs(ctx, q, expired)
}

// writeExpirePairs writes an approval.expire audit pair for each approval.
func (r *Recorder) writeExpirePairs(ctx context.Context, q Queries, expired []queries.Approval) error {
	for _, a := range expired {
		intent, err := r.intentParams(approvalCall(a, ToolApprovalExpire, ""))
		if err != nil {
			return err
		}
		if err := r.writePair(ctx, q, intent, OutcomeExpired, "", a.ID); err != nil {
			return err
		}
	}
	return nil
}

func isOnePendingViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == onePendingIndex
}

// Reuse records a repeat of a deferred call while its approval is still
// pending (spec 0012, AC-5): an intent and a pending_approval outcome naming
// the existing approval, with result's PendingResult keys. No new approval.
func (r *Recorder) Reuse(ctx context.Context, c Call, approvalID uuid.UUID, result map[string]any) error {
	if approvalID == uuid.Nil {
		return fmt.Errorf("%w: reuse needs the pending approval", ErrInvalidCall)
	}
	intent, err := r.intentParams(c)
	if err != nil {
		return err
	}
	return r.inTx(ctx, func(q Queries) error {
		callID, err := r.insertIntent(ctx, q, intent)
		if err != nil {
			return err
		}
		return r.insertOutcome(ctx, q, queries.InsertAuditOutcomeParams{
			CallID:     callID,
			Outcome:    string(OutcomePendingApproval),
			ApprovalID: nullUUID(approvalID),
			Result:     r.pendingResult(c, result),
		})
	})
}

// pendingResult encodes result through c.PendingResult (nil keeps nothing).
func (r *Recorder) pendingResult(c Call, result map[string]any) json.RawMessage {
	if len(c.PendingResult) == 0 {
		return nil
	}
	out, err := encodeKept(result, c.PendingResult, r.cfg.MaxPayloadBytes)
	if err != nil {
		return unencodableMarker()
	}
	return out
}

// PendingApproval is the pending, unexpired approval of tool and target for
// account, if any (spec 0012 AC-3, spec 0013 AC-5); target is empty for a
// tool without one. It reads under the write timeout.
func (r *Recorder) PendingApproval(ctx context.Context, account, tool, target string) (queries.Approval, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	a, err := r.store.FindPendingApproval(ctx, queries.FindPendingApprovalParams{
		RequesterAccount: text(account), ToolName: tool, Target: text(target),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return queries.Approval{}, false, nil
	}
	if err != nil {
		return queries.Approval{}, false, fmt.Errorf("%w: read pending approvals: %w", ErrAuditUnavailable, err)
	}
	return a, true, nil
}

// PendingCount counts account's pending, unexpired approvals of tool: the
// soft ACCESS_MAX_PENDING limit (spec 0013, AC-5).
func (r *Recorder) PendingCount(ctx context.Context, account, tool string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	n, err := r.store.CountPendingApprovals(ctx, queries.CountPendingApprovalsParams{
		RequesterAccount: text(account), ToolName: tool,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: count pending approvals: %w", ErrAuditUnavailable, err)
	}
	return n, nil
}

// ThreadPendingTarget is the target of the thread's newest pending,
// unexpired approval of tool; ok is false when there is none (spec 0013,
// AC-5: one access request per conversation).
func (r *Recorder) ThreadPendingTarget(ctx context.Context, tool, channelID, threadTS string) (target string, ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	t, err := r.store.ThreadPendingTarget(ctx, queries.ThreadPendingTargetParams{
		SlackChannelID: channelID, SlackThreadTs: threadTS, ToolName: tool,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%w: read the thread's pending approval: %w", ErrAuditUnavailable, err)
	}
	return t.String, true, nil
}

// Approval reads one approval; an unknown id is ErrApprovalNotFound.
func (r *Recorder) Approval(ctx context.Context, id uuid.UUID) (queries.Approval, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	a, err := r.store.GetApproval(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return queries.Approval{}, ErrApprovalNotFound
	}
	if err != nil {
		return queries.Approval{}, fmt.Errorf("%w: read approval: %w", ErrAuditUnavailable, err)
	}
	return a, nil
}

// ApprovalsSince lists up to limit approvals created at or after since,
// oldest first (spec 0012, AC-13).
func (r *Recorder) ApprovalsSince(ctx context.Context, since time.Time, limit int32) ([]queries.Approval, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	list, err := r.store.ListApprovalsSince(ctx, queries.ListApprovalsSinceParams{Since: since, MaxRows: limit})
	if err != nil {
		return nil, fmt.Errorf("%w: list approvals: %w", ErrAuditUnavailable, err)
	}
	return list, nil
}

// Execution is how an approved call ended (spec 0012, AC-13).
type Execution struct {
	Outcome Outcome
	Reason  string          // the outcome row's stored error, empty for ok
	Result  json.RawMessage // the kept StoreResult keys, nil when none
}

// ExecutionOf reads how approval a's execution ended: the newest outcome row
// naming it whose tool is a's tool or approval.abandon, leaving out the
// pending_approval row. ok is false when there is none yet.
func (r *Recorder) ExecutionOf(ctx context.Context, a queries.Approval) (Execution, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	row, err := r.store.ExecutionOutcomeForApproval(ctx, queries.ExecutionOutcomeForApprovalParams{
		ApprovalID: nullUUID(a.ID), ToolName: a.ToolName,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, false, nil
	}
	if err != nil {
		return Execution{}, false, fmt.Errorf("%w: read the execution outcome: %w", ErrAuditUnavailable, err)
	}
	return Execution{Outcome: Outcome(row.Outcome.String), Reason: row.Error.String, Result: row.Result}, true, nil
}

// MarkApproval ends an approved approval after its execution: executed when
// ok, failed otherwise. found is false when no approved row was there (an
// abandon got to it first, spec 0012 AC-11).
func (r *Recorder) MarkApproval(ctx context.Context, id uuid.UUID, ok bool) (found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	if ok {
		_, err = r.store.MarkApprovalExecuted(ctx, id)
	} else {
		_, err = r.store.MarkApprovalFailed(ctx, id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: mark approval: %w", ErrAuditUnavailable, err)
	}
	return true, nil
}

func validateDefer(c Call, req ApprovalRequest) error {
	switch {
	case req.Kind != ApproverIT && req.Kind != ApproverGroupOwner:
		return fmt.Errorf("%w: unknown approver kind %q", ErrInvalidCall, req.Kind)
	case len(req.Approvers) == 0:
		return fmt.Errorf("%w: no approvers", ErrInvalidCall)
	case req.ExpiresAt.IsZero():
		return fmt.Errorf("%w: no expiry", ErrInvalidCall)
	case req.Kind == ApproverGroupOwner && (req.Target == "" || len(req.Approvers) != 1):
		return fmt.Errorf("%w: a group owner approval needs its group and exactly one owner", ErrInvalidCall)
	case c.ChannelID == "" || c.ThreadTS == "":
		return fmt.Errorf("%w: an approval needs the Slack thread", ErrInvalidCall)
	}
	return nil
}

// Decide applies one Approve or Deny click (the clicker's Slack id from the
// interaction payload and, for a group owner approval, their AD account,
// both passed by agent code and the approval route). In one transaction it
// makes the guarded move and writes an approval.approve or approval.deny
// audit pair: ok or denied_approval when the move happened, denied_policy
// with the reason when it was refused. The pair's approver is the clicker's
// Slack id; a moved group owner decision also names the decider account as
// its target (spec 0013, AC-8). An unknown id returns ErrApprovalNotFound.
func (r *Recorder) Decide(ctx context.Context, approvalID uuid.UUID, by Decider, approve bool) (Decision, error) {
	if by.SlackID == "" {
		return Decision{}, fmt.Errorf("%w: no clicker", ErrInvalidCall)
	}
	tool, movedOutcome := ToolApprovalDeny, OutcomeDeniedApproval
	if approve {
		tool, movedOutcome = ToolApprovalApprove, OutcomeOK
	}

	var d Decision
	err := r.inTx(ctx, func(q Queries) error {
		var err error
		d, err = move(ctx, q, approvalID, by, approve)
		if err != nil {
			return err
		}
		outcome := movedOutcome
		if !d.Moved {
			outcome = OutcomeDeniedPolicy
		}
		c := approvalCall(d.Approval, tool, by.SlackID)
		if d.Moved && d.Approval.ApproverKind == string(ApproverGroupOwner) {
			c.Target = by.Account
		}
		intent, err := r.intentParams(c)
		if err != nil {
			return err
		}
		return r.writePair(ctx, q, intent, outcome, d.Reason, d.Approval.ID)
	})
	return d, err
}

// move reads the approval for its kind, which says who the decider is, then
// tries the guarded update; when that changes nothing it reads the row again
// to say why.
func move(ctx context.Context, q Queries, id uuid.UUID, by Decider, approve bool) (Decision, error) {
	a, err := q.GetApproval(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrApprovalNotFound
	}
	if err != nil {
		return Decision{}, fmt.Errorf("read approval: %w", err)
	}
	decider := deciderFor(a, by)
	if approve {
		a, err = q.ApproveApproval(ctx, queries.ApproveApprovalParams{ID: id, Decider: decider, DeciderSlackID: by.SlackID})
	} else {
		a, err = q.DenyApproval(ctx, queries.DenyApprovalParams{ID: id, Decider: decider, DeciderSlackID: by.SlackID})
	}
	if err == nil {
		return Decision{Approval: a, Moved: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, fmt.Errorf("move approval: %w", err)
	}
	if a, err = q.GetApproval(ctx, id); err != nil {
		return Decision{}, fmt.Errorf("read approval: %w", err)
	}
	return Decision{Approval: a, Reason: refusalReason(a, decider, approve)}, nil
}

// refusalReason mirrors the guards in ApproveApproval and DenyApproval.
func refusalReason(a queries.Approval, decider string, approve bool) string {
	switch {
	case a.Status == "expired":
		return "expired"
	case a.Status != "pending":
		return "already decided"
	case decider == "" || !slices.Contains(a.AllowedApprovers, decider):
		return "not an allowed approver"
	case approve && IsRequester(a, decider):
		return "requester cannot approve"
	default:
		return "expired"
	}
}

// Cancel ends a pending group owner approval whose owner cannot be reached
// in Slack (spec 0013, AC-11): in one transaction, the guarded move to
// expired and an approval.cancel audit pair, outcome expired, reason owner
// unreachable. moved is false, with nothing written, when the approval is
// not a pending group owner approval. An unknown id is ErrApprovalNotFound.
func (r *Recorder) Cancel(ctx context.Context, id uuid.UUID) (a queries.Approval, moved bool, err error) {
	err = r.inTx(ctx, func(q Queries) error {
		var err error
		a, err = q.CancelApproval(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			if a, err = q.GetApproval(ctx, id); errors.Is(err, pgx.ErrNoRows) {
				return ErrApprovalNotFound
			}
			if err != nil {
				return fmt.Errorf("read approval: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("cancel approval: %w", err)
		}
		moved = true
		intent, err := r.intentParams(approvalCall(a, ToolApprovalCancel, ""))
		if err != nil {
			return err
		}
		return r.writePair(ctx, q, intent, OutcomeExpired, ReasonOwnerUnreachable, a.ID)
	})
	return a, moved, err
}

// Cancelled reports whether approval id was cancelled as owner unreachable:
// an approval.cancel outcome names it (spec 0013, AC-11), so a settle after
// a crash still tells a cancel from a timeout.
func (r *Recorder) Cancelled(ctx context.Context, id uuid.UUID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.WriteTimeout)
	defer cancel()
	ok, err := r.store.CancelReasonForApproval(ctx, nullUUID(id))
	if err != nil {
		return false, fmt.Errorf("%w: read the cancel reason: %w", ErrAuditUnavailable, err)
	}
	return ok, nil
}

// Expire moves up to limit overdue approvals to expired and writes an
// approval.expire audit pair for each, in one transaction. POST
// /approvals/expire calls it on every agent tick (spec 0012, AC-12).
func (r *Recorder) Expire(ctx context.Context, limit int32) ([]queries.Approval, error) {
	var expired []queries.Approval
	err := r.inTx(ctx, func(q Queries) error {
		var err error
		if expired, err = q.ExpireDueApprovals(ctx, limit); err != nil {
			return fmt.Errorf("expire approvals: %w", err)
		}
		return r.writeExpirePairs(ctx, q, expired)
	})
	return expired, err
}

// Abandon moves up to limit approved approvals whose decided_at is older
// than grace to failed (spec 0012, AC-12) and writes an approval.abandon pair
// for each (approver the clicker's Slack id, target the requester account,
// or the decider account for a group owner approval, outcome error, reason
// execution not confirmed), in one transaction. It never runs
// a stored call: an execution that never confirmed is handed to a human.
func (r *Recorder) Abandon(ctx context.Context, grace time.Duration, limit int32) ([]queries.Approval, error) {
	if grace <= 0 {
		return nil, fmt.Errorf("%w: abandon needs a positive grace", ErrInvalidCall)
	}
	var abandoned []queries.Approval
	err := r.inTx(ctx, func(q Queries) error {
		var err error
		abandoned, err = q.AbandonStuckApprovals(ctx, queries.AbandonStuckApprovalsParams{
			GraceSeconds: grace.Seconds(), MaxRows: limit,
		})
		if err != nil {
			return fmt.Errorf("abandon approvals: %w", err)
		}
		for _, a := range abandoned {
			c := approvalCall(a, ToolApprovalAbandon, a.DeciderSlackID.String)
			c.Target = a.RequesterAccount.String
			if a.ApproverKind == string(ApproverGroupOwner) {
				c.Target = a.DecidedBy.String
			}
			intent, err := r.intentParams(c)
			if err != nil {
				return err
			}
			if err := r.writePair(ctx, q, intent, OutcomeError, ReasonExecutionNotConfirmed, a.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return abandoned, err
}

// approvalCall describes a move on approval a for the audit log. The stored
// argument names the tool being approved, never its arguments.
func approvalCall(a queries.Approval, tool, approver string) Call {
	return Call{
		Tool:             tool,
		Tier:             Tier(a.RiskTier),
		RequesterSlackID: a.RequesterSlackID,
		RequesterAccount: a.RequesterAccount.String,
		ChannelID:        a.SlackChannelID,
		ThreadTS:         a.SlackThreadTs,
		TicketID:         a.ZammadTicketID.Int64,
		Args:             map[string]any{"tool": a.ToolName},
		Store:            []string{"tool"},
		ApprovalID:       a.ID,
		ApproverSlackID:  approver,
	}
}
