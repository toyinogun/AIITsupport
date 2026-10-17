# Review, feat/data-model, 2026-10-17

**Reviewed by**: claude-sonnet-5-5 (author model not stated)
**Scope**: 44 files, branch vs main (merge base 39a74ad, plus uncommitted and the untracked `db/migrations_test.go`)
**Verdict**: Changes requested

## Summary
The change adds the agent data model: one migration with eight tables, the append only audit trigger, per binary roles and grants, sqlc queries, an `internal/audit` recorder (intent and outcome rows, retries, panic and cancel handling, guarded approvals), a per test scratch database helper, and per binary DB settings. The schema, grants, trigger and approval state machine are sound, and I could not break audit integrity or the approve versus deny race. The suite passes with `-race` against the local Compose Postgres. The headline issue is that the "allow list keeps personal data out of the unerasable table" claim does not hold for key names and error text. A handful of smaller correctness and hardening items follow.

## Major
### 🟠 Untrusted free text reaches the unerasable audit_log, `internal/audit/payload.go:21`
**Problem**: `encodeKept` keeps every key name of the caller's `Args` and only replaces the value with `"[omitted]"`. The tool arguments come from the model, so a key name is attacker or model controlled text. The same table also stores `error` text taken verbatim from the action error (`audit.go:319`, up to 500 chars), from a panic value (`audit.go:177`), and from `Result.Reason`. Separately, `Defer` writes the full, unfiltered `Args` into `approvals.arguments` (`approvals.go:62-67`), a table the spec keeps as long as the audit log.
**Why it matters**: Spec invariant says keys outside the allow list "never reach the database" and the compliance section says the audit log "must never hold names, emails, or ticket text, which is why storage is an allow list". With these paths a prompt injection, a hallucinated key such as `{"john.devries@... needs reset": 1}`, or an LDAP/Zammad error string containing a DN or ticket text lands in a table that can never be erased (GDPR tradeoff the spec accepts only for allow listed data). AC-7 as worded ("stored as the string [omitted]") conflicts with the invariant, so this is partly a spec ambiguity, but the stronger invariant is the one the data processing note will promise.
**Suggested fix**: Decide which statement wins and make the code match. Safest: for omitted keys store only a count or a single fixed marker (or keep the key name only when it is in a registry known set), so no model controlled string is stored. For `error` and `Reason`, store a fixed vocabulary or a sanitised, length capped class string and keep the raw text in the (erasable) logs, or document explicitly that these columns can hold system error text. For `approvals.arguments`, require the registry (feature #9) to validate arguments against the tool schema, rejecting unknown keys, before `Defer`, and note that in the spec's follow-ups. Add a test with a hostile key name.

## Minor
### 🟡 "Executes at most once" is not enforced by the model, `internal/audit/audit.go:157`, `db/queries/approvals.sql:46`
**Problem**: Nothing claims an approved approval before the action runs. `Run` with an `ApprovalID` does not check the row is `approved`, and `MarkApprovalExecuted` only guards after the action ran. Two `Run` calls with one `ApprovalID` (crash recovery, a retry, a buggy caller) both execute.
**Why it matters**: The spec lists "executes at most once" as a key invariant; for MFA reset or group changes a double run is the harm the approval flow exists to prevent. #14 owns recovery, but the data model is cheap to harden now.
**Suggested fix**: Add a claim step (an `executing` transition or a unique partial index on `audit_log (approval_id)` for execution intents) and have an execute entry point verify `status = 'approved'` and not expired before calling the action.

### 🟡 Results returned alongside an error after the transaction rolled back, `internal/audit/approvals.go:98`, `:146`, `:212`
**Problem**: `Decide` returns `d` with `Moved: true` even when writing the audit pair failed and the transaction rolled back (the approval is still `pending`). `Defer` returns a populated approval whose insert was rolled back, and `Expire` returns rows that were not expired.
**Why it matters**: A caller that checks `d.Moved` before `err`, or posts the Slack button from the returned approval, acts on state that does not exist. For approvals that is a security relevant footgun.
**Suggested fix**: Return the zero value on any error from all three functions.

### 🟡 Outcome retry treats an already committed insert as a failure, `internal/audit/audit.go:221`
**Problem**: `recordOutcome` retries every error. If an attempt times out client side after the server committed, the retry hits the `(call_id, kind)` unique violation and the final result is `ErrAuditIncomplete` plus an `audit_outcome_incomplete` error log, although the outcome row exists. Non transient errors (no intent row) are retried too.
**Why it matters**: The caller reports a false audit failure and may retry a write action that already ran.
**Suggested fix**: Treat SQLSTATE 23505 on a retry as success, and skip retries for errors that cannot be transient.

### 🟡 Expiry and decision times use the application clock, `internal/audit/approvals.go:40`, `:87`
**Problem**: `ExpiresAt` is supplied by the caller (Go clock) while every guard compares to `now()` on the database. `Defer` also does not check that `ExpiresAt` is in the future or that the tier is `medium`. Spec invariant: "All timestamps come from the database clock".
**Why it matters**: Clock skew between tool server and Postgres shifts the approval window; a past or zero-ish value creates an approval that is born expired.
**Suggested fix**: Pass a duration and compute `now() + $interval` in `CreateApproval`, or at least validate `ExpiresAt` is after now and reject non medium tiers.

### 🟡 Audit settings are not validated at config load, `internal/config/config.go:98`
**Problem**: `AUDIT_OUTCOME_RETRIES`, `AUDIT_WRITE_TIMEOUT` and `AUDIT_MAX_PAYLOAD_BYTES` are only range checked in `audit.NewRecorder`, which no binary calls yet. The spec says the tool server "fails at startup" on a negative value.
**Why it matters**: Until #9 wires the recorder, a bad value passes startup silently, and the config layer (single source of truth per AGENTS.md) disagrees with the recorder about what is valid.
**Suggested fix**: Validate the ranges in `LoadToolServer` and add config tests, or call `NewRecorder` in `cmd/toolserver` now.

### 🟡 `make dev` hands every credential to both binaries, `Makefile:14`
**Problem**: `LOAD_ENV` exports the whole `.env`, so the agent process now carries `MIGRATE_DATABASE_URL` (owner role) and `TOOLSERVER_DATABASE_URL`, and later the LDAP/Entra/Zammad secrets.
**Why it matters**: The per binary roles and the credential boundary exist so the agent cannot reach these; in local dev the agent environment defeats both, and `make dev` is the demo path.
**Suggested fix**: Start each binary with only its own variables (for example `env -i` plus a per binary allow list, or separate env files).

### 🟡 Half built test template is reused forever, `internal/db/dbtest/dbtest.go:148`
**Problem**: The template database is created, then migrated. If the process is killed between those steps (Ctrl-C, CI timeout), a partially migrated database with the final hashed name remains and `ensureTemplate` returns it as `exists`.
**Why it matters**: Every later DB test fails with confusing missing-table errors until someone drops the database by hand.
**Suggested fix**: Migrate under a temporary name and rename on success, or verify the goose version equals the latest before reusing.

### 🟡 Test gaps around grants, rollback atomicity and the role guard
**Problem**: (1) `grants_test.go:24` probes one locked column per table, so a stray `GRANT UPDATE (allowed_approvers)` or `(expires_at)` on `approvals`, or `(requester_slack_id)` coverage beyond one column, would pass; comparing `information_schema.column_privileges` to the grants table would close it. (2) AC-13's "missing role names the problem" branch of the migration guard is untested (`migrations_test.go` covers only the wrong user). (3) The "any failure changes nothing" promise of `Defer`, `Decide` and `Expire` has no real Postgres test (the fake store's `InTx` does not roll back). (4) No test for `Expire` racing `Decide`, which `SKIP LOCKED` is meant to handle.
**Why it matters**: These are the security relevant branches of the feature.
**Suggested fix**: Add the column privilege comparison, a rollback test (force the outcome insert to fail in `Defer`), and an expire/decide race test; the missing-role guard can be tested by exercising the `DO` block's logic against a role list in a scratch cluster or accepted as manual.

