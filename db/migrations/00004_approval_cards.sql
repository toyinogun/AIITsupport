-- Approval cards (spec 0012): one row per approval the agent owes an IT card
-- and an outcome for. Written only by the agent. No foreign key to approvals:
-- the agent role cannot read that table, and spec 0003 avoids cross role FKs.

-- +goose Up
CREATE TABLE approval_cards (
    approval_id      uuid        PRIMARY KEY,
    conversation_id  bigint      NOT NULL REFERENCES conversations (id),
    tool_name        text        NOT NULL,
    slack_channel_id text        NOT NULL,
    -- null until a post succeeds.
    card_ts          text,
    -- at most 3 posts per card, each attempt recorded before it is sent.
    post_attempts    int         NOT NULL DEFAULT 0 CHECK (post_attempts >= 0),
    last_post_at     timestamptz,
    expires_at       timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- null means the outcome is still owed to the user and the card.
    settled_at       timestamptz,
    outcome          text CHECK (outcome IN ('executed', 'failed', 'denied', 'expired')),
    CHECK ((outcome IS NULL) = (settled_at IS NULL))
);
-- The tick reads unsettled cards, oldest first.
CREATE INDEX approval_cards_unsettled_idx ON approval_cards (created_at) WHERE settled_at IS NULL;

GRANT SELECT, INSERT ON approval_cards TO noord_agent;
GRANT UPDATE (card_ts, post_attempts, last_post_at, settled_at, outcome) ON approval_cards TO noord_agent;

-- +goose Down
DROP TABLE approval_cards;
