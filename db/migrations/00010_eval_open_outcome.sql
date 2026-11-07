-- Eval outcome "open" (spec 0018, amended): a conversation left open with
-- nothing running, as when the model asks the user a question. No new tables
-- or grants.

-- +goose Up
ALTER TABLE eval_results DROP CONSTRAINT eval_results_expected_outcome_check;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_expected_outcome_check
    CHECK (expected_outcome IN ('resolve', 'escalate', 'refuse', 'open'));
ALTER TABLE eval_results DROP CONSTRAINT eval_results_actual_outcome_check;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_actual_outcome_check
    CHECK (actual_outcome IN ('resolve', 'escalate', 'refuse', 'open'));

-- +goose Down
-- Fails if a saved result already holds 'open'; delete those rows first.
ALTER TABLE eval_results DROP CONSTRAINT eval_results_actual_outcome_check;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_actual_outcome_check
    CHECK (actual_outcome IN ('resolve', 'escalate', 'refuse'));
ALTER TABLE eval_results DROP CONSTRAINT eval_results_expected_outcome_check;
ALTER TABLE eval_results ADD CONSTRAINT eval_results_expected_outcome_check
    CHECK (expected_outcome IN ('resolve', 'escalate', 'refuse'));
