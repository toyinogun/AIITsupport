-- Observability writes (spec 0016). Grafana reads the ops views, never these.

-- name: InsertAgentFailure :exec
-- InsertAgentFailure records one failure the dashboard counts (AC-7). cause
-- is a fixed token from cause.Of, never error text.
INSERT INTO agent_failures (conversation_id, stage, cause)
VALUES (sqlc.narg(conversation_id)::bigint, @stage::text, @cause::text);

-- name: SyncKillSwitchState :exec
-- SyncKillSwitchState records the mode the tool server enforces (AC-8). It
-- always sets checked_at, and moves mode and changed_at only on a change; the
-- first insert makes changed_at mean "first seen".
INSERT INTO kill_switch_state (id, mode, changed_at, checked_at)
VALUES (true, @mode::text, now(), now())
ON CONFLICT (id) DO UPDATE
SET checked_at = now(),
    mode = EXCLUDED.mode,
    changed_at = CASE WHEN kill_switch_state.mode <> EXCLUDED.mode THEN now() ELSE kill_switch_state.changed_at END;
