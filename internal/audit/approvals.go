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
)

// ErrApprovalNotFound means no approval has that id; nothing is recorded.
var ErrApprovalNotFound = errors.New("approval not found")

// ApprovalRequest says who may approve a deferred call and until when.
type ApprovalRequest struct {
	Kind      ApproverKind
	Approvers []string // Slack ids, snapshot now: later setting changes don't widen it
	ExpiresAt time.Time
}

// Decision is what Decide did with a click.
type Decision struct {
	Approval queries.Approval
	Moved    bool   // false when the click was refused
	Reason   string // why it was refused, when Moved is false
}

// Defer records a call that must wait for a human: in one transaction, the
// intent row, the approval holding the exact call, and an outcome row
// pending_approval that names the approval. The original call_id is then
// closed; running the call after approval is a new Run with ApprovalID set.
func (r *Recorder) Defer(ctx context.Context, c Call, req ApprovalRequest) (queries.Approval, error) {
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
		})
		if err != nil {
			return fmt.Errorf("create approval: %w", err)
		}
		return r.insertOutcome(ctx, q, queries.InsertAuditOutcomeParams{
			CallID:     callID,
			Outcome:    string(OutcomePendingApproval),
			ApprovalID: nullUUID(approval.ID),
		})
	})
	return approval, err
}

func validateDefer(c Call, req ApprovalRequest) error {
	switch {
	case req.Kind != ApproverIT && req.Kind != ApproverGroupOwner:
		return fmt.Errorf("%w: unknown approver kind %q", ErrInvalidCall, req.Kind)
	case len(req.Approvers) == 0:
		return fmt.Errorf("%w: no approvers", ErrInvalidCall)
	case req.ExpiresAt.IsZero():
		return fmt.Errorf("%w: no expiry", ErrInvalidCall)
	case c.ChannelID == "" || c.ThreadTS == "":
		return fmt.Errorf("%w: an approval needs the Slack thread", ErrInvalidCall)
	}
	return nil
}

// Decide applies one Approve or Deny click from clicker (the Slack id from the
// interaction payload, passed by agent code). In one transaction it makes the
// guarded move and writes an approval.approve or approval.deny audit pair: ok
// or denied_approval when the move happened, denied_policy with the reason
// when it was refused. An unknown id returns ErrApprovalNotFound.
func (r *Recorder) Decide(ctx context.Context, approvalID uuid.UUID, clicker string, approve bool) (Decision, error) {
	if clicker == "" {
		return Decision{}, fmt.Errorf("%w: no clicker", ErrInvalidCall)
	}
	tool, movedOutcome := ToolApprovalDeny, OutcomeDeniedApproval
	if approve {
		tool, movedOutcome = ToolApprovalApprove, OutcomeOK
	}

	var d Decision
	err := r.inTx(ctx, func(q Queries) error {
		var err error
		d, err = move(ctx, q, approvalID, clicker, approve)
		if err != nil {
			return err
		}
		outcome := movedOutcome
		if !d.Moved {
			outcome = OutcomeDeniedPolicy
		}
		intent, err := r.intentParams(approvalCall(d.Approval, tool, clicker))
		if err != nil {
			return err
		}
		return r.writePair(ctx, q, intent, outcome, d.Reason, d.Approval.ID)
	})
	return d, err
}

// move tries the guarded update; when it changes nothing it reads the row
// again to say why.
func move(ctx context.Context, q Queries, id uuid.UUID, clicker string, approve bool) (Decision, error) {
	var (
		a   queries.Approval
		err error
	)
	if approve {
		a, err = q.ApproveApproval(ctx, queries.ApproveApprovalParams{ID: id, Clicker: clicker})
	} else {
		a, err = q.DenyApproval(ctx, queries.DenyApprovalParams{ID: id, Clicker: clicker})
	}
	if err == nil {
		return Decision{Approval: a, Moved: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, fmt.Errorf("move approval: %w", err)
	}
	a, err = q.GetApproval(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrApprovalNotFound
	}
	if err != nil {
		return Decision{}, fmt.Errorf("read approval: %w", err)
	}
	return Decision{Approval: a, Reason: refusalReason(a, clicker, approve)}, nil
}

// refusalReason mirrors the guards in ApproveApproval and DenyApproval.
func refusalReason(a queries.Approval, clicker string, approve bool) string {
	switch {
	case a.Status != "pending":
		return "already decided"
	case !slices.Contains(a.AllowedApprovers, clicker):
		return "not an allowed approver"
	case approve && clicker == a.RequesterSlackID:
		return "requester cannot approve"
	default:
		return "expired"
	}
}

// Expire moves up to limit overdue approvals to expired and writes an
// approval.expire audit pair for each, in one transaction. Feature #14
// decides who calls it and how the agent escalates.
func (r *Recorder) Expire(ctx context.Context, limit int32) ([]queries.Approval, error) {
	var expired []queries.Approval
	err := r.inTx(ctx, func(q Queries) error {
		var err error
		if expired, err = q.ExpireDueApprovals(ctx, limit); err != nil {
			return fmt.Errorf("expire approvals: %w", err)
		}
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
	})
	return expired, err
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
