-- Eval harness (spec 0018, AC-10): repeats, the prompt and tool hashes, and one
-- row per ticket attempt. No new tables; noord_eval keeps its grants.

-- +goose Up
ALTER TABLE eval_runs
    ADD COLUMN repeats     int  NOT NULL DEFAULT 1 CHECK (repeats BETWEEN 1 AND 5),
    ADD COLUMN prompt_hash text NOT NULL DEFAULT '',
    ADD COLUMN tools_hash  text NOT NULL DEFAULT '';
-- The empty default only lets the migration apply to old rows; new rows must
-- name their hashes.
ALTER TABLE eval_runs
    ALTER COLUMN prompt_hash DROP DEFAULT,
    ALTER COLUMN tools_hash DROP DEFAULT;

ALTER TABLE eval_results
    ADD COLUMN attempt            int     NOT NULL DEFAULT 1 CHECK (attempt >= 1),
    ADD COLUMN failed_checks      text[]  NOT NULL DEFAULT '{}',
    ADD COLUMN actual_refusal_code text   CHECK (actual_refusal_code IN ('blocked', 'protected', 'not_yours')),
    ADD COLUMN approval_wait_ms   int     NOT NULL DEFAULT 0 CHECK (approval_wait_ms >= 0);

ALTER TABLE eval_results DROP CONSTRAINT eval_results_run_id_ticket_key_key;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_run_id_ticket_key_attempt_key
    UNIQUE (run_id, ticket_key, attempt);

-- +goose Down
ALTER TABLE eval_results DROP CONSTRAINT eval_results_run_id_ticket_key_attempt_key;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_run_id_ticket_key_key UNIQUE (run_id, ticket_key);
ALTER TABLE eval_results
    DROP COLUMN approval_wait_ms, DROP COLUMN actual_refusal_code,
    DROP COLUMN failed_checks, DROP COLUMN attempt;
ALTER TABLE eval_runs DROP COLUMN tools_hash, DROP COLUMN prompt_hash, DROP COLUMN repeats;
