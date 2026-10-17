-- The agent owned data model (spec 0003): conversations, usage, Slack event
-- dedup, approvals, the append only audit log, and eval runs, plus the grants
-- that decide what each binary's role may touch. Runs as noord_owner; the
-- roles themselves come from db/roles.sql, never from a migration.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    missing text;
BEGIN
    IF current_user <> 'noord_owner' THEN
        RAISE EXCEPTION 'run migrations as noord_owner, not %', current_user
            USING HINT = 'point MIGRATE_DATABASE_URL at the noord_owner role (db/roles.sql creates it)';
    END IF;
    SELECT string_agg(r, ', ') INTO missing
    FROM unnest(ARRAY['noord_agent', 'noord_toolserver', 'noord_eval']) AS r
    WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = r);
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'missing database roles: %', missing
            USING HINT = 'run make db-roles (db/roles.sql) as a superuser first';
    END IF;
END
$$;
-- +goose StatementEnd

-- Agent: one row per Slack thread.
CREATE TABLE conversations (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slack_channel_id   text        NOT NULL,
    slack_thread_ts    text        NOT NULL,
    requester_slack_id text        NOT NULL,
    directory_account  text,
    zammad_ticket_id   bigint,
    status             text        NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'resolved', 'escalated')),
    last_seq           int         NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    closed_at          timestamptz,
    UNIQUE (slack_channel_id, slack_thread_ts),
    CHECK ((status = 'open') = (closed_at IS NULL))
);
CREATE INDEX conversations_status_created_idx ON conversations (status, created_at);
CREATE INDEX conversations_closed_idx ON conversations (closed_at) WHERE status <> 'open';

-- Agent: the Messages API transcript, content purgeable by feature #12.
CREATE TABLE conversation_messages (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id   bigint      NOT NULL REFERENCES conversations (id),
    seq               int         NOT NULL CHECK (seq >= 1),
    role              text        NOT NULL CHECK (role IN ('user', 'assistant')),
    content           jsonb,
    content_purged_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, seq),
    CHECK ((content IS NULL) = (content_purged_at IS NOT NULL))
);

-- Agent: one row per model call; cost in micro US dollars.
CREATE TABLE llm_usage (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id    bigint      NOT NULL REFERENCES conversations (id),
    model              text        NOT NULL,
    input_tokens       int         NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens      int         NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_read_tokens  int         NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens int         NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    cost_microusd      bigint      NOT NULL CHECK (cost_microusd >= 0),
    pricing_date       date        NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX llm_usage_conversation_idx ON llm_usage (conversation_id);

-- Agent: Slack event ids already seen, so a redelivered event is dropped.
CREATE TABLE slack_events (
    event_id    text PRIMARY KEY,
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX slack_events_received_idx ON slack_events (received_at);

-- Tool server: a medium risk call waiting for a human. The id goes into the
-- Slack button value, so it is random, never sequential.
CREATE TABLE approvals (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    call_id            uuid        NOT NULL UNIQUE,
    tool_name          text        NOT NULL,
    arguments          jsonb       NOT NULL,
    risk_tier          text        NOT NULL CHECK (risk_tier IN ('read', 'low', 'medium', 'blocked')),
    requester_slack_id text        NOT NULL,
    requester_account  text,
    slack_channel_id   text        NOT NULL,
    slack_thread_ts    text        NOT NULL,
    zammad_ticket_id   bigint,
    approver_kind      text        NOT NULL CHECK (approver_kind IN ('it', 'group_owner')),
    allowed_approvers  text[]      NOT NULL CHECK (cardinality(allowed_approvers) > 0),
    status             text        NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'executed', 'failed')),
    decided_by         text,
    decided_at         timestamptz,
    executed_at        timestamptz,
    expires_at         timestamptz NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    -- A human decision always names who and when; pending and expired never do.
    CHECK ((status IN ('approved', 'denied', 'executed', 'failed'))
        = (decided_by IS NOT NULL AND decided_at IS NOT NULL)),
    CHECK (status NOT IN ('pending', 'expired') OR (decided_by IS NULL AND decided_at IS NULL)),
    CHECK ((executed_at IS NOT NULL) = (status IN ('executed', 'failed')))
);
CREATE INDEX approvals_pending_expiry_idx ON approvals (expires_at) WHERE status = 'pending';

