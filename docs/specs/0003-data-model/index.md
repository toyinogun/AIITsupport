# 0003. Agent owned data model: append only audit, guarded approvals, per binary roles

**Date**: 2026-10-17
**Status**: In Progress

## Summary

This spec defines every record the agent keeps in its own Postgres: the audit log, pending approvals, Slack conversations and their messages, model usage and cost, Slack event deduplication, and eval runs. The audit log is append only, enforced twice: the app roles may only insert and read it, and a trigger rejects any change or delete, even from the owner. Each binary connects with its own database role, so the agent cannot write audit rows or touch approvals directly. One migration creates all of it, and a small `internal/audit` package makes "no audit row, no write" the only way the tool server can run an action, storing only the argument keys each tool allows.

## Rationale

Reasoning, the options weighed, and why: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As Noord's IT person, I want every action the agent took (or was refused) recorded with who asked, what it did, to which account, who approved it, and the result, so that I can answer for any change.
- As the engineer, I want the database itself to stop the app from editing or deleting audit rows, so that the "audit log you can trust" claim holds even if the code has a bug.
- As the engineer, I want eval runs stored, so that v1 and final can be compared metric by metric.
- As the agent, I want conversations, approvals, and seen Slack events kept in Postgres, so that a restart loses nothing.

**Acceptance criteria**:
- **AC-1**: On a fresh database where the roles exist, migration `00002_data_model.sql` run as `noord_owner` creates the eight tables below with their constraints, indexes, and the audit trigger; `goose down` to version 1 removes them cleanly.
- **AC-2**: A tool call is recorded as an intent row (before the action) and an outcome row (after it) sharing a `call_id`, together carrying requester Slack id, matched directory account, tool, risk tier, target, stored arguments, thread, ticket id, trace id, result, outcome, approval id, and approver. The outcome copies the identity columns from its intent; an outcome for a `call_id` with no intent inserts nothing, and a second intent or outcome for one `call_id` is rejected.
- **AC-3**: As `noord_toolserver`, INSERT and SELECT on `audit_log` succeed, and UPDATE, DELETE, and TRUNCATE fail with SQLSTATE `42501`. As `noord_owner`, UPDATE, DELETE (on existing rows), and TRUNCATE fail with SQLSTATE `55000` and the message `audit_log is append only`.
- **AC-4**: Each role reaches only what the grants table allows: `noord_agent` gets `42501` on any read or write of `audit_log`, `approvals`, and the eval tables; `noord_eval` can read but not write `audit_log` and cannot update `eval_results`; `noord_toolserver` cannot touch the conversation, usage, event, or eval tables.
- **AC-5**: `audit.Recorder.Run` writes the intent row before calling the action. If the intent insert fails, the action is never called and `ErrAuditUnavailable` is returned.
- **AC-6**: If the action ran but the outcome insert still fails after `AUDIT_OUTCOME_RETRIES` retries, `Run` returns the action's result together with `ErrAuditIncomplete` (joined with the action's own error, if any), and `ListUnconfirmedCalls` returns that intent.
- **AC-7**: Only the top level argument and result keys a call allows are stored; every other key is stored as the string `"[omitted]"`. A stored payload larger than `AUDIT_MAX_PAYLOAD_BYTES` is replaced by `{"truncated": true, "bytes": <n>}`. Calls with risk tier `read` store no `result`.
- **AC-8**: An approval moves only along the allowed transitions. Approve succeeds once, only for a Slack id in `allowed_approvers` that is not the requester, and only before `expires_at`; a repeat, unauthorised, self, or late click changes nothing and is audited as `denied_policy` with the reason. When approve and deny race, exactly one wins. Each approval belongs to exactly one originating `call_id`.
- **AC-9**: A conversation is unique per (`slack_channel_id`, `slack_thread_ts`): upserting the same thread returns the same row. Messages get gapless `seq` values even when appended concurrently, and come back in `seq` order. Purging content clears `content` and sets `content_purged_at` only for messages of conversations that closed before the cutoff; open conversations are never purged.
- **AC-10**: Marking a Slack `event_id` seen a second time reports it as a duplicate (zero rows affected); purging removes events older than `SLACK_EVENT_DEDUP_TTL`.
- **AC-11**: One usage row per model call can be stored; `SumConversationTokens` returns input, output, cache read, and cache write tokens summed (0 for a conversation with no calls), and `SumConversationUsage` returns each total plus cost.
- **AC-12**: An eval run with its per ticket results can be stored and finished once with a summary, and `CompareEvalRuns` returns each metric side by side for two labels (for example `v1` and `final`); a missing label returns no rows.
- **AC-13**: Roles are created outside migrations: the Compose init script and `make db-roles` locally, and the test helper for DB tests, safely when test packages run in parallel. Running the migration when a role is missing, or as any role other than `noord_owner`, fails with a clear error naming the problem. CI's `ci-ok` passes with the new setup.
- **AC-14**: Each binary reads its own connection string (`AGENT_DATABASE_URL`, `TOOLSERVER_DATABASE_URL`) and `make migrate` uses `MIGRATE_DATABASE_URL`; `DATABASE_URL` no longer exists, `.env.example` matches `internal/config`, and `make gen` leaves no diff.
- **AC-15**: `Run` still writes the outcome when the caller's context is cancelled after the action started, and when the action panics it writes an `error` outcome and then panics again.

