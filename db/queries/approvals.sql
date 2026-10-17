-- Approvals (spec 0003). Each move is one guarded UPDATE: no rows back means
-- the move did not happen, and the caller reads the row to say why.

-- name: CreateApproval :one
INSERT INTO approvals (
    call_id, tool_name, arguments, risk_tier, requester_slack_id, requester_account,
    slack_channel_id, slack_thread_ts, zammad_ticket_id, approver_kind,
    allowed_approvers, expires_at
) VALUES (
    @call_id, @tool_name, @arguments, @risk_tier, @requester_slack_id, sqlc.narg(requester_account),
    @slack_channel_id, @slack_thread_ts, sqlc.narg(zammad_ticket_id), @approver_kind,
    @allowed_approvers::text[], @expires_at
)
RETURNING *;

-- name: GetApproval :one
SELECT * FROM approvals WHERE id = @id;

-- name: ApproveApproval :one
UPDATE approvals
SET status = 'approved', decided_by = @clicker::text, decided_at = now()
WHERE id = @id AND status = 'pending' AND expires_at > now()
  AND @clicker::text = ANY (allowed_approvers)
  AND @clicker::text <> requester_slack_id
RETURNING *;

-- name: DenyApproval :one
UPDATE approvals
SET status = 'denied', decided_by = @clicker::text, decided_at = now()
WHERE id = @id AND status = 'pending' AND expires_at > now()
  AND @clicker::text = ANY (allowed_approvers)
RETURNING *;

-- name: ExpireDueApprovals :many
UPDATE approvals
SET status = 'expired'
WHERE id IN (
    SELECT a.id FROM approvals AS a
    WHERE a.status = 'pending' AND a.expires_at <= now()
    ORDER BY a.expires_at
    LIMIT @max_rows
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: MarkApprovalExecuted :one
UPDATE approvals SET status = 'executed', executed_at = now()
WHERE id = @id AND status = 'approved'
RETURNING *;

-- name: MarkApprovalFailed :one
UPDATE approvals SET status = 'failed', executed_at = now()
WHERE id = @id AND status = 'approved'
RETURNING *;
