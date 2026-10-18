# internal/audit

## Overview

The only way the tool server writes `audit_log` and `approvals` (spec 0003). `Recorder.Run` records an intent row before an action and an outcome row after it, so "no audit row, no write" is structural, not a habit each tool has to remember. The database backs it up: only `noord_toolserver` may insert audit rows, and a trigger stops every role, the owner included, from changing one.

## Key files

| File | Owns |
|---|---|
| `audit.go` | `Call`, `Result`, `Tier`, `Outcome`, `Config`, `Recorder`, `Run`, `Refuse`, the outcome retry, panic and cancel handling |
| `approvals.go` | `Defer`, `Decide`, `Expire`, and the audit pair each approval move writes |
| `payload.go` | the allow list filter (`encodeKept`, `"_omitted"` count, size cap) and `WithReason` |
| `history.go` | `CountRecentOK` and `ThreadHasOK`, the reads behind the tool server's rate limits and one ticket per thread (spec 0007); they count only `ok` outcomes and fail with `ErrAuditUnavailable` |
| `store.go` | `Store` / `Queries` interfaces and `PgStore` (pool connected as `noord_toolserver`) |
| `audit_test.go` | unit tests on a fake store |
| `audit_pg_test.go` | the same behaviors against real Postgres as `noord_toolserver` (needs `TEST_DATABASE_URL`) |

## Conventions

- Every write action runs inside `Recorder.Run`. A call that never runs (policy, identity, read only mode) goes through `Refuse`; one that needs a human goes through `Defer`.
- The requester, thread, and ticket in `Call` come from MCP `_meta` set by agent code, never from the model or a tool argument. The approver comes from the Slack click (`Decide`'s `clicker`).
- `Call.Store` and `Call.StoreResult` list the top level keys kept in the row. Every other key is dropped, name and value, and only counted as `"_omitted": n`. Never allow a key that holds free text: nested values under an allowed key are stored whole.
- The `error` column holds fixed text only. Wrap an action error with `audit.WithReason("account not found", err)` to store that reason; anything else stores `action failed`, and a panic stores `action panicked`. `Result.Reason` and `Refuse`'s reason are stored as is, so pass fixed strings, never request, directory, or ticket data.
- Each approval move is one guarded UPDATE in `db/queries/approvals.sql` plus its audit pair, in one transaction. An approval keeps its unfiltered `arguments` only while `pending` or `approved`; every query that ends it must clear them.
- Errors: `ErrAuditUnavailable` (intent not written, action not run), `ErrAuditIncomplete` (action ran, outcome not written; the intent shows up in `ListUnconfirmedCalls`), `ErrInvalidCall` (programming error), `ErrApprovalNotFound`. Log with `call_id` only: a database error can quote the row's values.

## Gotchas

- `InsertAuditOutcome` copies the identity columns from the intent row, except `approval_id`: a passed one wins (that's how `Defer`'s `pending_approval` outcome names an approval created after its intent).
- A `read` tier call stores no `result`.
- The settings (`AUDIT_OUTCOME_RETRIES`, `AUDIT_WRITE_TIMEOUT`, `AUDIT_MAX_PAYLOAD_BYTES`) are declared in `internal/config`; `NewRecorder` checks their ranges, so the tool server refuses to start on a bad value.

## Related specs

- [0003 data model](../../docs/specs/0003-data-model/index.md) (audit log, approvals, the recorder, AC-2 to AC-8 and AC-15)
- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (the history reads, AC-9 and AC-10)

_Drafted by /sync from the introducing change, worth a quick human pass._
