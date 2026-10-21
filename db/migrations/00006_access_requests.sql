-- Access requests via group owner (spec 0013). An approval can now name the
-- group it adds to (target), and records the clicker's Slack id apart from
-- the decider, which is an AD account for a group owner approval. A group
-- owner card has no channel until its first DM post, and remembers the
-- owner's Slack id so only that person's click counts.

-- +goose Up
ALTER TABLE approvals ADD COLUMN target text;
-- A group name is not personal data, so target is never cleared.
ALTER TABLE approvals ADD CONSTRAINT approvals_group_target_check
    CHECK (tool_name <> 'group_access_request' OR target IS NOT NULL);

ALTER TABLE approvals ADD COLUMN decider_slack_id text;
-- Every decision so far came from an IT approval, decided by a Slack id.
UPDATE approvals SET decider_slack_id = decided_by WHERE decided_by IS NOT NULL;
ALTER TABLE approvals ADD CONSTRAINT approvals_decider_slack_check
    CHECK ((decided_by IS NULL) = (decider_slack_id IS NULL));

-- At most one pending approval per account, tool, and group.
DROP INDEX approvals_one_pending_idx;
CREATE UNIQUE INDEX approvals_one_pending_idx ON approvals (requester_account, tool_name, coalesce(target, ''))
    WHERE status = 'pending';

GRANT UPDATE (decider_slack_id) ON approvals TO noord_toolserver;

ALTER TABLE approval_cards ALTER COLUMN slack_channel_id DROP NOT NULL;
-- The owner's Slack id, found by email at the first successful DM post.
ALTER TABLE approval_cards ADD COLUMN approver_slack_id text;
ALTER TABLE approval_cards ADD CONSTRAINT approval_cards_posted_channel_check
    CHECK (card_ts IS NULL OR slack_channel_id IS NOT NULL);

GRANT UPDATE (slack_channel_id, approver_slack_id) ON approval_cards TO noord_agent;

-- +goose Down
REVOKE UPDATE (slack_channel_id, approver_slack_id) ON approval_cards FROM noord_agent;
ALTER TABLE approval_cards DROP CONSTRAINT approval_cards_posted_channel_check;
ALTER TABLE approval_cards DROP COLUMN approver_slack_id;
-- A group owner card that never posted has no channel to keep.
DELETE FROM approval_cards WHERE slack_channel_id IS NULL;
ALTER TABLE approval_cards ALTER COLUMN slack_channel_id SET NOT NULL;

REVOKE UPDATE (decider_slack_id) ON approvals FROM noord_toolserver;
DROP INDEX approvals_one_pending_idx;
CREATE UNIQUE INDEX approvals_one_pending_idx ON approvals (requester_account, tool_name)
    WHERE status = 'pending';
ALTER TABLE approvals DROP CONSTRAINT approvals_decider_slack_check;
ALTER TABLE approvals DROP COLUMN decider_slack_id;
ALTER TABLE approvals DROP CONSTRAINT approvals_group_target_check;
ALTER TABLE approvals DROP COLUMN target;
