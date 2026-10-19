# Verify: locked out user flow · spec 0008 · updated 2026-10-19
_Steps derived from spec 0008 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

Live runs on 2026-10-19 used the demo account mapped to `jan.devries`, the tool server started with `READ_ONLY=false` for the run (the `.env` default stays `true`), and `claude-sonnet-5-5`. The domain unlocks an account by itself after 15 minutes, so DM within 15 minutes of `make directory-lock`.

Set up once for the live steps: put an Anthropic key in `.env` as `ANTHROPIC_API_KEY` (with both Slack tokens set the agent refuses to start without it), run `make noord-reset` so a demo Slack account's overlay email matches a seeded Noord user, then `make dev` with the tool server up. Use the superuser URL `SU=postgres://noord:noord-dev@localhost:55432/noord?sslmode=disable` for the database checks.

## UI / manual (live Slack, Samba, Zammad, and the Anthropic API)
- [x] `make directory-lock NAME=<demo user>`, then DM the bot "I'm locked out" from that user's Slack account → "Looking into it now.", then a reply saying the account is unlocked, ending with the resolved footer → AC-7, AC-12
- [x] Time the AC-7 run: the gap between the DM's `ts` and the final reply's `ts` (`conversations.history` on the DM) is under 60 seconds; record both values here → AC-7
  - 2026-10-19 run 2: DM `ts` 1792405487.222599, ack at +0.27 s, final reply at +11.28 s (`conversations.replies`); `agent_turn_done.latency_ms` 11341
- [x] After the AC-7 run: `make directory-login NAME=<demo user>` shows not locked; the Zammad ticket is closed with an internal note starting "Added by the Noord assistant:"; `conversations.status` is `resolved` with `zammad_ticket_id` and `directory_account` set → AC-6, AC-7
  - 2026-10-19 run 2: `locked: false, bad password count: 0`; Zammad ticket 22023 `closed`, a customer note then an internal note "Added by the Noord assistant: Account was enabled and locked ... Ran account_unlock; it succeeded ..."; conversation 2 `resolved`, `jan.devries`, ticket id 23, 8 stored messages
- [x] `ListAuditByThread` (or `SELECT tool_name, kind, outcome, zammad_ticket_id, trace_id FROM audit_log WHERE slack_channel_id = '<dm channel>' AND slack_thread_ts = '<ts>' ORDER BY id`) holds exactly four intent and outcome pairs: `user_lookup`, `ticket_create`, `account_unlock`, `ticket_update` → AC-4, AC-7
  - 2026-10-19 run 2: exactly those four pairs, all `ok`; `account_unlock` result `{"locked": false, "was_locked": true}`; the unlock and close rows carry ticket id 23 from `_meta`
- [x] The agent log for the run has one `agent_turn_done` (`outcome` `resolved`, `model_calls` 4, `tool_calls` 4) and no message text, email, name, or user id; with `OTEL_EXPORTER_OTLP_ENDPOINT` set, the tool server's `tool.call` spans share the turn's trace id → AC-17, Observability
  - 2026-10-19 run 2: one `agent_turn_done` (`resolved`, 4 model calls, 4 tool calls), and no message text, name, account, department, email, or ticket number in either service's log. Trace part not run: `OTEL_EXPORTER_OTLP_ENDPOINT` unset, so `trace_id` is empty (covered by `TestTraceContextCrossesToTheToolServer`)
  - 2026-10-19 `/check verify` (AC-8 paused run, endpoint pointed at a throwaway OTLP sink): all 14 spans share trace `4e3220610ce45e9767ecd98959e498fe`, including the tool server's four `tool.call` spans; span attributes hold ids, token counts, and the model only, no message text
- [x] DM "I can't log in" from an unlocked demo user → it says the account is not locked (most likely the password), opens a ticket, leaves it open; no `account_unlock` with `was_locked: true` in the audit log → AC-8
  - 2026-10-19 run 1: "I'm locked out" 21 minutes after the lock, after the domain's 15 minute auto unlock. Reply: not locked, most likely the password, ticket 22022 opened for IT, no password asked for; audit `user_lookup` and `ticket_create` only; conversation 1 `open`; 6.3 s
