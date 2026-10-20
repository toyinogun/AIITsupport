-- Conversations and their messages (spec 0003). Only noord_agent writes them.

-- name: UpsertConversation :one
INSERT INTO conversations (slack_channel_id, slack_thread_ts, requester_slack_id)
VALUES (@slack_channel_id, @slack_thread_ts, @requester_slack_id)
ON CONFLICT (slack_channel_id, slack_thread_ts) DO UPDATE SET updated_at = now()
RETURNING *;

-- name: GetConversationByThread :one
SELECT * FROM conversations
WHERE slack_channel_id = @slack_channel_id AND slack_thread_ts = @slack_thread_ts;

-- name: GetConversation :one
SELECT * FROM conversations WHERE id = @id;

-- name: SetConversationStatus :one
-- SetConversationStatus changes only an open conversation. It may keep it open
-- while filling in the account or ticket id; leaving open sets closed_at.
UPDATE conversations
SET status = @status::text,
    directory_account = COALESCE(sqlc.narg(directory_account)::text, directory_account),
    zammad_ticket_id = COALESCE(sqlc.narg(zammad_ticket_id)::bigint, zammad_ticket_id),
    closed_at = CASE WHEN @status::text <> 'open' THEN now() END,
    updated_at = now()
WHERE id = @id AND status = 'open'
RETURNING *;

-- name: SetConversationTicket :exec
-- SetConversationTicket fills in the ticket id the restart sweep created
-- after it had already claimed the conversation (spec 0008, AC-14). It never
-- replaces an id that is already stored.
UPDATE conversations
SET zammad_ticket_id = @zammad_ticket_id, updated_at = now()
WHERE id = @id AND zammad_ticket_id IS NULL;

-- name: AppendMessage :one
-- AppendMessage takes the next seq from the conversation's counter; the row
-- lock serialises concurrent appends, so seq stays gapless. No rows back
-- means the conversation does not exist.
WITH next AS (
    UPDATE conversations
    SET last_seq = last_seq + 1, updated_at = now()
    WHERE conversations.id = @conversation_id
    RETURNING conversations.id AS conversation_id, conversations.last_seq
)
INSERT INTO conversation_messages (conversation_id, seq, role, content)
SELECT next.conversation_id, next.last_seq, @role::text, @content::jsonb FROM next
RETURNING *;

-- name: ListMessages :many
SELECT * FROM conversation_messages
WHERE conversation_id = @conversation_id
ORDER BY seq;

-- name: PurgeMessageContentBefore :execrows
-- PurgeMessageContentBefore clears content of conversations closed before the
-- cutoff. Open conversations are never touched.
UPDATE conversation_messages AS m
SET content = NULL, content_purged_at = now()
FROM conversations AS c
WHERE m.conversation_id = c.id
  AND c.status <> 'open' AND c.closed_at < @cutoff::timestamptz
  AND m.content IS NOT NULL;

-- name: CloseIdleConversationsBefore :execrows
-- CloseIdleConversationsBefore ends open conversations with no activity
-- since the cutoff (spec 0010, AC-8): they become resolved, closed at their
-- last activity, so the content purge counts from then. updated_at stays.
UPDATE conversations
SET status = 'resolved', closed_at = updated_at
WHERE status = 'open' AND updated_at < @cutoff::timestamptz;

-- name: CountRecentConversations :one
-- CountRecentConversations is what AGENT_CONVERSATIONS_PER_HOUR is checked
-- against (spec 0008, AC-19).
SELECT count(*)::bigint AS conversations
FROM conversations
WHERE requester_slack_id = @requester_slack_id AND created_at > @since::timestamptz;

-- name: ListInterruptedConversations :many
-- ListInterruptedConversations finds open conversations a turn stopped in the
-- middle of (spec 0008, AC-14): the last stored message is the user's, or an
-- assistant message holding a tool_use block. Open conversations are never
-- purged, so content is present.
SELECT c.*
FROM conversations AS c
JOIN conversation_messages AS m ON m.conversation_id = c.id AND m.seq = c.last_seq
WHERE c.status = 'open'
  AND c.updated_at < @stale_before::timestamptz
  AND (m.role = 'user' OR m.content @> '[{"type": "tool_use"}]'::jsonb)
ORDER BY c.id;