-- Tool server: every call, an intent row before it acts and an outcome row
-- after. Append only: see the trigger below.
CREATE TABLE audit_log (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    call_id            uuid        NOT NULL DEFAULT gen_random_uuid(),
    kind               text        NOT NULL CHECK (kind IN ('intent', 'outcome')),
    occurred_at        timestamptz NOT NULL DEFAULT clock_timestamp(),
    tool_name          text        NOT NULL,
    risk_tier          text        NOT NULL CHECK (risk_tier IN ('read', 'low', 'medium', 'blocked')),
    requester_slack_id text        NOT NULL,
    requester_account  text,
    slack_channel_id   text,
    slack_thread_ts    text,
    zammad_ticket_id   bigint,
    target             text,
    arguments          jsonb,
    result             jsonb,
    outcome            text CHECK (outcome IN (
        'ok', 'error', 'denied_policy', 'denied_identity', 'read_only',
        'pending_approval', 'denied_approval', 'expired')),
    error              text CHECK (char_length(error) <= 500),
    approval_id        uuid REFERENCES approvals (id),
    approver_slack_id  text,
    trace_id           text,
    UNIQUE (call_id, kind),
    CHECK (kind <> 'intent' OR (outcome IS NULL AND result IS NULL AND error IS NULL)),
    CHECK (kind <> 'outcome' OR (outcome IS NOT NULL AND arguments IS NULL))
);
CREATE INDEX audit_log_thread_idx ON audit_log (slack_channel_id, slack_thread_ts, id);
CREATE INDEX audit_log_occurred_idx ON audit_log (occurred_at);
CREATE INDEX audit_log_approval_idx ON audit_log (approval_id) WHERE approval_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append only' USING ERRCODE = '55000';
END
$$;
-- +goose StatementEnd

-- Blocks every role, the owner included. Only a reviewed migration that drops
-- these triggers can change an audit row.
CREATE TRIGGER audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();
CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();

-- Eval: one row per harness run; summary is {"metrics": {"<name>": <number>}}.
CREATE TABLE eval_runs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    label           text UNIQUE,
    git_sha         text        NOT NULL,
    model           text        NOT NULL,
    ticket_set_hash text        NOT NULL,
    status          text        NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'completed', 'failed')),
    summary         jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    CHECK ((status = 'running') = (finished_at IS NULL)),
    CHECK (status <> 'completed' OR summary IS NOT NULL)
);

-- Eval: one row per ticket per run.
CREATE TABLE eval_results (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id           bigint      NOT NULL REFERENCES eval_runs (id),
    ticket_key       text        NOT NULL,
    expected_outcome text        NOT NULL CHECK (expected_outcome IN ('resolve', 'escalate', 'refuse')),
    actual_outcome   text CHECK (actual_outcome IN ('resolve', 'escalate', 'refuse')),
    expected_tools   text[]      NOT NULL DEFAULT '{}',
    actual_tools     text[]      NOT NULL DEFAULT '{}',
    passed           boolean     NOT NULL,
    unsafe_action    boolean     NOT NULL,
    wrong_action     boolean     NOT NULL,
    duration_ms      int         NOT NULL CHECK (duration_ms >= 0),
    input_tokens     int         NOT NULL CHECK (input_tokens >= 0),
    output_tokens    int         NOT NULL CHECK (output_tokens >= 0),
    cost_microusd    bigint      NOT NULL CHECK (cost_microusd >= 0),
    slack_channel_id text,
    slack_thread_ts  text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, ticket_key)
);

-- Grants (spec 0003, Security model). New tables carry no PUBLIC grants, and
-- identity columns need no sequence grants, so this is the whole list.
GRANT SELECT, INSERT ON conversations TO noord_agent;
GRANT UPDATE (status, directory_account, zammad_ticket_id, last_seq, updated_at, closed_at)
    ON conversations TO noord_agent;
GRANT SELECT, INSERT ON conversation_messages TO noord_agent;
GRANT UPDATE (content, content_purged_at) ON conversation_messages TO noord_agent;
GRANT SELECT, INSERT ON llm_usage TO noord_agent;
GRANT SELECT, INSERT, DELETE ON slack_events TO noord_agent;

GRANT SELECT, INSERT ON approvals TO noord_toolserver;
GRANT UPDATE (status, decided_by, decided_at, executed_at) ON approvals TO noord_toolserver;
GRANT SELECT, INSERT ON audit_log TO noord_toolserver;

GRANT SELECT ON audit_log, conversations, llm_usage TO noord_eval;
GRANT SELECT, INSERT ON eval_runs TO noord_eval;
GRANT UPDATE (status, summary, finished_at) ON eval_runs TO noord_eval;
GRANT SELECT, INSERT ON eval_results TO noord_eval;

-- +goose Down
DROP TABLE eval_results;
DROP TABLE eval_runs;
DROP TABLE audit_log;
DROP FUNCTION audit_log_append_only();
DROP TABLE approvals;
DROP TABLE slack_events;
DROP TABLE llm_usage;
DROP TABLE conversation_messages;
DROP TABLE conversations;
