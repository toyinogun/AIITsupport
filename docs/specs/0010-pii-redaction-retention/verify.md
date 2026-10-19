# Verify: PII redaction and retention · spec 0010 · updated 2026-10-19
_Steps derived from spec 0010 acceptance criteria and its Value sourcing table, after the build step 6 probes. `/check verify` runs these; `/test` locks the durable ones._

## Zammad probes (build step 6), 2026-10-19, Zammad 7.2.2

Each probe ran through `rails r` inside one transaction that rolled back, so the running Zammad kept no tickets and no job. Journal [0035](../../journal/0035-zammad-merged-tickets-have-no-close-time.md) has the story.

| Probe | Result |
|---|---|
| A merged ticket has a close time | **Failed.** `close_at` and `last_close_at` are both null on a merged ticket: Zammad sets them only for state type `closed` (`Ticket::SetsCloseTime`). |
| A ticket reopened and closed again | `close_at` keeps the first close; `last_close_at` moves to the new close. |
| A merge sets `updated_at` (cross check, 2026-10-19) | Yes: after `merge_to`, the state is `merged` and `updated_at` is the merge time (0.19 s after the merge started), so `updated_at` is a sound source for "merged N days ago". |
| The job's batch size | `Job::OBJECTS_BATCH_SIZE` is 2000: one run takes at most 2000 matches; the next run takes the rest. |
| The relative date condition takes days | Works: `{"operator": "before (relative)", "value": "30", "range": "day"}`. |
| The tag operator | `contains one`, with the value as a string (`"noord-assistant"`); an array raises `NoMethodError` in `lib/selector/sql.rb`. |
| The delete action | `perform: {"ticket.action": {"value": "delete"}}` deletes the matched tickets on `job.run(true)`. The job needs `created_by_id` and `updated_by_id` set. |
| The compose `zammad-scheduler` service runs jobs | Yes: the `Job.run` scheduler is active and ran at 17:44 UTC; `noord-retention` shows `next_run_at` at the next full hour. |

