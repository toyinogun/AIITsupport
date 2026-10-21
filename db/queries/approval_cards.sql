-- Approval cards (spec 0012). Only noord_agent writes them. Every move is a
-- guarded UPDATE: zero rows back means another worker or tick got there first.

-- name: InsertApprovalCard :execrows
-- InsertApprovalCard returns 1 exactly when this caller created the row and
-- so owes the first post.
-- A group owner card has no channel until its first DM post (spec 0013).
INSERT INTO approval_cards (approval_id, conversation_id, tool_name, slack_channel_id, expires_at)
VALUES (@approval_id, @conversation_id, @tool_name, sqlc.narg(slack_channel_id), @expires_at)
ON CONFLICT (approval_id) DO NOTHING;

-- name: AdoptApprovalCard :execrows
-- AdoptApprovalCard gives an approval whose pending result never reached the
-- agent its card row (spec 0012, AC-14), when its thread is a known
-- conversation. Zero rows: the row exists already, or the thread is unknown.
INSERT INTO approval_cards (approval_id, conversation_id, tool_name, slack_channel_id, expires_at)
SELECT @approval_id, c.id, @tool_name, sqlc.narg(card_channel_id), @expires_at
FROM conversations AS c
WHERE c.slack_channel_id = @thread_channel_id AND c.slack_thread_ts = @thread_ts
ON CONFLICT (approval_id) DO NOTHING;

-- name: RecordCardPostAttempt :execrows
-- RecordCardPostAttempt claims one post attempt before it is sent: only while
-- no post has succeeded, under the attempt cap, and spaced from the last
-- attempt, so a slow post is never doubled by the tick.
UPDATE approval_cards
SET post_attempts = post_attempts + 1, last_post_at = now()
WHERE approval_id = @approval_id AND card_ts IS NULL AND settled_at IS NULL
  AND post_attempts < @max_attempts::int
  AND (last_post_at IS NULL OR last_post_at < now() - make_interval(secs => @spacing_seconds::float8));

-- name: SetApprovalCardPosted :execrows
-- SetApprovalCardPosted stores the first successful post: its ts and, for a
-- group owner card, the DM channel and the owner's Slack id (spec 0013,
-- AC-10). A value already set is kept.
UPDATE approval_cards
SET card_ts = @card_ts::text,
    slack_channel_id = coalesce(slack_channel_id, @slack_channel_id::text),
    approver_slack_id = coalesce(approver_slack_id, sqlc.narg(approver_slack_id)::text)
WHERE approval_id = @approval_id AND card_ts IS NULL;

-- name: GetApprovalCard :one
SELECT * FROM approval_cards WHERE approval_id = @approval_id;

-- name: ListUnsettledApprovalCards :many
SELECT * FROM approval_cards
WHERE settled_at IS NULL
ORDER BY created_at
LIMIT @max_rows;

-- name: SettleApprovalCard :execrows
-- SettleApprovalCard records the outcome once; zero rows means another settle won.
UPDATE approval_cards SET settled_at = now(), outcome = @outcome::text
WHERE approval_id = @approval_id AND settled_at IS NULL;

-- name: ApprovalCardIDsAmong :many
-- ApprovalCardIDsAmong is which of these approvals already have a card row,
-- so the adopt step inserts only for the rest (spec 0012, AC-14).
SELECT approval_id FROM approval_cards WHERE approval_id = ANY (@approval_ids::uuid[]);
