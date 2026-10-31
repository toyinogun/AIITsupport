-- One trace per conversation (spec 0016, AC-1): the agent stores the trace id
-- on the thread's row, so every turn of the thread joins the same trace.

-- +goose Up
ALTER TABLE conversations
    ADD COLUMN trace_id text CHECK (trace_id ~ '^[0-9a-f]{32}$');

GRANT UPDATE (trace_id) ON conversations TO noord_agent;

-- +goose Down
ALTER TABLE conversations DROP COLUMN trace_id;