### 🟡 `CompareEvalRuns` fails the whole query on a malformed metric, `db/queries/eval.sql:35`
**Problem**: `summary` is unvalidated jsonb. A non numeric or null metric value makes the `float8` cast or the scan into `float64` fail for every metric, and a non object `metrics` makes `jsonb_each` error.
**Why it matters**: One bad summary blocks the v1 versus final comparison, the deliverable of #20.
**Suggested fix**: Add a CHECK that `summary -> 'metrics'` is an object of numbers (or filter with `jsonb_typeof(value) = 'number'` in the join).

## Nits
- ⚪ `internal/audit/audit.go:264`, `inTx` wraps `ErrInvalidCall` from inside a closure as `ErrAuditUnavailable`; exempt it like `ErrApprovalNotFound`.
- ⚪ `internal/audit/approvals.go:194`, `Expire` runs `limit` pairs inside one `AUDIT_WRITE_TIMEOUT` transaction; a large limit can time out and roll back forever. Document or cap the limit.
- ⚪ `internal/audit/audit.go:178`, re-panicking with `panic(p)` drops the original stack; log it at error level (without the value) before re-panicking.
- ⚪ `db/migrations_test.go:41`, scratch database names embed the PID and test name; they stay under 63 characters today but a longer test name will break the connection.
- ⚪ `db/roles.sql:16`, creates login roles with a known password on whatever cluster runs it; add a header warning that it is dev only.
- ⚪ `.golangci.yml`, consider a depguard rule keeping `cmd/agent` from importing `internal/audit` (the package "only the tool server" uses), mirroring the credential boundary.

## Strengths
- The audit integrity design is correct and well tested: column level grants, a trigger that stops the owner too (55000), and a test that proves both layers with the real roles.
- `InsertAuditOutcome` as `INSERT ... SELECT` from the intent makes "outcome without intent" impossible and copies identity so callers cannot drift.
- Approval transitions are single guarded `UPDATE`s with CHECKs backing them; the approve versus deny race, self approval, late click and one approval per call are all tested on a real database as `noord_toolserver`.
- `AppendMessage` as one CTE statement gives gapless `seq` under concurrency, with a concurrent test.
- `Run` handles cancel, panic and `Goexit` and logs only the call id; the dbtest helper (advisory lock, hashed template, per role pools) makes DB tests parallel safe.

## Test coverage
Strong for the headline paths: unit tests on a fake store cover intent first ordering, no action on intent failure, retries and backoff cap, cancel, panic, allow list, truncation and read tier; Postgres tests cover roles, trigger, approvals and the conversation, usage, event and eval queries. Gaps are listed in the Minor finding on test gaps: single column grant probes, the untested missing-role guard branch, no real rollback test for `Defer`/`Decide`/`Expire`, and no `Expire` versus `Decide` race. Suite result: `TEST_DATABASE_URL=... go test -race -count=1 ./...` passes on all packages.
