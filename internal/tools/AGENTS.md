# internal/tools

## Overview

The tool layer (spec 0007): the registry of tools with their risk tiers, and the one dispatcher every tool call passes through. `internal/mcpserver` serves it over MCP at `/mcp`; `internal/mcpclient` is how the agent calls it. The dispatcher checks, in a fixed order, whether the tool is blocked, whether the arguments are valid, who is asking, whether account changes are paused, and whether the requester hit a limit. Only then does the handler run, inside `audit.Recorder`, so every call leaves an audit pair.

## Key files

| File | Owns |
|---|---|
| `tools.go` | `Requester`, `Request`, `Args`, `Input`, `Result`, the handler types (`ReadHandler`, `AccountHandler`), `Deps`, `AccountDeps`, and the narrow interfaces (`Directory`, `DirectoryWriter`, `HelpdeskAPI`, `History`, `KillSwitch`) |
| `registry.go` | `Tool`, `Registry`, `NewRegistry`, the construction checks, the tool names, the schema bounds, and the five production tools |
| `dispatcher.go` | `Config` (and `Config.Check`, the startup OU checks), `Systems`, `Dispatcher`, `Call` (timeout, span, `tool_call` log line), the ordered pipeline, identity |
| `schema.go` | `Field`, the argument validator (`parseArgs`), `InputSchema` for `tools/list` |
| `refusal.go` | `Code`, the refusal table (code, outcome, stored reason, sentence), `failure`, and the LDAP and Zammad error classification |
| `killswitch.go` | `NewKillSwitch`, `FileSwitch` (read every call, only `read_write` allows) |
| `fence.go` | `Fence.CheckGroup`, `FloorDeniedGroups`, `ErrGroupProtected` |
| `helpdesk.go` | `Helpdesk`: the Zammad client plus the default group and the cached ticket states |
| `user_lookup.go`, `account_unlock.go`, `tickets.go` | one handler each (unexported, reachable only through the registry); `ticket_update` holds the close guard (spec 0009, AC-6) |
| `escalate.go` | `escalate` (spec 0009): the escalation reasons, `checkEscalate` (code only reasons need the `_meta` initiator `code`), the audit reads first, then tag and note or create |
| `toolstest/` | in memory fakes for tests: `MemStore` (an `audit.Store` that answers the history reads like the SQL and, like Postgres, fails a query whose context has ended) and `Directory` (a fake directory that is also the writer; `Stall` and `Slow` model a hung or slow directory) |
| `../mcpserver/` | `Handler`: stateless streamable HTTP, the bearer check (SHA-256 digests, constant time), the body cap, the W3C trace context extract, and the `_meta` middleware that answers −32602 |
| `../mcpclient/` | `Client.Call` / `ListTools`, `Requester`, the `_meta` key names, `ErrIncompleteRequester`; every request carries the bearer token and the W3C trace context, so `tool.call` spans join the agent's trace |

## Conventions

- **Add a tool** with one registry entry plus a typed handler. A handler that changes a directory account takes `AccountDeps`; that alone puts it behind the kill switch and `TOOL_WRITE_LIMIT`. Every other handler takes `Deps`, which holds no writer.
- Identity comes only from MCP `_meta`, checked for exact format in `internal/mcpserver`. No tool argument names the requester, a target account, or a ticket id; account tools act only on the matched requester.
- Handlers refuse with `refused(refuse...)` and fail with `directoryFailure(err)`, `helpdeskFailure(err)`, or `failed(err)`. The model and the audit `error` column only ever see the fixed text of the refusal table; never echo an argument, an account name, or a directory or Zammad message.
- `Store` lists only argument keys that are not free text (`Field.FreeText`); registry construction fails otherwise. A tier `medium` tool needs an `Approval` hook; a `blocked` tool has no handler.
- Logs and spans carry only `tool`, `tier`, `outcome`, and `trace_id`. Log `audit_unavailable` with no error text: a database error can quote the row.
- A failure also logs one `tool_call_failed` line (`tool`, `code`, `trace_id`, `cause`). `cause` is a fixed token from `cause()` in `refusal.go` (`ldap_<result code>`, `zammad_<status>`, `deadline`, `canceled`, `network`, `audit`, `other`), never error text.
- Audit writes and reads outside the handler (refusals, `fail`, the rate limit count, `Defer`) run on `recordCtx(ctx)`, which drops the call's deadline, so a call that ran out of time still leaves its pair. If the checks used up the budget, the call fails before the handler.
- The kill switch file is read with no cache, at dispatcher step 7 and again inside an account handler just before the write. With `KILL_SWITCH_FILE` unset, `READ_ONLY` decides.
- `escalate` is a `ReadHandler` on purpose: the kill switch and `TOOL_WRITE_LIMIT` never stop it, and it is not `TicketLimited`. Every audit read comes before any Zammad call. `Requester.Initiator` comes only from the `_meta` key `noord.example/initiator`, which `internal/mcpserver` accepts only as exactly `code`.
- Test only tools go through `NewTestRegistry` in `registry_test.go`, so they can never reach the binary.
- `internal/tools` and `internal/mcpserver` are on both agent deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`). The agent imports only `internal/mcpclient`.

## Gotchas

- The SDK answers a call to an unknown tool name itself, so that call has no audit row. Schema validation runs in our dispatcher, not the SDK (tools are added with raw handlers), so a rejected call is audited.
- The rate limits are soft: they count `ok` outcomes, so concurrent calls can all pass, and an unlock whose outcome row failed is not counted.
- Zammad's plain `GET /api/v1/tickets/:id` omits `article_ids`; read `/api/v1/ticket_articles/by_ticket/:id` to see articles.
- `TestLiveTicketsOnZammad` leaves a closed ticket in the dev Zammad, and `TestLiveEscalateOnZammad` two open ones; run `make helpdesk-reset` after them.

## Commands

```bash
go test -race ./internal/tools/... ./internal/mcpserver/ ./internal/mcpclient/   # unit tests, no Docker
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 TEST_DATABASE_URL=... go test -race ./internal/mcpserver/   # the whole pipe live
set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run LiveTickets -v ./internal/tools/   # right after make helpdesk-reset
```

## Related specs

- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (the contract, with [verify.md](../../docs/specs/0007-tool-server-risk-tiers/verify.md))
- [0003 data model](../../docs/specs/0003-data-model/index.md) (the audit recorder the dispatcher runs every call through)
- [0009 refusal and escalation](../../docs/specs/0009-refusal-escalation/index.md) (`escalate`, the close guard, the `_meta` initiator)

_Drafted by /develop from the introducing change, worth a quick human pass._
