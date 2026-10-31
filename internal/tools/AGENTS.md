# internal/tools

## Overview

The tool layer (spec 0007): the registry of tools with their risk tiers, and the one dispatcher every tool call passes through. `internal/mcpserver` serves it over MCP at `/mcp`; `internal/mcpclient` is how the agent calls it. The dispatcher checks, in a fixed order, whether the tool is blocked, whether the arguments are valid, who is asking, whether account changes are paused, and whether the requester hit a limit. Only then does the handler run, inside `audit.Recorder`, so every call leaves an audit pair.

## Key files

| File | Owns |
|---|---|
| `tools.go` | `Requester`, `Request`, `Args`, `Input`, `Result`, the handler types (`ReadHandler`, `AccountHandler`, `ResetHandler`, `MFAHandler`, `GroupHandler`), `Deps`, `AccountDeps`, `ResetDeps`, `MFADeps`, `GroupDeps`, and the narrow interfaces (`Directory`, `DirectoryWriter`, `GroupReader`, `HelpdeskAPI`, `History`, `KillSwitch`, `AccountReader`, `GraphAPI`, `ApprovalReads`) |
| `registry.go` | `Tool`, `Registry`, `NewRegistry`, the construction checks, the tool names, the schema bounds, the six base tools plus `group_search` and `group_access_request` (always registered, spec 0013) and `kb_search` (always registered, spec 0014), `WithPasswordReset` (adds `password_reset_link` only when the `RESET_*` settings are set), and `WithMFAClear` (adds `mfa_clear` only when the `GRAPH_*` settings and `SLACK_IT_APPROVERS` are set) |
| `dispatcher.go` | `Config` (and `Config.Check`, the startup OU checks), `Systems`, `Dispatcher`, `Call` (timeout, span, `tool_call` log line), the ordered pipeline, identity |
| `schema.go` | `Field` (with `Bool`, a JSON boolean kept in `Args` as `"true"` or `"false"`, spec 0014), the argument validator (`parseArgs`), `InputSchema` for `tools/list` |
| `refusal.go` | `Code`, the refusal table (code, outcome, stored reason, sentence), `failure`, and the LDAP, Zammad, and Graph error classification |
| `killswitch.go`, `killsync.go` | `NewKillSwitch`, `FileSwitch` (read every call, only `read_write` allows), `Mode()` (the enforced mode; an unreadable file is `read_only` plus the error), and `SyncKillSwitch` (spec 0016: records the mode in `kill_switch_state` at start and every `KILL_SWITCH_SYNC_INTERVAL`; it informs the dashboard and never decides anything) |
| `fence.go` | `Fence.CheckGroup` (one group), `Fence.AllowedGroups` (the same check batched for `group_search`), `FloorDeniedGroups`, `ErrGroupProtected`; `AccountFence.CheckAccount` (spec 0011: the account itself is protected by group membership) |
| `helpdesk.go` | `Helpdesk`: the Zammad client plus the default group, the cached ticket states, and the cached KB id and category titles (spec 0014) |
| `user_lookup.go`, `account_unlock.go`, `tickets.go` | one handler each (unexported, reachable only through the registry); `ticket_update` holds the close guard (spec 0009, AC-6) and, with `kb_review` true, adds the `kb-review` tag (`tagKBReview`) after the update succeeds (spec 0014, AC-11) |
| `kb_search.go`, `kb_rank.go`, `kbtext.go` | `kb_search` (spec 0014): `KBSettings`, `LoadKBZone` (`Europe/Amsterdam`, from the embedded `time/tzdata` in `cmd/toolserver`), the candidates, the `GetAnswer` reads (at most 4 at a time), the published rule, the stale flag, the `url`; the word score and rank; the article HTML to plain text |
| `escalate.go` | `escalate` (spec 0009): the escalation reasons, `checkEscalate` (code only reasons need the `_meta` initiator `code`), the audit reads first, then tag and note or create |
| `mfa_clear.go` | `mfa_clear` (spec 0012): `Prechecked`, the AC-3 precheck order (refuse, reuse this thread's pending approval, or go ahead with the method counts and the ticket note), the clear (`mfaClear`: two kill switch reads, the account and Entra recheck, `clearOrder` with the default method last, `deleteSteps`) |
| `group_access.go` | `group_search` and `group_access_request` (spec 0013): the batched search (five directory sessions), `groupPrecheck` in the AC-5 order, the owner checks, `groupAdd` (two kill switch reads, the full recheck, the add, the read back), and `GroupOwnerView` for the approval view |
| `execute.go` | `Dispatcher.Execute`: runs an approved call once, from the approval row (never `_meta`), with a new call id under `APPROVAL_EXECUTE_TIMEOUT`, then marks the approval executed or failed |
| `password_reset_link.go` | `password_reset_link` (spec 0011): the AC-3 refusal order, the link issue, the "link sent" note, `ResetURLKey`, `ResetLinkSentText`, and `Helpdesk.CloseWithNote` (used by the reset page) |
| `toolstest/` | in memory fakes for tests: `MemStore` (an `audit.Store` that answers the history reads like the SQL and, like Postgres, fails a query whose context has ended) and `Directory` (a fake directory that is also the writer; `Stall` and `Slow` model a hung or slow directory) |
| `../mcpserver/` | `Handler`: stateless streamable HTTP, the bearer check (SHA-256 digests, constant time), the body cap, the W3C trace context extract (exported as `RequireToken`, `CapBody`, and `ExtractTrace` for the approval routes), and the `_meta` middleware that answers −32602 |
| `../mcpclient/` | `Client.Call` / `ListTools`, `Requester`, the `_meta` key names, `ErrIncompleteRequester`, and `ApprovalClient` for the approval routes (`approvals.go`, see `internal/approvalapi`); every request carries the bearer token and the W3C trace context, so `tool.call` spans join the agent's trace |

## Conventions

- **Add a tool** with one registry entry plus a typed handler. A handler that changes a directory account takes `AccountDeps`; that alone puts it behind the kill switch and `TOOL_WRITE_LIMIT`. Every other handler takes `Deps`, which holds no writer. A `ResetHandler` (`ResetDeps`) writes no directory account but is treated as an account change too: kill switch and `TOOL_WRITE_LIMIT` apply.
- `RESET_DENIED_GROUPS` (`AccountFence`, which accounts a reset link may target) is not `TOOL_DENIED_GROUPS` (`Fence`, which groups no tool may change). They differ on purpose: `SYNC-Entra-Users` is fenced from changes but must not block resets for its members (journal 0037). A denied name that does not resolve refuses every account and logs `denied_group_unresolved`.
- `reset_url` leaves the handler only in the MCP result: the audit row drops it (`_omitted`), and agent code strips it before anything is stored or sent to the model.
- Identity comes only from MCP `_meta`, checked for exact format in `internal/mcpserver`. No tool argument names the requester, a target account, or a ticket id; account tools act only on the matched requester.
- Handlers refuse with `refused(refuse...)` and fail with `directoryFailure(err)`, `helpdeskFailure(err)`, or `failed(err)`. The model and the audit `error` column only ever see the fixed text of the refusal table; never echo an argument, an account name, or a directory or Zammad message.
- `Store` lists only argument keys that are not free text (`Field.FreeText`); registry construction fails otherwise. A tier `medium` tool needs an `Approval` hook and exactly one pair: a `Precheck` with an `MFAHandler` (spec 0012), or a `GroupPrecheck` with a `GroupHandler` (spec 0013). Both handler kinds only run behind tier `medium`; a `blocked` tool has no handler.
- A medium call never runs its handler from `tools/call`: step 9 runs the `Precheck` (or `GroupPrecheck`), then refuses, reuses this thread's pending approval (`Recorder.Reuse`), or `Defer`s a new one. The handler runs only from `Dispatcher.Execute`, after a human click. A `Defer` that loses the race for the account's one pending approval (`audit.ErrApprovalPending`) is refused `already_pending`.
- Logs and spans carry only `tool`, `tier`, `outcome`, and `trace_id`. Log `audit_unavailable` with no error text: a database error can quote the row.
- A failure also logs one `tool_call_failed` line (`tool`, `code`, `trace_id`, `cause`). `cause` is a fixed token from `cause()` in `refusal.go` (`ldap_<result code>`, `zammad_<status>`, `graph_<status>`, `graph_not_found`, `graph_not_managed`, `deadline`, `canceled`, `network`, `audit`, `other`), never error text.
- Audit writes and reads outside the handler (refusals, `fail`, the rate limit count, `Defer`) run on `recordCtx(ctx)`, which drops the call's deadline, so a call that ran out of time still leaves its pair. If the checks used up the budget, the call fails before the handler.
- The kill switch file is read with no cache, at dispatcher step 7 and again inside an account handler just before the write. With `KILL_SWITCH_FILE` unset, `READ_ONLY` decides.
- `escalate` is a `ReadHandler` on purpose: the kill switch and `TOOL_WRITE_LIMIT` never stop it, and it is not `TicketLimited`. Every audit read comes before any Zammad call. `Requester.Initiator` comes only from the `_meta` key `noord.example/initiator`, which `internal/mcpserver` accepts only as exactly `code`.
- `kb_search` is a `ReadHandler` (spec 0014). Zammad finds the matches; code picks and orders them (`kb_rank.go`), keeps only answers published by Zammad's own rule, and computes `stale`; the model never decides either. `kbtext.go` drops every href, image, and URL written as text, so no article URL reaches the model; the only links a user sees are the ones agent code builds from `url`.
- Test only tools go through `NewTestRegistry` in `registry_test.go`, so they can never reach the binary.
- `internal/tools` and `internal/mcpserver` are on both agent deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`). The agent imports only `internal/mcpclient`.

## Gotchas

- The SDK answers a call to an unknown tool name itself, so that call has no audit row. Schema validation runs in our dispatcher, not the SDK (tools are added with raw handlers), so a rejected call is audited.
- The rate limits are soft: they count `ok` outcomes, so concurrent calls can all pass, and an unlock whose outcome row failed is not counted.
- Zammad's plain `GET /api/v1/tickets/:id` omits `article_ids`; read `/api/v1/ticket_articles/by_ticket/:id` to see articles.
- `TestLiveTicketsOnZammad` leaves a closed ticket in the dev Zammad, and `TestLiveEscalateOnZammad` two open ones; run `make helpdesk-reset` after them.
- Zammad's KB search returns hits newest first, not by relevance, and every query word must prefix match, so `kb_search` reads up to `KB_SEARCH_CANDIDATES` hits and ranks them itself (journal 0060).
- `TestLiveKBSearchOnZammad` pins its stale date to the seed's `as_of` (2026-10-16) in code and ignores `KB_STALE_AS_OF`; to see the setting move the flag, call `kb_search` on a tool server started with it.

## Commands

```bash
go test -race ./internal/tools/... ./internal/mcpserver/ ./internal/mcpclient/   # unit tests, no Docker
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 TEST_DATABASE_URL=... go test -race ./internal/mcpserver/   # the whole pipe live
set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run LiveTickets -v ./internal/tools/   # right after make helpdesk-reset
set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run LiveKB -v ./internal/tools/   # kb_search on the seeded KB, reads only
```

## Related specs

- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (the contract, with [verify.md](../../docs/specs/0007-tool-server-risk-tiers/verify.md))
- [0003 data model](../../docs/specs/0003-data-model/index.md) (the audit recorder the dispatcher runs every call through)
- [0009 refusal and escalation](../../docs/specs/0009-refusal-escalation/index.md) (`escalate`, the close guard, the `_meta` initiator)
- [0011 password reset links](../../docs/specs/0011-password-reset-links/index.md) (`password_reset_link`, `AccountFence`, `RESET_DENIED_GROUPS`)
- [0012 MFA re-registration with approval](../../docs/specs/0012-mfa-reregistration-approval/index.md) (`mfa_clear`, `Precheck`, `Execute`, `MFA_DENIED_GROUPS`)
- [0013 Access requests via group owner](../../docs/specs/0013-access-requests-group-owner/index.md) (`group_search`, `group_access_request`, `GroupPrecheck`, `Fence.AllowedGroups`)
- [0014 Knowledge base answers](../../docs/specs/0014-knowledge-base-answers/index.md) (`kb_search`, the published rule, the rank, the stale flag, `kb_review` on `ticket_update`)

_Drafted by /develop from the introducing change, worth a quick human pass._
