# Verify: Data model · spec 0003 · updated 2026-10-17
_Steps derived from spec 0003 acceptance criteria (AC-1 to AC-15) and its Value sourcing table. `/check verify` runs these; `/test` locks the durable ones._

Set `SU=postgres://noord:noord-dev@localhost:55432/noord?sslmode=disable` (the Compose superuser) and `OWNER=postgres://noord_owner:noord-dev@localhost:55432/noord?sslmode=disable`.

## Commands
- [ ] On an existing Compose volume, run `make db-roles` → the four roles exist (`select rolname from pg_roles where rolname like 'noord_%'`) and `goose_db_version` is owned by `noord_owner` → AC-13
- [ ] With an `.env` that has no `MIGRATE_DATABASE_URL`, run `make migrate` → it stops with "MIGRATE_DATABASE_URL: is not set; copy the database lines from .env.example into .env" → AC-13, AC-14
- [ ] `go tool goose -dir db/migrations postgres "$SU" up` → fails with "run migrations as noord_owner, not noord" → AC-13
- [ ] `go tool goose -dir db/migrations postgres "$OWNER" up` → "successfully migrated database to version: 2"; nine tables in `public`, all owned by `noord_owner`; two triggers on `audit_log` → AC-1
- [ ] `goose … "$OWNER" down-to 1` → no app tables left; `up` again → version 2 → AC-1
- [ ] Insert one `audit_log` row as `noord_toolserver`; then `UPDATE audit_log SET target = 'x'`, `DELETE FROM audit_log`, and `TRUNCATE audit_log` as `noord_toolserver` → each fails with SQLSTATE 42501 → AC-3
- [ ] The same three statements as `noord_owner` → each fails with SQLSTATE 55000 "audit_log is append only", and the row is still there → AC-3
- [ ] As `noord_agent`, `SELECT count(*) FROM audit_log` and `FROM approvals` → permission denied; as `noord_eval`, `UPDATE eval_results SET passed = passed WHERE false` → permission denied → AC-4
- [ ] `TEST_DATABASE_URL="$SU" go test -race -count=1 -v ./internal/audit/ ./internal/db/ ./cmd/...` → every test passes and none reports SKIP → AC-2 to AC-12, AC-15
- [ ] Copy the new database lines from `.env.example` into `.env`, run `make dev` → both binaries log "http server listening", and `curl localhost:18080/healthz` and `curl localhost:18081/healthz` return 200 → AC-14
- [ ] `make lint` → 0 issues; `make gen` twice → no change in `internal/db/queries`; `make tidy-check` → clean; after commit, `make gen-check` → clean → AC-14
- [ ] After pushing, CI's `test` job runs without a goose step and `ci-ok` is green → AC-13

## Value sourcing (one step per source, at the edge that breaks if it is wrong)
- [ ] Run a call whose intent has requester, account, thread, ticket, and target set; the outcome row carries the same values although `InsertAuditOutcome` was given only `call_id` and the outcome → AC-2 (outcome identity copied from the intent)
- [ ] Defer a call with `allowed_approvers = {U02}` and approve as `U02`: the approval's `decided_by` and the `approval.approve` row's `approver_slack_id` are both `U02`; approving as the requester instead records `denied_policy` "requester cannot approve" → AC-8 (approver from the click)
- [ ] Defer with `expires_at` one minute in the past: approve is refused as "expired", and `Recorder.Expire` moves it to `expired` with an `approval.expire` row → AC-8 (expiry from `expires_at`)
- [ ] Store usage with 1000 input, 200 output, 3000 cache read, 500 cache write tokens: `SumConversationTokens` returns 4700, not 1200 → AC-11 (budget counts cache tokens)
- [ ] Purge with a cutoff after now: a closed conversation's content is cleared and an open one's is kept; with a cutoff before `closed_at`, nothing is cleared → AC-9 (cutoff against `closed_at`, open excluded)
- [ ] Store an argument key outside the tool's allow list (for example a ticket `body`): the row shows `"body": "[omitted]"`, never the text; a 5 KB payload shows `{"truncated": true, …}` → AC-7
- [ ] Compare two labelled eval runs where one has an extra metric: only metrics present in both come back; a missing label returns no rows → AC-12 (`summary.metrics` shape)

## Acceptance-criteria coverage
- AC-1: migrate up, down, up · AC-2: outcome identity step, `TestPgRunWritesAnIntentAndAnOutcome`, `TestPgAnOutcomeNeedsAnIntent`, `TestPgASecondOutcomeIsRejected`
- AC-3: the two trigger steps · AC-4: the role permission step, `TestEachRoleReachesOnlyItsGrantedTables`
- AC-5: `TestRunNeverCallsTheActionWhenTheIntentFails` · AC-6: `TestRunReportsAnIncompleteOutcomeAfterTheRetries`, `TestPgAnIntentWithoutOutcomeIsListedAsUnconfirmed`
- AC-7: allow list step, `TestEncodeKeptStoresOnlyAllowedKeys`, `TestReadTierStoresNoResult` · AC-8: approver and expiry steps, the `TestPg*Approv*` tests
- AC-9: purge step, conversation tests · AC-10: `TestASlackEventIsSeenOnce` · AC-11: token sum step · AC-12: eval compare step
- AC-13: roles, guard, and CI steps · AC-14: `.env`, `make dev`, lint, gen, tidy steps · AC-15: `TestRunWritesTheOutcomeWhenTheContextIsCancelled`, `TestRunRecordsAPanicThenPanicsAgain`
