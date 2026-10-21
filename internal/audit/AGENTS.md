# internal/audit

## Overview

The only way the tool server writes `audit_log` and `approvals` (spec 0003). `Recorder.Run` records an intent row before an action and an outcome row after it, so "no audit row, no write" is structural, not a habit each tool has to remember. The database backs it up: only `noord_toolserver` may insert audit rows, and a trigger stops every role, the owner included, from changing one.

## Key files

| File | Owns |
|---|---|
| `audit.go` | `Call`, `Result`, `Tier`, `Outcome`, `Config`, `Recorder`, `Run`, `Refuse`, `CallIDFrom` (the intent's call id inside a `Run` action), the outcome retry, panic and cancel handling |
| `approvals.go` | `Defer`, `Reuse`, `Decide` (by a `Decider`: the clicker's Slack id, plus the AD account for a group owner approval), `Cancel` and `Cancelled` (spec 0013), `Expire`, `Abandon`, `MarkApproval`, the reads (`PendingApproval`, `PendingCount`, `ThreadPendingTarget`, `Approval`, `ApprovalsSince`, `ExecutionOf`), and the audit pair each approval move writes |
| `payload.go` | the allow list filter (`encodeKept`, `"_omitted"` count, size cap) and `WithReason` |
| `history.go` | the tool server's history reads, all failing with `ErrAuditUnavailable`: `CountRecent` and `ThreadHas` take the outcome to match (`ok` for the rate limits and one ticket per thread, spec 0007; `denied_policy` for the blocked attempt count and the close guard, spec 0009), `CountRecentOK` and `ThreadHasOK` wrap them for `ok`, and `ThreadTicketID` finds the thread's ticket (spec 0009), `CountRecentReason` counts refusals by stored reason (spec 0013's `protected` in the privileged window), and `ThreadOpenApprovalTool` is the close guard for every medium tool (spec 0012, spec 0013) |
| `store.go` | `Store` / `Queries` interfaces and `PgStore` (pool connected as `noord_toolserver`) |
| `audit_test.go` | unit tests on a fake store |
| `audit_pg_test.go` | the same behaviors against real Postgres as `noord_toolserver` (needs `TEST_DATABASE_URL`) |

## Conventions

- Every write action runs inside `Recorder.Run`. A call that never runs (policy, identity, read only mode) goes through `Refuse`; one that needs a human goes through `Defer`.
- The requester, thread, and ticket in `Call` come from MCP `_meta` set by agent code, never from the model or a tool argument. The approver comes from the Slack click (`Decide`'s `clicker`).
- `Call.Store` and `Call.StoreResult` list the top level keys kept in the row. Every other key is dropped, name and value, and only counted as `"_omitted": n`. Never allow a key that holds free text: nested values under an allowed key are stored whole.
- The `error` column holds fixed text only. Wrap an action error with `audit.WithReason("account not found", err)` to store that reason; anything else stores `action failed`, and a panic stores `action panicked`. `Result.Reason` and `Refuse`'s reason are stored as is, so pass fixed strings, never request, directory, or ticket data.
- Each approval move is one guarded UPDATE in `db/queries/approvals.sql` plus its audit pair, in one transaction. An approval keeps its unfiltered `arguments` only while `pending` or `approved`; every query that ends it must clear them.
- Errors: `ErrAuditUnavailable` (intent not written, action not run), `ErrAuditIncomplete` (action ran, outcome not written; the intent shows up in `ListUnconfirmedCalls`), `ErrInvalidCall` (programming error), `ErrApprovalNotFound`, `ErrApprovalPending` (`Defer` lost the race for the account's one pending approval; nothing written). Log with `call_id` only: a database error can quote the row's values.

## Gotchas

- At most one `pending` approval per account, tool, and target (the group, empty for other tools) is enforced by `approvals_one_pending_idx` (migration 00006; per account and tool in 00005). `Defer` first expires that account's overdue pending rows, with their `approval.expire` pairs, so a row the agent's tick has not reached yet never blocks the next request.
- `InsertAuditOutcome` copies the identity columns from the intent row, except `approval_id`: a passed one wins (that's how `Defer`'s `pending_approval` outcome names an approval created after its intent).
- A `read` tier call stores no `result`.
- The settings (`AUDIT_OUTCOME_RETRIES`, `AUDIT_WRITE_TIMEOUT`, `AUDIT_MAX_PAYLOAD_BYTES`) are declared in `internal/config`; `NewRecorder` checks their ranges, so the tool server refuses to start on a bad value.

## Related specs

- [0003 data model](../../docs/specs/0003-data-model/index.md) (audit log, approvals, the recorder, AC-2 to AC-8 and AC-15)
- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (the history reads, AC-9 and AC-10)
- [0009 refusal and escalation](../../docs/specs/0009-refusal-escalation/index.md) (the outcome kind reads and `ThreadTicketID`, AC-3 to AC-6)
- [0012 MFA re-registration with approval](../../docs/specs/0012-mfa-reregistration-approval/index.md) (`Reuse`, `Abandon`, the execution reads, the one pending approval rule)
- [0013 Access requests via group owner](../../docs/specs/0013-access-requests-group-owner/index.md) (the kind aware decide, `Cancel`, the per group pending rule)

_Drafted by /sync from the introducing change, worth a quick human pass._
