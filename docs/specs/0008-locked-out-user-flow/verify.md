# Verify: locked out user flow · spec 0008 · updated 2026-10-19
_Steps derived from spec 0008 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

Set up once for the live steps: put an Anthropic key in `.env` as `ANTHROPIC_API_KEY` (with both Slack tokens set the agent refuses to start without it), run `make noord-reset` so a demo Slack account's overlay email matches a seeded Noord user, then `make dev` with the tool server up. Use the superuser URL `SU=postgres://noord:noord-dev@localhost:55432/noord?sslmode=disable` for the database checks.

## UI / manual (live Slack, Samba, Zammad, and the Anthropic API)
- [ ] `make directory-lock NAME=<demo user>`, then DM the bot "I'm locked out" from that user's Slack account → "Looking into it now.", then a reply saying the account is unlocked, ending with the resolved footer → AC-7, AC-12
- [ ] Time the AC-7 run: the gap between the DM's `ts` and the final reply's `ts` (`conversations.history` on the DM) is under 60 seconds; record both values here → AC-7
- [ ] After the AC-7 run: `make directory-login NAME=<demo user>` shows not locked; the Zammad ticket is closed with an internal note starting "Added by the Noord assistant:"; `conversations.status` is `resolved` with `zammad_ticket_id` and `directory_account` set → AC-6, AC-7
- [ ] `ListAuditByThread` (or `SELECT tool_name, kind, outcome, zammad_ticket_id, trace_id FROM audit_log WHERE slack_channel_id = '<dm channel>' AND slack_thread_ts = '<ts>' ORDER BY id`) holds exactly four intent and outcome pairs: `user_lookup`, `ticket_create`, `account_unlock`, `ticket_update` → AC-4, AC-7
- [ ] The agent log for the run has one `agent_turn_done` (`outcome` `resolved`, `model_calls` 4, `tool_calls` 4) and no message text, email, name, or user id; with `OTEL_EXPORTER_OTLP_ENDPOINT` set, the tool server's `tool.call` spans share the turn's trace id → AC-17, Observability
- [ ] DM "I can't log in" from an unlocked demo user → it says the account is not locked (most likely the password), opens a ticket, leaves it open; no `account_unlock` with `was_locked: true` in the audit log → AC-8
- [ ] Set the kill switch to read only, lock the user, DM "I'm locked out" → it says account changes are paused, the ticket stays open with a note, the conversation stays open → AC-8
- [ ] DM "can I get a new monitor" → a short answer, an open ticket describing the request, "IT will follow up", no account change call → AC-9
- [ ] Mention the bot in `#it-help` → one reply in the thread with the channel redirect text, and no new `conversations` row → AC-1
- [ ] DM a single space → "Please send your question as a text message." and no row → AC-2
- [ ] Reply in a resolved thread → the closed thread text, nothing stored → AC-2
- [ ] DM from a Slack account whose email matches no Noord user → "Looking into it now.", then the not matched text; no `llm_usage` row for that conversation → AC-4
- [ ] Stop the agent mid turn (Ctrl+C during a slow reply), start it again → after the stale window the thread gets the restart text (or the no ticket text) and the conversation is `escalated` → AC-14

## Commands
- [ ] `go test -race ./internal/agent/... ./internal/slack/ ./internal/llm/ ./eval/` → all pass with fakes only → AC-1 to AC-6, AC-10 to AC-15, AC-17 to AC-19
- [ ] `TEST_DATABASE_URL="$SU" go test -race -count=1 -run TestPg ./internal/agent/` → the tool result transaction, usage sum, hourly count, and interrupted query pass on real Postgres, none skipped → AC-6, AC-14, AC-15, AC-19
- [ ] `go test -race ./cmd/agent/ ./internal/config/` → a model with no price, `LLM_PROVIDER=bedrock`, Slack without `ANTHROPIC_API_KEY`, and `AGENT_MAX_CONCURRENT=0` each stop startup naming the setting; without Slack none of these checks run → AC-15, AC-16
- [ ] `go test -race -run 'TestAgent' ./cmd/agent/` and `make lint` → `internal/agent` and `internal/llm` import no client system package, directly or transitively → AC-18
- [ ] `go test -race -run TestTraceContextCrossesToTheToolServer ./internal/mcpserver/` → the tool server's `tool_call` line carries the caller's trace id → Observability
- [ ] `make check` → lint, gen-check, tidy-check, vuln, secrets, and tests all pass (gen-check needs the sqlc output committed) → all