**Deviation from AC-9 and AC-10, chosen in the build session:** the condition is a nested OR. Closed tickets count from `last_close_at` (so a reclosed ticket's clock starts over), and merged tickets count from `updated_at`, the merge time (they have no close time). The spec's fallback named `last_close_at` for a failed probe; the merged branch is new. `/architect` updated AC-9 and AC-10 to match on 2026-10-19.

A second probe ran the real `seed/helpdesk/rails/retention.rb` over six tickets, then forced one run: the old closed and old merged assistant tickets were deleted; a reclosed one (first close 40 days ago, last close yesterday), a recently merged one, an old open one, and an old untagged closed one stayed. A rerun with 45 days updated the same job (one job named `noord-retention`).

## Commands

- [x] `make lint tidy-check vuln secrets` and `TEST_DATABASE_URL=postgres://noord:noord-dev@localhost:55432/noord?sslmode=disable make test` → pass (`gen-check` passes once the sqlc output is committed; it diffs against the git index) → all ACs
- [x] `go test -race -run 'TestNewRedacts|TestBuiltinKeys|TestAllowedKeys' -v ./internal/logging/` → PASS; an unknown key, a grouped key, an error, a struct, an email, a two word name, a 129 byte id, and an attribute keyed `msg` all print `[redacted]` → AC-1
- [x] `go test -race -run 'TestRuntimeKeysAreAllowListed|TestScanRejectsTheFixture' -v ./internal/logging/` → PASS; the fixture's eleven bad lines are named, `http.Error` is not → AC-2
- [x] `go test -race -run 'TestListenerLogsCauseNotErrorText' -v ./internal/slack/` and `go test -race -run 'TestServerErrorsLogAFixedLine|TestErrorLogBridgeDropsText' -v ./internal/server/` → PASS → AC-3
- [x] `go test -race -run 'TestRedactingExporter|TestErrorHandlerIsThrottled' -v ./internal/telemetry/` → PASS → AC-4
- [x] `go test -race -run 'TestCanary' -v ./internal/agent/ ./internal/mcpserver/` and `go test -race ./internal/canary/` → PASS; no seed name, email, username, Slack id, department, message, or ticket text, and no `[redacted]` → AC-5
- [x] `go test -race -run 'TestTicketRetentionDays' -v ./internal/config/` → PASS; unset gives 30, and `0`, `366`, `abc` stop all three loaders naming the setting → AC-7
- [x] `TEST_DATABASE_URL=... go test -race -run 'TestPgRetention|TestRetention' -v ./internal/agent/` → PASS; closed 31 days purged, closed 29 kept, idle 31 closed and purged in one pass, active kept, second pass 0 rows, DELETE denied (42501) → AC-8
- [x] `go test -race -run 'TestSeed|TestMarker' -v ./internal/seed/helpdesk/` → PASS; retention runs after the marker and after the token check, never on needs reset; `retention.rb` leaves the marker alone → AC-9
- [x] `set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run TestLiveRetentionScheduler -v ./internal/seed/helpdesk/` → PASS (2026-10-19, 30 s) → AC-10
  2026-10-19, after `/check review`: the test also holds a ticket whose `close_at` is past the window but whose `last_close_at` is inside it, and a ticket merged inside the window; both stay. PASS (32 s). Mutation check: with `retention.rb` reading `ticket.close_at`, the test fails (`ticket 107 gone = true, want false`); the file was then restored and the test rerun, which put the live scheduler back on `last_close_at`.

## Live, on `make dev`

- [x] Start both binaries with their stdout teed to files (`make dev 2>&1 | tee /tmp/noord-dev.log`, or each binary on its own with `| tee`). DM the bot "I'm locked out" as a seeded locked user, then, in a new thread, ask to be made Domain Admin. Grep the files for that user's display name, given name, surname, username, email, Slack user id, department, and both message texts → no match; grep for `[redacted]` → no match. Record the commands and the result here → AC-6
  Run 2026-10-19 (`/check verify`), requester `jan.devries` (locked first with `make directory-lock NAME=jan.devries`). Both binaries ran from `bin/` with `.env` loaded and `READ_ONLY=false` for this run only, each piped to its own file with `tee`. The unlock thread ended `outcome=resolved` (`account_unlock` ok; `make directory-login` then showed `locked: false`). The Domain Admin thread ended `outcome=escalated` (`privileged_access_change` `denied_policy`, then `escalate` with a ticket). Values grepped: display name, given name, surname, department (case sensitive, whole word); username, seeded email, overlay email, Slack user id (case insensitive, whole word); every user message line and both tickets' title and body lines from conversations 35 and 36 and tickets 86 and 87 (fixed strings). The check ran `jq -r '.. | strings'` over both files (134 string values), then `grep -wF` / `grep -wiF` / `grep -F` with the value lists: 0, 0, 0 hits. A stricter pass over the raw lines, keys included, any case, any substring: 0 hits. `grep -F '[redacted]'`: 0 hits. The value lists were kept in a private scratch folder and deleted after the run.
- [x] `make helpdesk-seed` → prints `retention scheduler` with `days` 30 and `tickets_matching_now`; rerun with `TICKET_RETENTION_DAYS=45` → the same line with 45, and the runbook's Rails one liner still shows one `noord-retention` job → AC-9
  Run 2026-10-19: `days` 30, `tickets_matching_now` 0; then `set -a && . ./.env && set +a && TICKET_RETENTION_DAYS=45 go run ./cmd/seed helpdesk` printed `days` 45, Rails showed one `noord-retention` job with `"value":"45"` in both branches; `make helpdesk-seed` put it back to 30. Note: `TICKET_RETENTION_DAYS=45 make helpdesk-seed` does not work while `.env` has the line `TICKET_RETENTION_DAYS=` (empty), because `LOAD_ENV` runs after the override and clears it.
- [x] Follow [the data retention runbook](../../runbooks/data-retention.md): both check commands run and print `0` (Postgres) and `matching_now` 0 (Zammad) after a run → AC-11

## Value sourcing

- [x] Redacted value: every redacted attribute prints exactly `[redacted]` (`logging.Redacted`) → AC-1 tests above
- [x] Which keys pass: add a log line with a new key in a scratch branch → `TestRuntimeKeysAreAllowListed` fails naming the file and line → AC-2
- [x] `cause`: a dedup failure with a `PgError` logs `postgres_<sqlstate>`; an auth.test 429 logs `slack_ratelimited` → AC-3
- [x] OTel error rate: five errors in a burst give one line, one more a minute later gives a second → AC-4
- [x] Cutoff: vary the window (`NewRetention(..., 29, ...)` against the DB test rows) → the 29 day conversation is purged too → AC-8
- [x] Idle close `closed_at`: equals the row's own `updated_at`, not now (the DB test's `closedIsUpdate`) → AC-8
- [x] `content_purged_at`: set by the database clock, together with `content` null → AC-8
- [x] Cadence: at start, then hourly (`RetentionEvery`); a failed step retries on the next tick, a hung query fails after 5 minutes with `deadline` → AC-8
- [x] Scheduler N, name, tag, states: from `TICKET_RETENTION_DAYS`, the constant `noord-retention`, the tag `noord-assistant`, and the `closed` and `merged` state ids (the probes above) → AC-9
- [x] M (tickets matching now): equals the job's condition counted by Zammad's selector; compare with the runbook one liner → AC-9

## Acceptance criteria coverage

- AC-1 handler tests · AC-2 scan tests · AC-3 listener and server tests · AC-4 exporter tests · AC-5 canaries · AC-6 live grep (passed 2026-10-19) · AC-7 config test · AC-8 retention unit and DB tests · AC-9 seed tests and `helpdesk-seed` · AC-10 live Zammad test · AC-11 the runbook
