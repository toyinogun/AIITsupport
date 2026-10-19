-- Password reset links (spec 0011): one row per link the tool server issues.
-- Only sha256(token) is stored; the plain token lives in the Slack message.
-- Written only by the tool server; the agent and the eval cannot see it.

-- +goose Up
CREATE TABLE reset_links (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash         bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    directory_account  text        NOT NULL,
    requester_slack_id text        NOT NULL,
    slack_channel_id   text        NOT NULL,
    slack_thread_ts    text        NOT NULL,
    zammad_ticket_id   bigint      NOT NULL,
    issue_call_id      uuid        NOT NULL,
    -- voided always means replaced by a newer link for the account.
    status             text        NOT NULL DEFAULT 'live'
        CHECK (status IN ('live', 'used', 'voided')),
    expires_at         timestamptz NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    used_at            timestamptz,
    voided_at          timestamptz,
    CHECK ((status = 'used') = (used_at IS NOT NULL)),
    CHECK ((status = 'voided') = (voided_at IS NOT NULL))
);
-- At most one live link per account.
CREATE UNIQUE INDEX reset_links_one_live_idx ON reset_links (directory_account) WHERE status = 'live';
-- The per account issue limit.
CREATE INDEX reset_links_account_created_idx ON reset_links (directory_account, created_at);
-- The retention cleanup.
CREATE INDEX reset_links_expires_idx ON reset_links (expires_at);

GRANT SELECT, INSERT, DELETE ON reset_links TO noord_toolserver;
GRANT UPDATE (status, used_at, voided_at) ON reset_links TO noord_toolserver;

-- +goose Down
DROP TABLE reset_links;
