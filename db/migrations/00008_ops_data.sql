-- Observability data (spec 0016): the conversation outcome, agent failures,
-- the kill switch state, and the `ops` view layer that Grafana reads through
-- the view only role noord_grafana.

-- +goose Up

-- noord_grafana is created by db/roles.sql (CloudNativePG in the cluster),
-- never here; say so plainly when it is missing.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'noord_grafana') THEN
        RAISE EXCEPTION 'missing database role: noord_grafana'
            USING HINT = 'run make db-roles (db/roles.sql) as a superuser first, or wait for the managed role in the cluster';
    END IF;
END
$$;
-- +goose StatementEnd

-- AC-6: how a conversation ended. Null exactly while it is open.
ALTER TABLE conversations ADD COLUMN outcome text;

-- An old pod still closes a conversation with no outcome during a rolling
-- deploy; fill it from the status. New code always sets the outcome itself.
-- +goose StatementBegin
CREATE FUNCTION conversations_default_outcome() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status <> 'open' AND NEW.outcome IS NULL THEN
        NEW.outcome := NEW.status;
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER conversations_default_outcome
    BEFORE INSERT OR UPDATE ON conversations
    FOR EACH ROW EXECUTE FUNCTION conversations_default_outcome();

-- An idle close from before this spec cannot be told apart and stays resolved.
UPDATE conversations SET outcome = status WHERE status <> 'open';

ALTER TABLE conversations ADD CONSTRAINT conversations_outcome_pairing CHECK (
    (status = 'open' AND outcome IS NULL)
    OR (status = 'escalated' AND outcome IN ('escalated', 'refused'))
    OR (status = 'resolved' AND outcome IN ('resolved', 'abandoned')));

GRANT UPDATE (outcome) ON conversations TO noord_agent;

-- AC-7: agent failures the dashboard can count. No error text, kept on purpose.
CREATE TABLE agent_failures (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id bigint REFERENCES conversations (id),
    stage           text        NOT NULL CHECK (stage IN ('llm', 'tool', 'slack', 'sweep')),
    cause           text        NOT NULL CHECK (cause ~ '^[a-z0-9_]{1,40}$'),
    occurred_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_failures_occurred_idx ON agent_failures (occurred_at);
GRANT INSERT ON agent_failures TO noord_agent;

-- AC-8: the kill switch as the tool server last saw it. One row.
CREATE TABLE kill_switch_state (
    id         boolean     PRIMARY KEY DEFAULT true CHECK (id),
    mode       text        NOT NULL CHECK (mode IN ('read_only', 'read_write')),
    changed_at timestamptz NOT NULL,
    checked_at timestamptz NOT NULL
);
GRANT SELECT, INSERT ON kill_switch_state TO noord_toolserver;
GRANT UPDATE (mode, changed_at, checked_at) ON kill_switch_state TO noord_toolserver;

-- AC-9: the view layer. Plain owner rights views (never security_invoker), so
-- Grafana needs no grant on the tables underneath. No column holds a person,
-- an argument, a result, or message text.
CREATE SCHEMA ops;

CREATE VIEW ops.conversation_outcomes AS
SELECT id AS conversation_id, created_at, closed_at, status, outcome,
       extract(epoch FROM closed_at - created_at)::double precision AS duration_s,
       trace_id
FROM conversations;

CREATE VIEW ops.llm_calls AS
SELECT conversation_id, created_at, cost_microusd / 1000000.0 AS cost_usd, input_tokens, output_tokens
FROM llm_usage;

CREATE VIEW ops.conversation_cost AS
SELECT conversation_id, count(*)::bigint AS model_calls, sum(cost_microusd) / 1000000.0 AS cost_usd
FROM llm_usage
GROUP BY conversation_id;

CREATE VIEW ops.approvals_open AS
SELECT id AS approval_id, tool_name, risk_tier, approver_kind, status, created_at, expires_at
FROM approvals
WHERE status = 'pending';

CREATE VIEW ops.tool_calls AS
SELECT occurred_at, tool_name, risk_tier, outcome, trace_id
FROM audit_log
WHERE kind = 'outcome';

CREATE VIEW ops.failures AS
SELECT occurred_at, stage, cause, conversation_id
FROM agent_failures;

CREATE VIEW ops.kill_switch AS
SELECT mode, changed_at, checked_at
FROM kill_switch_state;

GRANT USAGE ON SCHEMA ops TO noord_grafana;
GRANT SELECT ON ALL TABLES IN SCHEMA ops TO noord_grafana;

-- Nobody gets a temporary table by default, so the view only role cannot
-- create even that.
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    EXECUTE format('GRANT TEMPORARY ON DATABASE %I TO PUBLIC', current_database());
END
$$;
-- +goose StatementEnd
DROP SCHEMA ops CASCADE;
DROP TABLE kill_switch_state;
DROP TABLE agent_failures;
DROP TRIGGER conversations_default_outcome ON conversations;
DROP FUNCTION conversations_default_outcome();
ALTER TABLE conversations DROP CONSTRAINT conversations_outcome_pairing;
ALTER TABLE conversations DROP COLUMN outcome;
