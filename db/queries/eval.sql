-- Eval runs and results (spec 0003). Only noord_eval writes them.

-- name: CreateEvalRun :one
INSERT INTO eval_runs (label, git_sha, model, ticket_set_hash)
VALUES (sqlc.narg(label), @git_sha, @model, @ticket_set_hash)
RETURNING *;

-- name: FinishEvalRun :one
-- FinishEvalRun closes a running run once; no rows back means it was not running.
UPDATE eval_runs
SET status = @status::text, summary = sqlc.narg(summary)::jsonb, finished_at = now()
WHERE id = @id AND status = 'running'
RETURNING *;

-- name: InsertEvalResult :exec
INSERT INTO eval_results (
    run_id, ticket_key, expected_outcome, actual_outcome, expected_tools, actual_tools,
    passed, unsafe_action, wrong_action, duration_ms, input_tokens, output_tokens,
    cost_microusd, slack_channel_id, slack_thread_ts
) VALUES (
    @run_id, @ticket_key, @expected_outcome, sqlc.narg(actual_outcome),
    @expected_tools::text[], @actual_tools::text[],
    @passed, @unsafe_action, @wrong_action, @duration_ms, @input_tokens, @output_tokens,
    @cost_microusd, sqlc.narg(slack_channel_id), sqlc.narg(slack_thread_ts)
);

-- name: ListEvalResults :many
SELECT * FROM eval_results WHERE run_id = @run_id ORDER BY ticket_key;

-- name: CompareEvalRuns :many
-- CompareEvalRuns lines up each summary metric of two labelled runs. A metric
-- present in only one run, or a missing label, gives no row.
SELECT
    a.key::text AS metric,
    (a.value #>> '{}')::float8 AS value_a,
    (b.value #>> '{}')::float8 AS value_b
FROM eval_runs AS ra
CROSS JOIN LATERAL jsonb_each(ra.summary -> 'metrics') AS a
JOIN eval_runs AS rb ON rb.label = @label_b::text
CROSS JOIN LATERAL jsonb_each(rb.summary -> 'metrics') AS b
WHERE ra.label = @label_a::text AND a.key = b.key
ORDER BY metric;
