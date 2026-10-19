-- Password reset links (spec 0011), behind internal/resetlink.Store. Every
-- time comparison uses the database's clock, never Go's.

-- name: LockAccountForIssue :exec
-- Serializes issuing for one account until the transaction ends.
SELECT pg_advisory_xact_lock(hashtextextended('reset_links:' || @directory_account::text, 0));

-- name: VoidLiveForAccount :execrows
UPDATE reset_links
SET status = 'voided', voided_at = now()
WHERE directory_account = @directory_account AND status = 'live';

-- name: InsertResetLink :one
INSERT INTO reset_links (
    token_hash, directory_account, requester_slack_id, slack_channel_id, slack_thread_ts,
    zammad_ticket_id, issue_call_id, expires_at
) VALUES (
    @token_hash, @directory_account, @requester_slack_id, @slack_channel_id, @slack_thread_ts,
    @zammad_ticket_id, @issue_call_id, now() + make_interval(secs => @ttl_seconds::double precision)
)
RETURNING *;

-- name: CountRecentForAccount :one
SELECT count(*) FROM reset_links
WHERE directory_account = @directory_account
  AND created_at > now() - make_interval(secs => @window_seconds::double precision);

-- name: GetValidResetLinkByHash :one
SELECT * FROM reset_links
WHERE token_hash = @token_hash AND status = 'live' AND expires_at > now();

-- name: ClaimResetLink :one
-- The one move from live to used; no row back means the link was not usable.
UPDATE reset_links
SET status = 'used', used_at = now()
WHERE token_hash = @token_hash AND status = 'live' AND expires_at > now()
RETURNING *;

-- name: RevertResetLinkClaim :execrows
-- Undo a claim after a failed write, only while the link is still unexpired.
UPDATE reset_links
SET status = 'live', used_at = NULL
WHERE id = @id AND status = 'used' AND used_at = @used_at AND expires_at > now();

-- name: DeleteExpiredResetLinks :execrows
DELETE FROM reset_links
WHERE expires_at < now() - make_interval(days => @retention_days::int);
