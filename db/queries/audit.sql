-- Audit log (spec 0003). Only noord_toolserver writes it, through internal/audit.

-- name: InsertAuditIntent :one
-- InsertAuditIntent records a call before it acts; call_id comes from the default.
INSERT INTO audit_log (
    kind, tool_name, risk_tier, requester_slack_id, requester_account,
    slack_channel_id, slack_thread_ts, zammad_ticket_id, target, arguments,
    approval_id, approver_slack_id, trace_id
) VALUES (
    'intent', @tool_name, @risk_tier, @requester_slack_id, sqlc.narg(requester_account),
    sqlc.narg(slack_channel_id), sqlc.narg(slack_thread_ts), sqlc.narg(zammad_ticket_id),
    sqlc.narg(target), sqlc.narg(arguments),
    sqlc.narg(approval_id), sqlc.narg(approver_slack_id), sqlc.narg(trace_id)
)
RETURNING id, call_id;

-- name: InsertAuditOutcome :one
-- InsertAuditOutcome copies the identity columns from the call's intent row,
-- so it inserts nothing (no rows) when that intent does not exist. A given
-- approval_id wins over the intent's (Defer creates the approval after the intent).
INSERT INTO audit_log (
    call_id, kind, tool_name, risk_tier, requester_slack_id, requester_account,
    slack_channel_id, slack_thread_ts, zammad_ticket_id, target,
    approval_id, approver_slack_id, trace_id, outcome, result, error
)
SELECT
    i.call_id, 'outcome', i.tool_name, i.risk_tier, i.requester_slack_id, i.requester_account,
    i.slack_channel_id, i.slack_thread_ts, i.zammad_ticket_id, i.target,
    COALESCE(sqlc.narg(approval_id)::uuid, i.approval_id), i.approver_slack_id, i.trace_id,
    @outcome::text, sqlc.narg(result)::jsonb, sqlc.narg(error)::text
FROM audit_log AS i
WHERE i.call_id = @call_id AND i.kind = 'intent'
RETURNING id;

-- name: ListAuditByThread :many
SELECT * FROM audit_log
WHERE slack_channel_id = @slack_channel_id AND slack_thread_ts = @slack_thread_ts
ORDER BY id;

-- name: ListUnconfirmedCalls :many
-- ListUnconfirmedCalls finds intents with no outcome: the action's result is unknown.
SELECT i.* FROM audit_log AS i
WHERE i.kind = 'intent' AND i.occurred_at < @older_than
  AND NOT EXISTS (
      SELECT 1 FROM audit_log AS o WHERE o.call_id = i.call_id AND o.kind = 'outcome'
  )
ORDER BY i.id
LIMIT @max_rows;
