# internal/agent

## Overview

The agent loop (spec 0008). The Slack listener hands every accepted message to `Agent.Submit`; a per thread worker runs one turn at a time. Agent code decides who is asking (the Slack gate on `users.info`, then the tool server's `user_lookup`) before the model ever runs. Claude then drives the tool server's tools one call at a time inside hard limits, and code owns the conversation status, the ticket id, the fixed replies, and the failure path that always tries to leave IT a ticket.

## Key files

| File | Owns |
|---|---|
| `types.go` | `Inbound`, `SlackUser`, the interfaces `SlackUsers`, `Poster`, `Tools`, `Model`, `Store`, and `Conversation`, `StoredMessage`, `StatusChange`, `Usage` |
| `agent.go` | `Config`, `Deps`, `New`, `Submit`, `Run` (the second sweep, the shutdown drain), the per thread workers and the `AGENT_MAX_CONCURRENT` semaphore, `post`, `detached` |
| `turn.go` | one turn, steps 3 to 11 of the spec's turn table; `agent_turn_done` |
| `identity.go` | the Slack gate (`passesGate`), the code owned `user_lookup`, `callTool`, the requester block |
| `loop.go` | the model loop: budgets, request, `llm.call`, usage rows, the one tool per step rule, `statusChangeFor` |
| `history.go` | stored content as block arrays, `buildHistory` and the tool_use / tool_result pairing check, the tool result text |
| `failure.go` | `failurePath`, `ensureTicket` and its cases, the escalation body and note |
| `sweep.go` | `Sweep` for conversations a restart interrupted |
| `tooldefs.go` | the cached `tools/list` conversion (no `user_lookup`), cache marks |
| `replies.go` | every fixed text, the escalation title and notes, `Reason` and outcome tokens |
| `pgstore.go` | `NewStore`: the `Store` over sqlc, with the one transaction for a tool result and its status change |
| `prompt/system.md` | the embedded system prompt (an eval input; change it as code) |
| `agenttest/` | in memory fakes for every interface: `Users`, `Poster`, `Tools`, a scripted `Model`, and a `Store` that answers like the SQL |

## Conventions

- **Identity is code's.** `mcpclient.Requester` is built only from the Slack event, `users.info`, and the conversation row. The model's tool input reaches only the tool's arguments. `user_lookup` is never offered to the model.
- **Order is part of the contract.** Store the assistant message before its tool call runs; store every started call's result (on a detached context) before anything else; a ticket id or `resolved` is written only in the same transaction as the result that produced it (`AppendToolResult`).
- **Contexts.** The turn context (`AGENT_MESSAGE_TIMEOUT`) derives from the agent's work context, never from the signal. Started tool calls, result stores, usage rows, status writes, the escalation ticket, and every post run on `detached` contexts: they ignore the turn deadline but keep the span and still end when `AGENT_SHUTDOWN_GRACE` runs out.
- **No retries of tool calls**, and nothing from history is replayed. The SDK's own retry (`AGENT_MAX_RETRIES`) covers the model call.
- **Fixed texts live in `replies.go`.** The eval asserts on them; change them with the spec. Fixed texts carry no footer and are never stored.
- **Logs and spans** carry ids, counts, and fixed tokens only (`agent_turn_done`, `agent_escalated`, `agent_post_failed`, `agent_status_write_failed`, `agent_interrupted`, `agent_thread_busy`, `agent_sweep_failed`); never message text, email, Slack user id, names, departments, account names, ticket titles, or model output. `TestLogsAndSpansHoldNoPersonalData` guards this.
- This package and `internal/llm` are on the credential boundary: depguard `agent-credential-boundary` covers their files and `cmd/agent/boundary_test.go` checks their transitive imports.

## Gotchas

- The model's thinking blocks come back in the response and are stored and resent unchanged (an append only history). Never edit a stored message.
- With the default 30000 token budget, about eight model calls fit in one conversation, because cache reads count toward it (each lockout turn reads the cached prompt and tools).
- `Agent.Wait` exists only in tests (`export_test.go`); production waits through `Run`.
- `ListInterruptedConversations` matches `content @> '[{"type": "tool_use"}]'`, so stored content must stay the SDK param JSON.

## Commands

```bash
go test -race ./internal/agent/...                     # fakes only, no network
TEST_DATABASE_URL=postgres://noord:noord-dev@localhost:55432/noord?sslmode=disable go test -race -run TestPg ./internal/agent/
```

## Related specs

- [0008 locked out user flow](../../docs/specs/0008-locked-out-user-flow/index.md) (the contract)
- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (the tools, `_meta` identity, refusal codes)
- [0003 data model](../../docs/specs/0003-data-model/index.md) (conversations, messages, usage)

_Drafted by /develop from the introducing change, worth a quick human pass._