## Value sourcing checks
- [ ] Workspace team: start the agent and confirm a DM from a member of another workspace (or a Slack Connect user) gets the not staff text → the team id comes from `auth.test` → AC-3
- [ ] `Requester.Email`: change nothing in the message, confirm the `_meta` email the tool server audits is the Slack profile email, never text from the message → AC-18
- [ ] `Requester.TicketID`: in the AC-7 audit rows, `ticket_create` has no ticket target and `account_unlock` and `ticket_update` run after it with the new ticket id in `_meta` (their rows' `zammad_ticket_id` is that id) → AC-6
- [ ] `directory_account`: a second message in the same thread from a Slack account that now matches a different Noord user gets the account changed text → AC-4
- [ ] Requester block: lock a user with a timed lockout and DM; the model's reply reflects the lock (it does not ask whether the account is locked) → AC-5
- [ ] `tool_choice`: in a resolved thread's last model call the request used `none` (unit test `TestHappyPathUnlocksAndClosesTheTicket`) → AC-6
- [ ] `llm_usage` cost: for the AC-7 conversation, each row's `cost_microusd` equals `(input × 20000 + output × 100000 + cache_read × 2000 + cache_write × 25000 + 5000) / 10000` for `claude-sonnet-5-5`, and `pricing_date` is the date in `eval/pricing.yaml` → AC-15
- [ ] Token budget: `SELECT sum(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens) FROM llm_usage WHERE conversation_id = <id>` for the AC-7 run; record it here and compare with `AGENT_MAX_TOKENS_PER_TICKET` (30000) → AC-10
- [ ] Hourly count: start eleven new DM threads within an hour from one account → the eleventh gets the too many text; a different account is unaffected → AC-19
- [ ] Latency: `agent_turn_done.latency_ms` for the AC-7 run matches the gap measured from the DM `ts` → AC-7

## Acceptance-criteria coverage
- AC-1 … channel mention step; `TestChannelMentionIsRedirected`
- AC-2 … empty and closed thread steps; `TestEmptyMessage`, `TestClosedThread`, `TestDMIsSubmittedOnce`
- AC-3 … team value step; `TestSlackGate`, `TestSlackProfileErrorLeavesItOpen`, `TestToSlackUser`
- AC-4 … not matched and account changed steps; `TestDirectoryMatch`, `TestAccountChanged`
- AC-5 … requester block step; `TestHappyPathUnlocksAndClosesTheTicket`, `TestOnlyTheFirstToolRuns`, `TestBuildHistory`
- AC-6 … AC-7 aftermath and ticket id steps; `TestResolvedThenModelFails`, `TestPgToolResultTransaction`
- AC-7 … the live run and its timing (manual only)
- AC-8 … not locked and paused steps; `TestNotLockedLeavesTheTicketOpen`, `TestRefusedUpdateKeepsTheConversationOpen`
- AC-9 … the monitor request step (manual only)
- AC-10 … `TestFailureReasons`, `TestBrokenHistory`, `TestEnsureTicketCases`, budget step
- AC-11 … `TestStartedCallFinishesPastTheDeadline`, `TestNoAnswerStoresUnknown`
- AC-12 … footers in the AC-7 step; `TestHappyPathUnlocksAndClosesTheTicket`
- AC-13 … `TestOneThreadRunsInOrder`, `TestThreadQueueCap`, `TestConcurrencyCap`, `TestShutdownDrain`, `TestShutdownGraceCancels`
- AC-14 … restart step; `TestSweep`, `TestPgListInterrupted`
- AC-15 … cost step; `TestPriceForShippedModels`, `TestCostMicroUSD`, `TestRunRefusesABadModelSetup`
- AC-16 … `TestLoadAgentLoopChecks`, `TestOptionsUseOnlyTheGivenKey`, `TestRunRefusesABadModelSetup`
- AC-17 … log step; `TestLogsAndSpansHoldNoPersonalData`
- AC-18 … email value step; `TestModelCannotChangeTheRequester`, `TestAgentLoopPackagesDoNotImportClientSystems`
- AC-19 … hourly step; `TestHourlyCap`, `TestPgUsageAndHourlyCount`
