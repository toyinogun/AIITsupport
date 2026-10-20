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
-- An ended approval drops the full call (the CHECK on approvals requires it).
UPDATE approvals
SET status = 'denied', decided_by = @clicker::text, decided_at = now(), arguments = NULL
WHERE id = @id AND status = 'pending' AND expires_at > now()
  AND @clicker::text = ANY (allowed_approvers)
RETURNING *;

-- name: ExpireDueApprovals :many
UPDATE approvals
SET status = 'expired', arguments = NULL
WHERE id IN (
    SELECT a.id FROM approvals AS a
    WHERE a.status = 'pending' AND a.expires_at <= now()
    ORDER BY a.expires_at
    LIMIT @max_rows
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ExpireDueApprovalsFor :many
-- ExpireDueApprovalsFor expires one account's overdue pending approvals of a
-- tool, so Defer can create the next one (approvals_one_pending_idx).
UPDATE approvals
SET status = 'expired', arguments = NULL
WHERE requester_account = @requester_account AND tool_name = @tool_name
  AND status = 'pending' AND expires_at <= now()
RETURNING *;

-- name: MarkApprovalExecuted :one
UPDATE approvals SET status = 'executed', executed_at = now(), arguments = NULL
WHERE id = @id AND status = 'approved'
RETURNING *;

-- name: MarkApprovalFailed :one
UPDATE approvals SET status = 'failed', executed_at = now(), arguments = NULL
WHERE id = @id AND status = 'approved'
RETURNING *;

-- name: FindPendingApproval :one
-- FindPendingApproval is the one pending, unexpired approval of a tool for an
-- account (spec 0012, AC-3): the precheck reuses it or refuses.
SELECT * FROM approvals
WHERE requester_account = @requester_account AND tool_name = @tool_name
  AND status = 'pending' AND expires_at > now()
ORDER BY created_at DESC
LIMIT 1;

-- name: ThreadHasOpenApproval :one
-- ThreadHasOpenApproval guards ticket_update closed (spec 0012, AC-24).
SELECT EXISTS (
    SELECT 1 FROM approvals
    WHERE slack_channel_id = @slack_channel_id AND slack_thread_ts = @slack_thread_ts
      AND tool_name = @tool_name AND status IN ('pending', 'approved')
);

-- name: ListApprovalsSince :many
SELECT * FROM approvals
WHERE created_at >= @since::timestamptz
ORDER BY created_at, id
LIMIT @max_rows;

-- name: AbandonStuckApprovals :many
-- AbandonStuckApprovals ends approved rows whose execution was never
-- confirmed within the grace (spec 0012, AC-12). It never runs them.
UPDATE approvals
SET status = 'failed', executed_at = now(), arguments = NULL
WHERE id IN (
    SELECT a.id FROM approvals AS a
    WHERE a.status = 'approved'
      AND a.decided_at < now() - make_interval(secs => @grace_seconds::float8)
    ORDER BY a.decided_at
    LIMIT @max_rows
    FOR UPDATE SKIP LOCKED
)
RETURNING *;
