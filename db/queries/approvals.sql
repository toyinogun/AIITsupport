-- Approvals (spec 0003). Each move is one guarded UPDATE: no rows back means
-- the move did not happen, and the caller reads the row to say why.

-- name: CreateApproval :one
INSERT INTO approvals (
    call_id, tool_name, arguments, risk_tier, requester_slack_id, requester_account,
    slack_channel_id, slack_thread_ts, zammad_ticket_id, approver_kind,
    allowed_approvers, expires_at, target
) VALUES (
    @call_id, @tool_name, @arguments, @risk_tier, @requester_slack_id, sqlc.narg(requester_account),
    @slack_channel_id, @slack_thread_ts, sqlc.narg(zammad_ticket_id), @approver_kind,
    @allowed_approvers::text[], @expires_at, sqlc.narg(target)
)
RETURNING *;

-- name: GetApproval :one
SELECT * FROM approvals WHERE id = @id;

-- name: ApproveApproval :one
-- The decider is the clicker's Slack id for kind it and the clicker's AD
-- account for kind group_owner (spec 0013, AC-8); either way it must be
-- listed and must not be the requester. decider_slack_id is the clicker.
UPDATE approvals
SET status = 'approved', decided_by = @decider::text, decider_slack_id = @decider_slack_id::text, decided_at = now()
WHERE id = @id AND status = 'pending' AND expires_at > now()
  AND @decider::text = ANY (allowed_approvers)
  AND CASE approver_kind
      WHEN 'group_owner' THEN requester_account IS DISTINCT FROM @decider::text
      ELSE @decider::text <> requester_slack_id
  END
RETURNING *;

-- name: DenyApproval :one
-- An ended approval drops the full call (the CHECK on approvals requires it).
UPDATE approvals
SET status = 'denied', decided_by = @decider::text, decider_slack_id = @decider_slack_id::text, decided_at = now(),
    arguments = NULL
WHERE id = @id AND status = 'pending' AND expires_at > now()
  AND @decider::text = ANY (allowed_approvers)
RETURNING *;

-- name: CancelApproval :one
-- CancelApproval ends a pending group owner approval whose owner cannot be
-- reached in Slack (spec 0013, AC-11). It reuses the expired status; the
-- approval.cancel audit row tells it apart from a timeout.
UPDATE approvals
SET status = 'expired', arguments = NULL
WHERE id = @id AND status = 'pending' AND approver_kind = 'group_owner'
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
-- tool and target, so Defer can create the next one (approvals_one_pending_idx).
UPDATE approvals
SET status = 'expired', arguments = NULL
WHERE requester_account = @requester_account AND tool_name = @tool_name
  AND coalesce(target, '') = coalesce(sqlc.narg(target)::text, '')
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
-- FindPendingApproval is the one pending, unexpired approval of a tool and
-- target for an account (spec 0012 AC-3, spec 0013 AC-5): the precheck
-- reuses it or refuses. A null target matches only a null target.
SELECT * FROM approvals
WHERE requester_account = @requester_account AND tool_name = @tool_name
  AND coalesce(target, '') = coalesce(sqlc.narg(target)::text, '')
  AND status = 'pending' AND expires_at > now()
ORDER BY created_at DESC
LIMIT 1;

-- name: CountPendingApprovals :one
-- CountPendingApprovals counts an account's pending, unexpired approvals of
-- a tool: the soft ACCESS_MAX_PENDING limit (spec 0013, AC-5).
SELECT count(*) FROM approvals
WHERE requester_account = @requester_account AND tool_name = @tool_name
  AND status = 'pending' AND expires_at > now();

-- name: ThreadPendingTarget :one
-- ThreadPendingTarget is the target of the thread's newest pending,
-- unexpired approval of a tool: one access request per conversation (spec
-- 0013, AC-5). No rows means none.
SELECT target FROM approvals
WHERE slack_channel_id = @slack_channel_id AND slack_thread_ts = @slack_thread_ts
  AND tool_name = @tool_name AND status = 'pending' AND expires_at > now()
ORDER BY created_at DESC
LIMIT 1;

-- name: ThreadOpenApprovalTool :one
-- ThreadOpenApprovalTool is the tool of the thread's newest pending or
-- approved medium approval: the ticket_update close guard (spec 0012 AC-24,
-- widened to every medium tool by spec 0013 AC-18). No rows means none.
SELECT tool_name FROM approvals
WHERE slack_channel_id = @slack_channel_id AND slack_thread_ts = @slack_thread_ts
  AND risk_tier = 'medium' AND status IN ('pending', 'approved')
ORDER BY created_at DESC
LIMIT 1;

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