- [x] Set the kill switch to read only, lock the user, DM "I'm locked out" → it says account changes are paused, the ticket stays open with a note, the conversation stays open → AC-8
  - 2026-10-19 `/check verify`: `READ_ONLY=true`; reply "IT has paused account changes right now. I've opened ticket 22025", open footer; audit `user_lookup`, `ticket_create`, `account_unlock` `read_only`, `ticket_update` `ok` with state `new`; conversation 5 `open`; 9.1 s
- [x] DM "can I get a new monitor" → a short answer, an open ticket describing the request, "IT will follow up", no account change call → AC-9
  - 2026-10-19 run 3 (sent from Claude in Chrome at your request): "Can I get a new monitor?" → "I can't approve or order hardware myself. I've opened ticket 22024 for your monitor request, and IT will follow up with you", plus the open footer; audit `user_lookup` and `ticket_create` only; conversation 3 `open`, ticket 24; 6.1 s; 4696 tokens, 8591 micro USD
- [x] Mention the bot in `#it-help` → one reply in the thread with the channel redirect text, and no new `conversations` row → AC-1
  - 2026-10-19 `/check verify`: one thread reply "For account help, please send me a direct message…"; `agent_turn_done` outcome `redirected`, 0 model and 0 tool calls; no row for the channel
- [x] DM a single space → "Please send your question as a text message." and no row → AC-2
  - 2026-10-19 `/check verify`: blocked, the Slack web client will not send a whitespace only message (covered by `TestEmptyMessage`); accepted as covered by that test on 2026-10-19
- [x] Reply in a resolved thread → the closed thread text, nothing stored → AC-2
  - 2026-10-19 `/check verify`: reply in conversation 2's thread → "This conversation is closed…"; outcome `closed_thread`, 0 calls; still 8 messages and 4 usage rows, still `resolved`
- [x] DM from a Slack account whose email matches no Noord user → "Looking into it now.", then the not matched text; no `llm_usage` row for that conversation → AC-4
  - 2026-10-19 `/check verify`: a second workspace member whose email is not in the overlay → "Looking into it now.", then "I couldn't match your Slack account to a single Noord account…"; conversation 8 `escalated`, no account, 0 messages, 0 `llm_usage` rows; audit `user_lookup` `denied_identity` only; reason `not_matched`, 0 model calls
- [x] Stop the agent mid turn (Ctrl+C during a slow reply), start it again → after the stale window the thread gets the restart text (or the no ticket text) and the conversation is `escalated` → AC-14
  - 2026-10-19 `/check verify`: Ctrl+C lets a running turn finish inside `AGENT_SHUTDOWN_GRACE`, so the agent was stopped with `kill -9` right after the user message was stored. Restarted at 12:47:27; at 12:49:03 the sweep logged `agent_interrupted`, reran `user_lookup`, opened ticket 22026 with the restart note, posted "Sorry, I restarted while working on this…", and set conversation 6 `escalated`. Note: `conversations.zammad_ticket_id` stays empty for that row

## Commands
- [x] `go test -race ./internal/agent/... ./internal/slack/ ./internal/llm/ ./eval/` → all pass with fakes only → AC-1 to AC-6, AC-10 to AC-15, AC-17 to AC-19
- [x] `TEST_DATABASE_URL="$SU" go test -race -count=1 -run TestPg ./internal/agent/` → the tool result transaction, usage sum, hourly count, and interrupted query pass on real Postgres, none skipped → AC-6, AC-14, AC-15, AC-19
- [x] `go test -race ./cmd/agent/ ./internal/config/` → a model with no price, `LLM_PROVIDER=bedrock`, Slack without `ANTHROPIC_API_KEY`, and `AGENT_MAX_CONCURRENT=0` each stop startup naming the setting; without Slack none of these checks run → AC-15, AC-16
- [x] `go test -race -run 'TestAgent' ./cmd/agent/` and `make lint` → `internal/agent` and `internal/llm` import no client system package, directly or transitively → AC-18
- [x] `go test -race -run TestTraceContextCrossesToTheToolServer ./internal/mcpserver/` → the tool server's `tool_call` line carries the caller's trace id → Observability
- [x] `make check` → lint, gen-check, tidy-check, vuln, secrets, and tests all pass (gen-check needs the sqlc output committed) → all