## Decision

**Chosen option**: Option 1: append only audit with intent and outcome rows, mutable approvals with guarded transitions, per binary roles.

All agent owned tables land in one migration now. The tool server is the only audit writer and the only approvals writer; the agent owns conversations, messages, usage, and seen events; the eval owns its tables and reads the audit log as ground truth.

**Implementation skills**: `golang-database` · `golang-error-handling` · `golang-concurrency` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `postgres-patterns`, `database-migrations` (`everything-claude-code` plugin). Open each only when building in its area.

## Feature design

**Data model sketch**

Every table has `created_at timestamptz not null default now()` unless noted. Primary keys are `bigint generated always as identity` unless noted. Enumerated values are `text` with a `CHECK` constraint (not Postgres enum types). Money is `bigint` micro US dollars (`cost_microusd`, 1 USD = 1,000,000), so no numeric type crosses into Go.

`conversations` (owner: agent)

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | bigint identity | no | PK |
| `slack_channel_id` | text | no | unique with `slack_thread_ts` |
| `slack_thread_ts` | text | no | the thread's root ts: a top level message's own `ts`, a reply's `thread_ts`. The agent puts this same value in MCP `_meta` |
| `requester_slack_id` | text | no | |
| `directory_account` | text | yes | sAMAccountName once matched (feature #10) |
| `zammad_ticket_id` | bigint | yes | |
| `status` | text | no | `open` (default), `resolved`, `escalated` |
| `last_seq` | int | no | default 0; the message counter |
| `updated_at` | timestamptz | no | default now(); bumped by every query that changes the row |
| `closed_at` | timestamptz | yes | set when status leaves `open` |

`conversation_messages` (owner: agent)

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | bigint identity | no | PK |
| `conversation_id` | bigint | no | FK → `conversations.id`; unique with `seq` |
| `seq` | int | no | from `conversations.last_seq`, starts at 1, gapless |
| `role` | text | no | `user`, `assistant` (Messages API roles; tool results are `user`) |
| `content` | jsonb | yes | the Messages API content blocks; null once purged |
| `content_purged_at` | timestamptz | yes | |

`llm_usage` (owner: agent)

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | bigint identity | no | PK |
| `conversation_id` | bigint | no | FK → `conversations.id` |
| `model` | text | no | the provider model id actually called |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` | int | no | default 0, each ≥ 0 |
| `cost_microusd` | bigint | no | ≥ 0 |
| `pricing_date` | date | no | the `eval/pricing.yaml` entry used |

`slack_events` (owner: agent)

| Column | Type | Null | Notes |
|---|---|---|---|
| `event_id` | text | no | PK |
| `received_at` | timestamptz | no | default now(); no `created_at` |

`approvals` (owner: tool server)

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | uuid | no | PK, default `gen_random_uuid()`; goes into the Slack button value, so it must not be guessable |
| `call_id` | uuid | no | unique; the intent row that asked for approval |
| `tool_name` | text | no | |
| `arguments` | jsonb | no | the exact, full call to run on approval (medium tier tools take no secrets) |
| `risk_tier` | text | no | `read`, `low`, `medium`, `blocked` |
| `requester_slack_id` | text | no | |
| `requester_account` | text | yes | |
| `slack_channel_id`, `slack_thread_ts` | text | no | |
| `zammad_ticket_id` | bigint | yes | |
| `approver_kind` | text | no | `it`, `group_owner` |
| `allowed_approvers` | text[] | no | Slack ids snapshot at creation, at least one |
| `status` | text | no | `pending` (default), `approved`, `denied`, `expired`, `executed`, `failed` |
| `decided_by` | text | yes | the clicker's Slack id |
| `decided_at`, `executed_at` | timestamptz | yes | |
| `expires_at` | timestamptz | no | |

`audit_log` (owner: tool server, append only)

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | bigint identity | no | PK; order of record |
| `call_id` | uuid | no | default `gen_random_uuid()` on the intent; unique with `kind` |
| `kind` | text | no | `intent`, `outcome` |
| `occurred_at` | timestamptz | no | default `clock_timestamp()` (real time, not transaction start); replaces `created_at` |
| `tool_name` | text | no | a registry tool, or `approval.approve`, `approval.deny`, `approval.expire` |
| `risk_tier` | text | no | `read`, `low`, `medium`, `blocked` |
| `requester_slack_id` | text | no | from MCP `_meta` |
| `requester_account` | text | yes | null when no directory match |
| `slack_channel_id`, `slack_thread_ts` | text | yes | null only for calls outside a Slack thread (`approval.expire`) |
| `zammad_ticket_id` | bigint | yes | |
| `target` | text | yes | the account or group acted on |
| `arguments` | jsonb | yes | allowed keys only, size capped |
| `result` | jsonb | yes | allowed keys only, size capped; never for `read` tier |
| `outcome` | text | yes | `ok`, `error`, `denied_policy`, `denied_identity`, `read_only`, `pending_approval`, `denied_approval`, `expired` |
| `error` | text | yes | the error or refusal reason for any non `ok` outcome; at most 500 characters |
| `approval_id` | uuid | yes | FK → `approvals.id` |
| `approver_slack_id` | text | yes | |
| `trace_id` | text | yes | OTel trace id, links to Tempo (feature #18) |

Which columns each kind carries (enforced by `CHECK`):

| Columns | `intent` | `outcome` |
|---|---|---|
| `tool_name`, `risk_tier`, requester, thread, `zammad_ticket_id`, `target`, `approval_id`, `approver_slack_id`, `trace_id` | set by the caller | copied from the intent by `InsertAuditOutcome` |
| `arguments` | allowed | must be null |
| `outcome` | must be null | required |
| `result`, `error` | must be null | allowed |

`eval_runs` (owner: eval)

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | bigint identity | no | PK |
| `label` | text | yes | unique when set (`v1`, `final`); a rerun takes a new label |
| `git_sha` | text | no | |
| `model` | text | no | |
| `ticket_set_hash` | text | no | sha256 of the ticket file |
| `status` | text | no | `running` (default), `completed`, `failed` |
| `summary` | jsonb | yes | `{"metrics": {"<name>": <number>, …}}`; names from scope #20: `resolution_rate`, `correct_escalation_rate`, `unsafe_actions`, `wrong_actions`, `median_time_to_resolve_ms`, `cost_per_ticket_microusd` |
| `finished_at` | timestamptz | yes | |

`CHECK`: `completed` requires `summary` and `finished_at`; `failed` requires `finished_at`.

`eval_results` (owner: eval)

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | bigint identity | no | PK |
| `run_id` | bigint | no | FK → `eval_runs.id`; unique with `ticket_key` |
| `ticket_key` | text | no | the ticket's id in the ticket file |
| `expected_outcome` | text | no | `resolve`, `escalate`, `refuse` |
| `actual_outcome` | text | yes | same values; null if the ticket errored |
| `expected_tools`, `actual_tools` | text[] | no | default `{}` |
| `passed`, `unsafe_action`, `wrong_action` | bool | no | |
| `duration_ms` | int | no | |
| `input_tokens`, `output_tokens` | int | no | |
| `cost_microusd` | bigint | no | |
| `slack_channel_id`, `slack_thread_ts` | text | yes | joins to `audit_log` and `conversations` |

Relationships: conversations 1:N conversation_messages · conversations 1:N llm_usage · approvals 1:1 the originating intent (`call_id`) and 1:N audit_log rows (`approval_id`) · eval_runs 1:N eval_results. `audit_log` and `eval_results` reach a conversation by (`slack_channel_id`, `slack_thread_ts`), not an FK, because the tool server only receives the thread in `_meta` and an FK would block an audit insert for a bookkeeping reason.

Indexes beyond PKs and the unique constraints above: `audit_log (slack_channel_id, slack_thread_ts, id)`, `audit_log (occurred_at)`, `audit_log (approval_id) where approval_id is not null`; `approvals (expires_at) where status = 'pending'`; `conversations (status, created_at)`, `conversations (closed_at) where status <> 'open'`; `llm_usage (conversation_id)`; `slack_events (received_at)`.

**State transitions**

- `approvals`: `pending → approved | denied | expired`; `approved → executed | failed`. No other move. Each is one `UPDATE … WHERE id = $1 AND status = '<expected>'`; approve also requires `expires_at > now()`, the clicker `= ANY(allowed_approvers)`, and the clicker `<> requester_slack_id`. Zero rows means the move did not happen.
- `conversations`: `open → resolved | escalated`. Terminal states are final.
- `eval_runs`: `running → completed | failed`, once.

**Approval and audit flow** (how the pieces fit; features #14 and #15 build the Slack side)

1. A medium tier call arrives. `Recorder.Defer` writes, in one transaction: the intent row, the `approvals` row (with that `call_id`), and the outcome row `pending_approval` carrying `approval_id`. The original `call_id` is now closed; it is never executed under that id.
2. A click arrives at the tool server approve endpoint (spec 0001). `Recorder.Decide` runs, in one transaction, the guarded `ApproveApproval` or `DenyApproval` plus an audit pair with `tool_name` `approval.approve` or `approval.deny`, the approval's `risk_tier`, its requester and thread, `approval_id`, and `approver_slack_id` = the clicker. A move that happened gets outcome `ok` (approve) or `denied_approval` (deny). A move refused by the guard gets `denied_policy` with the reason in `error` (`not an allowed approver`, `requester cannot approve`, `expired`, `already decided`), read from the row after the update returned nothing.
3. On approve, the tool server runs the stored call through `Run` with a **new** `call_id`, the approval's `arguments`, `approval_id`, and `approver_slack_id`, then `MarkApprovalExecuted` or `MarkApprovalFailed`.
4. `ExpireDueApprovals` moves overdue rows to `expired`, and `Recorder.Expire` writes an `approval.expire` pair with outcome `expired` per row. Who calls it and when is feature #14.

**API surface** (this feature's surface is the sqlc queries plus `internal/audit`; no HTTP endpoints)

| Query / function | Kind | Role | Key inputs | Key outputs | Key errors |
|---|---|---|---|---|---|
| `InsertAuditIntent` | `:one` | toolserver | intent columns | `id`, `call_id` | FK violation on bad `approval_id` |
| `InsertAuditOutcome` | `:one` | toolserver | `call_id`, `outcome`, `result`, `error` | `id`; no rows if no intent | unique violation on a second outcome |
| `ListAuditByThread` | `:many` | toolserver, eval | channel, thread_ts | rows by `id` | |
| `ListUnconfirmedCalls` | `:many` | toolserver, eval | older_than, limit | intents with no outcome | |
| `CreateApproval` | `:one` | toolserver | approval columns | row | check violation on empty approvers, unique on `call_id` |
| `ApproveApproval` / `DenyApproval` | `:one` | toolserver | id, clicker | row or no rows | |
| `ExpireDueApprovals` | `:many` | toolserver | limit | rows moved to `expired` | |
| `MarkApprovalExecuted` / `MarkApprovalFailed` | `:one` | toolserver | id | row or no rows | |
| `GetApproval` | `:one` | toolserver | id | row | no rows |
| `UpsertConversation` | `:one` | agent | channel, thread_ts, requester | row; `ON CONFLICT … DO UPDATE SET updated_at = now()` | |
| `GetConversationByThread` | `:one` | agent, eval | channel, thread_ts | row | no rows |
| `SetConversationStatus` | `:one` | agent | id, status, optional account, optional ticket id | row; no rows if not `open` | check violation |
| `AppendMessage` | `:one` | agent | conversation id, role, content | row with next `seq` | |
| `ListMessages` | `:many` | agent | conversation id | rows by `seq` | |
| `PurgeMessageContentBefore` | `:execrows` | agent | cutoff | count | |
| `InsertLLMUsage` | `:exec` | agent | usage columns | | check violation |
| `SumConversationTokens` | `:one` | agent, eval | conversation id | bigint | |
| `SumConversationUsage` | `:one` | agent, eval | conversation id | input, output, cache read, cache write, cost totals (bigint) | |
| `MarkEventSeen` | `:execrows` | agent | event_id | 1 new, 0 duplicate | |
| `PurgeSeenEventsBefore` | `:execrows` | agent | cutoff | count | |
| `CreateEvalRun` | `:one` | eval | run columns | row | unique violation on a reused label |
| `FinishEvalRun` | `:one` | eval | id, status, summary | row; no rows if not `running` | check violation |
| `InsertEvalResult` | `:exec` | eval | result columns | | unique violation on a repeated ticket |
| `ListEvalResults` | `:many` | eval | run id | rows | |
| `CompareEvalRuns` | `:many` | eval | label a, label b | metric, value a, value b (float8) | no rows if a label is missing |
| `audit.Recorder.Run(ctx, Call, action)` | Go | toolserver | `Call`, `func(ctx) (Result, error)` | `Result`, error | `ErrAuditUnavailable`, `ErrAuditIncomplete` |
| `audit.Recorder.Refuse(ctx, Call, outcome, reason)` | Go | toolserver | `Call`, a refusal outcome | | `ErrAuditUnavailable` |
| `audit.Recorder.Defer(ctx, Call, ApprovalRequest)` | Go | toolserver | `Call`, approver kind, approvers, expiry | approval row | `ErrAuditUnavailable` |
| `audit.Recorder.Decide(ctx, approvalID, clicker, approve bool)` | Go | toolserver | | approval row, moved bool | `ErrAuditUnavailable` |
| `audit.Recorder.Expire(ctx, limit)` | Go | toolserver | | expired rows | `ErrAuditUnavailable` |

Query details the builder must keep:
- `InsertAuditOutcome` is `INSERT … SELECT` that copies the identity columns from the intent row with that `call_id`, so it returns no rows when the intent is missing.
- `SetConversationStatus` updates only `WHERE status = 'open'`; it may keep status `open` while setting `directory_account` or `zammad_ticket_id` (`sqlc.narg` with `COALESCE`), sets `closed_at` only when the new status is not `open`, and bumps `updated_at`.
- `AppendMessage` is one statement: a CTE that runs `UPDATE conversations SET last_seq = last_seq + 1, updated_at = now() … RETURNING last_seq`, then inserts the message with that `seq`. The row lock serialises concurrent appends on one thread.
- `PurgeMessageContentBefore` joins `conversations` and clears content only where `status <> 'open' AND closed_at < cutoff AND content IS NOT NULL`.
- `SumConversationTokens` is `COALESCE(SUM(input + output + cache_read + cache_write), 0)::bigint`.
- `CompareEvalRuns` inner joins `jsonb_each(summary -> 'metrics')` of the two labelled runs on metric name, casting `(value #>> '{}')::float8`. If sqlc cannot type it, unpack the two summaries in Go instead.

`audit.Recorder` semantics:
- `NewRecorder(store, cfg)` takes a `Store` interface (the sqlc queries plus a transaction runner), so tests can inject failures without a scratch role. It rejects a negative `AUDIT_OUTCOME_RETRIES`, so the tool server fails at startup.
- `Call.Store` lists the top level argument keys to keep and `Call.StoreResult` the result keys to keep (declared per tool in the registry, feature #9); every other key becomes `"[omitted]"`. Only top level keys are filtered: nested values under an allowed key are stored whole, so the registry must not allow a key that holds free text. A filtered payload over `AUDIT_MAX_PAYLOAD_BYTES` becomes `{"truncated": true, "bytes": n}`. `read` tier calls store no result.
- `Run`: insert the intent within `AUDIT_WRITE_TIMEOUT` (on failure return `ErrAuditUnavailable` and do not call `action`); call `action` with a deferred `recover`; map a panic or error to outcome `error` with the message cut to 500 characters, otherwise the `Result` outcome (default `ok`); insert the outcome on `context.WithoutCancel(ctx)` with `AUDIT_WRITE_TIMEOUT` per attempt, retrying up to `AUDIT_OUTCOME_RETRIES` times (backoff 50 ms, doubling, capped at 1 s; `3` means four attempts); on final failure log `audit_outcome_incomplete` at error level with `call_id` only and return the result with `errors.Join(actionErr, ErrAuditIncomplete)`; re-panic after the outcome attempt if the action panicked.
- `Refuse`, `Defer`, `Decide`, and `Expire` each write their rows in one transaction; any failure returns `ErrAuditUnavailable` and changes nothing.

**Value sourcing**

| Action | Value | Source |
|---|---|---|
| audit intent | `requester_slack_id`, thread, `zammad_ticket_id` | MCP `_meta`, set by agent code (spec 0001 cross component rule) |
| audit intent | `requester_account` | the tool server's identity match (feature #10); null until it exists or when there is no match |
| audit intent | `tool_name`, `risk_tier`, `Store`, `StoreResult` keys | the tool registry (feature #9) |
| audit intent | `target`, `arguments` | the tool call's arguments, filtered by `Store` |
| audit intent | `call_id` | `gen_random_uuid()` default, returned by `InsertAuditIntent` |
| audit intent | `trace_id` | the current OTel span context; null when tracing is a no op |
| audit outcome | identity columns | copied from the intent row by `InsertAuditOutcome` |
| audit outcome | `outcome`, `result`, `error` | the action's `Result` and error, the panic, or the `Refuse`/`Decide` reason |
| approval decision | clicker Slack id | the Slack interaction payload, received by agent code and sent to the approve endpoint (spec 0001), never the model |
| approval execution | `approval_id`, `approver_slack_id`, arguments | the approval row (`decided_by`, `arguments`) |
| approval | `allowed_approvers` | `SLACK_IT_APPROVERS` for `it`; the group owner's Slack id for `group_owner` (resolved in feature #15) |
| approval | `expires_at` | `now() + APPROVAL_TIMEOUT` (default set by feature #14) |
| conversation | `slack_thread_ts` | the root message `ts` (top level) or the event's `thread_ts` (reply) |
| llm_usage | tokens | the Messages API response `usage` block |
| llm_usage | `cost_microusd`, `pricing_date` | tokens × the `eval/pricing.yaml` entry for the called model, looked up when the row is written (feature #10 builds the lookup; the agent refuses to start if `LLM_MODEL` has no price) |
| budget check | tokens per ticket | `SumConversationTokens` against `AGENT_MAX_TOKENS_PER_TICKET` |
| slack_events | seen marker | `MarkEventSeen` **before** processing (at most once: a crash mid message loses that message, and the user asks again, rather than risking a duplicate write) |
| slack_events purge | cutoff | `now() - SLACK_EVENT_DEDUP_TTL`, run by the agent hourly (wired in feature #10) |
| message purge | cutoff | `now() - TICKET_RETENTION_DAYS` (feature #12 owns the default and the job) |
| eval result | `actual_tools` | `audit_log` intent rows for the ticket's thread, ordered by `id` |
| eval result | tokens, `cost_microusd` | `SumConversationUsage` for `GetConversationByThread` |
| eval result | `actual_outcome`, `passed`, flags, `duration_ms` | computed by the harness (feature #20) against the ticket file |
| eval run | `git_sha`, `model`, `ticket_set_hash`, `label`, `summary` | the harness: git HEAD, `LLM_MODEL`, sha256 of the ticket file, a CLI flag, its metric totals |

**Key invariants**

- No audit row, no write: a write action runs only inside `audit.Recorder.Run`, after its intent row exists.
- `audit_log` is never updated, deleted, or truncated: no app role holds those privileges, and trigger `audit_log_append_only` (BEFORE UPDATE OR DELETE per row, BEFORE TRUNCATE per statement) raises `audit_log is append only` with SQLSTATE `55000` for everyone, owner included. Only a reviewed migration that drops the trigger can change that.
- One intent and at most one outcome per `call_id`; every outcome has an intent. An intent with no outcome means "outcome unknown"; it is never filled in later.
- Keys outside a tool's allow list, and payloads over the cap, never reach the database.
- An approval leaves `pending` at most once and executes at most once, never on its requester's own click. `CHECK`: `approved` and `denied` have `decided_by` and `decided_at`; `expired` has no `decided_by`; `executed_at` is set only for `executed` and `failed`; `cardinality(allowed_approvers) > 0`.
- A conversation that is not `open` has `closed_at`. Message `content` is null exactly when `content_purged_at` is set. Message `seq` is gapless per conversation.
- All timestamps come from the database clock.

**Security model**

Roles (`noord_owner` owns every table and only runs migrations; no running binary gets it):

| Table | `noord_agent` | `noord_toolserver` | `noord_eval` |
|---|---|---|---|
| `conversations` | SELECT, INSERT, UPDATE (status, directory_account, zammad_ticket_id, last_seq, updated_at, closed_at) | none | SELECT |
| `conversation_messages` | SELECT, INSERT, UPDATE (content, content_purged_at) | none | none |
| `llm_usage` | SELECT, INSERT | none | SELECT |
| `slack_events` | SELECT, INSERT, DELETE | none | none |
| `approvals` | none | SELECT, INSERT, UPDATE (status, decided_by, decided_at, executed_at) | none |
| `audit_log` | none | SELECT, INSERT | SELECT |
| `eval_runs` | none | none | SELECT, INSERT, UPDATE (status, summary, finished_at) |
| `eval_results` | none | none | SELECT, INSERT |

- Identity columns need no sequence grants. New tables carry no PUBLIC grants, so the migration only grants; it does not revoke.
- The database boundary stops a compromised agent from writing audit or approval rows directly. It does not stop the agent from calling the tool server's approve endpoint with a made up clicker id: that endpoint trusts the agent's bearer token (spec 0001). Feature #14 decides whether to harden it.
- The read only Grafana role is added by feature #18.
- Compliance scope: GDPR (Noord is a Dutch employer; the data is about its staff). The audit log holds Slack ids, directory account names, and only allow listed argument keys, kept for the whole engagement and never erased; `approvals` rows are kept as long, since audit rows reference them. It must never hold names, emails, or ticket text, which is why storage is an allow list. Conversation message content is personal data and is purgeable (feature #12). The data processing note (feature #22) states all of this.

**Configuration required**

- `AGENT_DATABASE_URL` (field `Agent.DatabaseURL`, required): connection as `noord_agent`. Replaces `DATABASE_URL`.
- `TOOLSERVER_DATABASE_URL` (field `ToolServer.DatabaseURL`, required): connection as `noord_toolserver`. Replaces `DATABASE_URL`.
- `MIGRATE_DATABASE_URL` (field `Migrate.DatabaseURL` in a new `Migrate` struct, required): connection as `noord_owner`. `make migrate` reads it from the shell; the struct exists so the drift test covers the name (add it to `Names()`), and the migration Job in feature #17 adds a `LoadMigrate`.
- `SLACK_EVENT_DEDUP_TTL` (agent, default `24h`).
- `AUDIT_OUTCOME_RETRIES` (tool server, default `3`, at least 0).
- `AUDIT_WRITE_TIMEOUT` (tool server, default `5s`): per attempt timeout for each audit insert.
- `AUDIT_MAX_PAYLOAD_BYTES` (tool server, default `4096`): cap for stored `arguments` and `result`.
- `EVAL_DATABASE_URL` is declared with `cmd/eval` (feature #20); the `noord_eval` role and its grants exist from this feature.
- Every local role uses the throwaway dev password `noord-dev` (the same one Compose already uses), set in `db/roles.sql` and in the `.env.example` URLs. In the cluster, CNPG `managed.roles` creates the roles from Secrets (feature #17).

**sqlc type overrides** (in `sqlc.yaml`): `uuid` → `github.com/google/uuid.UUID`, nullable `uuid` → `github.com/google/uuid.NullUUID`; `jsonb` → `encoding/json.RawMessage`; non null `timestamptz` → `time.Time` (nullable stays `pgtype.Timestamptz`); `text[]` → `[]string`. Money is `bigint`, so no numeric override is needed.

**Critical test scenarios** (DB tests run when `TEST_DATABASE_URL` points at a superuser connection; CI provides one)

- Happy path: `Run` with a fake action writes an intent then an `ok` outcome with the same `call_id`; a disallowed key reads `"[omitted]"`; an oversize payload reads `truncated`, verifies **AC-2**, **AC-7**.
- Failure case: a store whose intent insert fails leaves the action's call counter at 0 and returns `ErrAuditUnavailable`, verifies **AC-5**. A store whose outcome insert always fails returns the result with `ErrAuditIncomplete`, and `ListUnconfirmedCalls` finds the intent, verifies **AC-6**. A cancelled context and a panicking action still produce an outcome row, verifies **AC-15**.
- Failure case: approve and deny race on one approval from two goroutines; exactly one moves it and the other is audited `denied_policy`; a requester's own click is refused, verifies **AC-8**. Twenty concurrent `AppendMessage` calls give `seq` 1 to 20, verifies **AC-9**.
- Auth/permission: after inserting a row, as `noord_toolserver` `UPDATE audit_log`, `DELETE`, and `TRUNCATE` return `42501`; as `noord_owner` they return `55000`; as `noord_agent`, `SELECT` from `audit_log` and `approvals` returns `42501`; as `noord_eval`, `UPDATE eval_results` returns `42501`, verifies **AC-3**, **AC-4**.

## Build plan

Tracer Bullet within the feature: the audit strand that slice 1 (#9, #10) needs is proven end to end first, then the other tables are thickened. One migration carries the whole target model, because the schema is small and #5's done when needs every part.

1. Roles: add `db/roles.sql`, idempotent (`DO` blocks with `IF NOT EXISTS`), creating `noord_owner`, `noord_agent`, `noord_toolserver`, `noord_eval` as login roles with password `noord-dev`, `GRANT CREATE ON SCHEMA public TO noord_owner`, and reassigning `goose_db_version` to `noord_owner` if it exists. Mount it in `compose.yaml` under `/docker-entrypoint-initdb.d/`. Add `make db-roles` (`docker compose exec -T postgres psql -U noord -d noord < db/roles.sql`), and make `migrate` depend on it, satisfies **AC-13**.
2. Config: replace `DATABASE_URL` with `AGENT_DATABASE_URL` and `TOOLSERVER_DATABASE_URL`; add the `Migrate` struct (and to `Names()`), `SLACK_EVENT_DEDUP_TTL`, `AUDIT_OUTCOME_RETRIES`, `AUDIT_WRITE_TIMEOUT`, and `AUDIT_MAX_PAYLOAD_BYTES`; update `.env.example`, both `main.go` files, and the config tests. `make migrate` uses `$${MIGRATE_DATABASE_URL:?…}` so an old `.env` fails with a clear message, satisfies **AC-14**.
3. Migration `db/migrations/00002_data_model.sql` (with `-- +goose StatementBegin/End` around `DO` blocks and functions): a guard that raises unless `current_user = 'noord_owner'` and all four roles exist, naming what is wrong; the eight tables with checks, FKs, unique constraints, and indexes; the `audit_log_append_only` trigger function and triggers; the grants table above. A `Down` that drops it all, satisfies **AC-1**, **AC-13**.
4. Embed and test helper: add `db/embed.go` (package `db`, exposing the migrations FS and `roles.sql`) and `internal/db/dbtest`. From `TEST_DATABASE_URL` (superuser), under `pg_advisory_lock`, apply `roles.sql` and build one migrated template database per migrations hash; each test then gets `CREATE DATABASE … TEMPLATE` and a pool per role; skip when the variable is unset. Move `TestOpenPingsALiveDatabase` and the DB tests in `cmd/agent` and `cmd/toolserver` onto the helper, and remove the `goose up` step from `.github/workflows/ci.yml`, satisfies **AC-13**.
5. Audit strand: `db/queries/audit.sql` and `approvals.sql`, the `sqlc.yaml` overrides, `make gen`, then `internal/audit` (`Recorder`, `Store`, `Call`, `Result`, `Run`, `Refuse`, `Defer`, `Decide`, `Expire`, allow list filter, size cap, retry, panic and cancel handling) with unit tests on a fake store, satisfies **AC-2**, **AC-5**, **AC-6**, **AC-7**, **AC-15**.
6. Grant and trigger tests: every cell of the grants table, positive and negative, plus owner blocked by the trigger, satisfies **AC-3**, **AC-4**.
7. Approval tests on a real database: each allowed and refused move, self approval, late click, and the approve against deny race, each with its audit pair, satisfies **AC-8**.
8. `db/queries/conversations.sql`, `usage.sql`, and `slack_events.sql`, with tests for the upsert, concurrent `AppendMessage`, the closed only content purge, duplicate and purged events, and the token and usage sums, satisfies **AC-9**, **AC-10**, **AC-11**.
9. `db/queries/eval.sql`, with a test storing two small labelled runs, finishing them, refusing a second finish, and comparing them, satisfies **AC-12**.
10. Update the `db/AGENTS.md` facts this changes (roles, `make db-roles`, the three URLs, the test helper); `/sync` reconciles the rest, satisfies **AC-13**, **AC-14**.

## Consequences

**Positive**:
- The append only claim is enforced by the database twice, and is demonstrable on camera with one `UPDATE` that fails.
- `Recorder` makes "no audit row, no write" structural, not a convention every tool must remember, and the allow list keeps personal data out of the one table that can't be erased.
- The eval scores what the tool server actually did, so the audit log is exercised on every eval run.
- Per binary roles add a database boundary to the credential boundary: the agent cannot write audit or approval rows directly.

**Negative / tradeoffs**:
- Two audit rows per call, and approvals add a third and fourth (decision, execution), so reading "what happened" means joining on `call_id` and `approval_id`.
- Audit and approval rows can never be removed without a migration that drops the trigger. A GDPR erasure request that reaches them is a deliberate, reviewed operation, stated in the data processing note.
- Every tool must declare its stored keys; a new tool with no allow list stores nothing useful, and a wrongly allowed free text key would leak into an unerasable table. Review allow lists like code.
- Marking Slack events seen before processing means a crash mid message drops that message.
- Local dev needs the roles: an existing Compose volume runs `make db-roles` once (`make migrate` now does it), and nothing runs as the superuser any more.
- Renaming `DATABASE_URL` touches both binaries, the config tests, `.env.example`, CI, and spec 0001's config list.
- Cost is computed at insert time, so a later price correction does not change stored cost; `pricing_date` makes the basis visible.
- Adds the `github.com/google/uuid` dependency.

**Neutral**:
- `CHECK` constraints instead of enum types mean a new outcome or status is a small migration.
- `conversation_messages.content` stores Messages API blocks as jsonb, so resuming a thread is a direct load.
- DB tests need `TEST_DATABASE_URL` as a superuser to create roles and scratch databases.

## Follow-up

- [ ] Spec 0001: update the *Configuration required* database line to the per binary names (`AGENT_DATABASE_URL`, `TOOLSERVER_DATABASE_URL`, `EVAL_DATABASE_URL`, `MIGRATE_DATABASE_URL`).
- [ ] Feature #9: the tool registry declares each tool's risk tier and `Store` / `StoreResult` keys (never a free text key), and every handler runs through `audit.Recorder`.
- [ ] Feature #10: the identity match fills `requester_account`; the `eval/pricing.yaml` lookup (a small embedding Go file beside it) fills `llm_usage.cost_microusd`; the hourly event purge; whether a message in a closed thread opens a new conversation.
- [ ] Feature #12: the purge job for `conversation_messages.content` and the `TICKET_RETENTION_DAYS` default.
- [ ] Feature #14: who calls `Recorder.Expire` and how the agent learns to escalate; recovering an approval stuck in `approved` after a crash; the `APPROVAL_TIMEOUT` default; whether the approve endpoint should verify the clicker beyond trusting the agent.
- [ ] Feature #17: CNPG `managed.roles` for the four roles, the migration Job (`LoadMigrate`, embedded migrations from `db/embed.go`), and the `ScheduledBackup` before the first real audit rows.
- [ ] Feature #18: the read only Grafana role and its SELECT grants.
- [ ] Feature #20: `EVAL_DATABASE_URL`, the JSON export of each run (CI artifact, with v1 and final committed under `eval/runs/`), and how the harness derives `actual_outcome`.
- [ ] Feature #22: the data processing note covers audit and approval retention and the no erasure tradeoff.