## Value sourcing checks
- [x] Workspace team: start the agent and confirm a DM from a member of another workspace (or a Slack Connect user) gets the not staff text → the team id comes from `auth.test` → AC-3
  - 2026-10-19 `/check verify`: same team, so the gate was tested with a multi channel guest (`is_restricted` true, Slack Pro trial) instead of an outside account → "Looking into it now.", then "I can only help Noord staff with a confirmed work email…"; conversation 10 `escalated`, 0 tool and 0 model calls, nothing stored, no audit row; reason `slack_profile`. The `team_id` comparison itself rests on `TestSlackGate`
- [x] `Requester.Email`: change nothing in the message, confirm the `_meta` email the tool server audits is the Slack profile email, never text from the message → AC-18
  - 2026-10-19 `/check verify`: the AC-7 `user_lookup` audit row has `arguments` `{}` and the message "I'm locked out now" holds no email, yet it matched `jan.devries`, so the email came from `_meta` (the audit stores the account, not the email)
- [x] `Requester.TicketID`: in the AC-7 audit rows, `ticket_create` has no ticket target and `account_unlock` and `ticket_update` run after it with the new ticket id in `_meta` (their rows' `zammad_ticket_id` is that id) → AC-6
- [x] `directory_account`: a second message in the same thread from a Slack account that now matches a different Noord user gets the account changed text → AC-4
  - 2026-10-19 `/check verify`: not reachable live, since nobody else can post in someone's DM thread (covered by `TestAccountChanged`); accepted as covered by that test on 2026-10-19
- [x] Requester block: lock a user with a timed lockout and DM; the model's reply reflects the lock (it does not ask whether the account is locked) → AC-5
  - 2026-10-19 `/check verify`: in the AC-7 run the model's first step was `ticket_create` with a body stating the account is locked until 10:39:22Z; the AC-8 paused reply also gave the lock's end time
- [x] `tool_choice`: in a resolved thread's last model call the request used `none` (unit test `TestHappyPathUnlocksAndClosesTheTicket`) → AC-6
- [x] `llm_usage` cost: for the AC-7 conversation, each row's `cost_microusd` equals `(input × 20000 + output × 100000 + cache_read × 2000 + cache_write × 25000 + 5000) / 10000` for `claude-sonnet-5-5`, and `pricing_date` is the date in `eval/pricing.yaml` → AC-15
  - 2026-10-19 run 2: four rows, e.g. 219 in, 150 out, 1958 cache read → 2330 micro USD (checked by hand); total 8772 micro USD (about $0.009); `pricing_date` 2026-10-06
- [x] Token budget: `SELECT sum(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens) FROM llm_usage WHERE conversation_id = <id>` for the AC-7 run; record it here and compare with `AGENT_MAX_TOKENS_PER_TICKET` (30000) → AC-10
  - 2026-10-19 run 2: 9926 tokens for a full lockout (cache reads 7832 of them), about a third of the budget
- [x] Hourly count: start eleven new DM threads within an hour from one account → the eleventh gets the too many text; a different account is unaffected → AC-19
  - 2026-10-19 `/check verify`, first half only: with `AGENT_CONVERSATIONS_PER_HOUR=5` and five conversations in the hour, the sixth DM got "You've started a lot of conversations with me in the last hour…", no ack, no stored message, no usage or audit row, reason `too_many`. Second half: with `AGENT_CONVERSATIONS_PER_HOUR=1` and jan at 3 conversations in the hour, the second account's first DM passed the hourly check (conversation 8). Its next DM got the too many text, so the count is per account
- [x] Latency: `agent_turn_done.latency_ms` for the AC-7 run matches the gap measured from the DM `ts` → AC-7

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
