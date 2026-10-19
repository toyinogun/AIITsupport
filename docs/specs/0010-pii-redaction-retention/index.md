# 0010. PII redaction and retention: allow listed log and span keys, an hourly content purge, and a Zammad retention scheduler

**Date**: 2026-10-19
**Status**: Proposed

## Summary

This spec puts safety nets under the privacy rules every earlier spec already follows. Log lines and exported spans keep only short, plain values under the keys on one shared allow list (a list of keys known to hold ids, counts, or fixed tokens), and anything else prints as `[redacted]`. Stored chat text in Postgres is cleared 30 days after a conversation ends, and a Zammad scheduler deletes the assistant's closed tickets 30 days after they close: one number, two start events. Building it means changing three small packages (`internal/logging`, `internal/telemetry`, `internal/server`), adding one agent loop and one seed step, and writing a canary test (a test that hunts for known names in the output) over the unlock and refusal flows.

## Rationale

Reasoning, the options weighed, and the inventory of where personal data lives: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As Noord's IT person, I want logs and traces I can share with a contractor or paste into a ticket without leaking a colleague's name, email, or message, so that debugging never becomes a data incident.
- As Noord (the data controller under GDPR), I want chat text and assistant ticket text deleted on a fixed, stated schedule, so that the assistant keeps personal data no longer than it needs it.
- As the engineer, I want a new log key or span attribute that could carry personal data to fail CI before it ships, so that privacy does not depend on every future change remembering the rule.

**Acceptance criteria**:
- **AC-1**: `logging.New` (used by `cmd/agent` and `cmd/toolserver`) builds a JSON handler whose `ReplaceAttr` keeps the built in keys (`time`, `level`, `msg`) and passes an attribute only when all hold: it is not inside a group (`len(groups) == 0`), `logging.Allowed(key)` is true, its resolved value is a string, int, uint, float, bool, or duration, and a string value is at most 128 bytes with no whitespace and no `@`. Every other attribute keeps its key and gets the value `[redacted]`. This applies to attributes added with `With` too. `cmd/seed` builds its own plain JSON handler with an unexported helper inside `cmd/seed`; `internal/logging` offers no unredacted constructor.
- **AC-2**: `logging.Allowed` and `logging.AllowedKeys()` (a sorted copy) read one unexported list, shared by logs and spans. It starts as exactly the keys the runtime code uses today, without `error`, plus the new keys in this spec (see *Allow list* below). A Go test loads the runtime packages with `golang.org/x/tools/go/packages` and type information (`cmd/agent`, `cmd/toolserver`, and every `internal/` package except `internal/seed` and `internal/db`). It fails, naming the file and line, when: a key passed to a `*slog.Logger` method or a `slog` attribute constructor is not a string literal, is `time`, `level`, `msg`, or `source`, or is not on the list; a key given to an `attribute` constructor (`String`, `Int`, `Int64`, `Float64`, `Bool`, their `Slice` forms, `Stringer`, or `attribute.Key(...)` methods) is not a string literal or not on the list; or a span name passed to `Start` is not a string literal. Resource attributes built in `internal/telemetry` are exempt.
- **AC-3**: No runtime log line carries error text. The five `internal/slack/listener.go` call sites that log `"error", err.Error()` (`slack_connect_failed` twice, `slack_ack_failed`, `slack_event_dedup_failed`, `slack_event_purge_failed`) log `cause` instead, from `cause.Of`. `poster.Check` errors go through `wrapErr`, and a `ConnectionErrorEvent` is classified as `cause.Of(wrapErr(e.ErrorObj))`, so they get `slack_*` tokens, not `other`. `internal/server`'s `http.Server.ErrorLog` writes one fixed `http_server_error` line at error level with no text from the standard library (TLS errors and panic values never reach the log).
- **AC-4**: `telemetry.Setup(ctx, service, endpoint, logger)` always installs an OpenTelemetry error handler that logs `telemetry_export_failed` with only `cause`, at most once a minute. With an endpoint set, it wraps the OTLP exporter in `telemetry.NewRedactingExporter(inner)`. Every exported span keeps an attribute only under the AC-1 rules (key on the list, a plain short value), carries no events, has an empty status description (the status code stays), and its links and instrumentation scope carry no attributes. Resource attributes (`service.name`) pass unchanged. The wrapper builds new slices and never changes the span it was given.
- **AC-5**: A canary test per binary drives the unlock flow and the refusal flow (a model escalation and a backstop) through the real `logging.New` handler and `NewRedactingExporter`, using the existing fakes (`internal/agent/agenttest` and the tool server's fakes), and collects every log line and exported span. The test checks JSON string values only, never keys. None may hold: any display name, given name, or surname from `seed/directory/noord.yaml` (case sensitive, whole word), any email or username from it (case insensitive, whole word), the fake requester's Slack user id, the fake requester's department (case sensitive, whole word), the user's message text, or the ticket `title`, `body`, or `summary` the scripted model wrote. The output also holds no `[redacted]`, which proves the allow list covers these flows.
- **AC-6**: One live run on `make dev` (real Samba, Zammad, and Slack) does one unlock and one privileged refusal. Both binaries' stdout is captured with `tee` to files, grepped for the AC-5 values of the requester used, finds none, and the commands and result are recorded in `verify.md`.
- **AC-7**: `TICKET_RETENTION_DAYS` has default `30` (a `default` tag) in `config.Common`. One shared check, called by `LoadAgent`, `LoadToolServer`, and `LoadSeedHelpdesk`, refuses values outside 1 to 365 with an error naming the setting. `.env.example` lists it with `30`.
- **AC-8**: The agent runs a retention pass at start and then every hour, whether or not Slack is configured. With cutoff = the agent's clock now minus `TICKET_RETENTION_DAYS` × 24h, a pass first closes idle conversations (`status` `open` and `updated_at` before the cutoff become `resolved`, with `closed_at` set to their `updated_at`), then purges message content (`PurgeMessageContentBefore`: messages of conversations that are not `open` and closed before the cutoff get `content` null and `content_purged_at` set). Open conversations with activity after the cutoff, and conversations closed after it, are untouched. Each step logs `retention_purged` at info with `kind` (`idle_conversations` or `messages`) and `rows`. A failed step logs `retention_purge_failed` at warn with `kind` and `cause`, and the next tick tries again. A failure never stops the agent, nothing is logged when the pass ends because the agent's context ended, and the loop ends with that context.
- **AC-9**: Every `make helpdesk-seed` run that reaches the fresh or the already seeded path upserts exactly one Zammad scheduler named `noord-retention`: on the fresh path after the marker is written, on the already seeded path after `checkToken` passes. It is active and runs every hour at minute 0. Its condition: tags contain `noord-assistant`, state is one of the ids of the `closed` and `merged` states, and close time is more than N days ago (N = `TICKET_RETENTION_DAYS`). Its action deletes the ticket. Rerunning with a different N updates the same scheduler, never a second one. `seed/helpdesk/rails/retention.rb` is left out of the marker hash, so adding or editing it never forces a reset. The seed output reports `retention scheduler: <N> days, <M> tickets match now` on both paths. A Zammad in the needs reset state still returns `ErrNeedsReset`, and its scheduler is not touched.
- **AC-10**: Live Zammad test (`TEST_ZAMMAD=1`, after `helpdesk-reset`): tickets are created through the assistant's `internal/zammad` client (so they carry `noord-assistant`), closed, and their close times set through Rails. After one forced run of `noord-retention`, a closed tagged ticket whose close time is past the window is gone (API 404). These all remain: a closed tagged ticket inside the window, an open tagged ticket with an old creation date, and a seeded closed ticket (seeded tickets don't carry the tag).
- **AC-11**: `docs/runbooks/data-retention.md` states what is purged, where, and when (idle conversations closed, message content, assistant tickets). It states what is kept, for how long, and why (`audit_log`, `approvals`, conversation ids and account names, `llm_usage`, as spec 0003 decided), and that a DM `channel_id` in logs is a stable pseudonym for one user. It states that Slack history follows the workspace plan's retention (escalation summaries in the IT channel, replies in DMs), and that the model provider keeps API request data under its own retention terms. It explains how to change the window (edit the setting, rerun `make helpdesk-seed`, restart both binaries), and gives the commands to check both purges ran: a SQL count of messages with content in conversations closed before the cutoff (expected 0), and a Rails one liner printing `noord-retention`'s `last_run_at` and current match count.

## Decision

**Chosen option**: Option 2: structural nets (a key and value allow list enforced in the slog handler and the span exporter, plus a typed source scan) plus retention where each copy of the text lives (an agent retention loop for Postgres, a Zammad scheduler for tickets).

Personal data is kept out of logs and traces by deny by default rules at the handler and the exporter, and by a CI test over the source. Retained text is cleared by the system that holds it, on one setting, counted from when the conversation or ticket ends.

**Implementation skills**: `golang-security` (PII in logs) · `golang-error-handling` (slog, the cause tokens) · `golang-concurrency` (the retention loop) · `golang-database` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `opentelemetry` (`grafana/skills`, `.claude/skills/opentelemetry/`, the exporter wrapper) · `golang-testing` (`everything-claude-code` plugin). Open each only when building in its area.

## Feature design

**Data model sketch**: no schema or grant change.

| Table | Column | Change |
|---|---|---|
| `conversation_messages` | `content` (jsonb, null once purged), `content_purged_at` (timestamptz) | written by `PurgeMessageContentBefore`, which exists (`db/queries/conversations.sql`) |
| `conversations` | `status`, `closed_at`, `updated_at` | written by a new query `CloseIdleConversationsBefore` (`:execrows`, cutoff): `SET status = 'resolved', closed_at = updated_at WHERE status = 'open' AND updated_at < @cutoff` (`updated_at` stays as is); read by the purge's join |

The agent already holds `UPDATE (status, closed_at, updated_at)` on `conversations` and `UPDATE (content, content_purged_at)` on `conversation_messages`. The tool server gains nothing. The purge is one unbatched update using the existing partial index on closed conversations, which is fine at this volume.

**State transitions**:
- Conversation: `open` → `resolved` by the idle close (no activity for N days), besides spec 0008's and 0009's own transitions. A later message in that thread gets `closed_thread` (spec 0008), so purged text is never replayed.
- Message content: `stored` → `purged` (`content` null and `content_purged_at` set, together), once, only after its conversation ended more than N days ago. It never goes back.
- Zammad ticket: `closed` or `merged` → deleted, N days after its close time. A ticket IT reopens is no longer in those states, so it is safe until closed again.

**Allow list** (`logging.Allowed`; the build confirms it against the call sites, and AC-2's scan keeps it honest):

| Group | Keys |
|---|---|
| ids | `event_id`, `channel_id`, `conversation_id`, `call_id`, `trace_id` |
| fixed tokens | `service`, `outcome`, `cause`, `reason`, `kind`, `code`, `tool`, `tier`, `event_type`, `api`, `escalation_reason`, `stop_reason`, `model`, `addr` |
| counts and flags | `attempt`, `attempts`, `backoff_ms`, `latency_ms`, `model_calls`, `tool_calls`, `ticket`, `removed`, `read_only`, `rows`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` |

Adding a key is a reviewed one line change. It must only ever hold an id that is not a person's (Slack channel ids and event ids are fine; a Slack user id is not), a count, a flag, or a fixed token.

**API surface** (Go functions and operator commands; no new HTTP surface):

| Surface | Kind | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `logging.New(w, level, service)` | func | writer, level, service | `*slog.Logger` with the AC-1 handler | n/a | none (level already validated) |
| `logging.Allowed(key)`, `logging.AllowedKeys()` | funcs | key | bool; a sorted copy of the list | n/a | n/a |
| `cause.Of(err)` | func (`internal/cause`) | error | fixed token | n/a | n/a |
| `telemetry.Setup(ctx, service, endpoint, logger)` | func | as today, plus the logger | `ShutdownFunc`; wraps the exporter, sets the error handler | n/a | exporter creation fails |
| `telemetry.NewRedactingExporter(inner)` | func | an `sdktrace.SpanExporter` | an `sdktrace.SpanExporter` | n/a | passes `inner`'s errors through |
| retention loop (`internal/agent/retention.go`) | goroutine started by `cmd/agent`'s errgroup | `TICKET_RETENTION_DAYS`, clock, interval, `Store` | `retention_purged` / `retention_purge_failed` lines | `noord_agent` role | DB down: log and retry next tick |
| `Store.CloseIdleConversations(ctx, cutoff)`, `Store.PurgeMessageContent(ctx, cutoff)` | agent `Store` methods (pgstore and `agenttest` fake) | cutoff | rows | `noord_agent` | DB errors |
| retention step (`seed/helpdesk/rails/retention.rb`, `StepRetention`) | Rails runner step, as `bootstrap` and `backdate` | `NOORD_RETENTION_DAYS` | `NOORD_RESULT` with days and match count | `cmd/seed` only (the admin path) | `NOORD_FAILED` |

**Value sourcing**:

| Action | Value produced / displayed | Source |
|---|---|---|
| log handler, span wrapper | the replacement value | constant `logging.Redacted` = `[redacted]` |
| log handler, span wrapper | which keys and values pass | the unexported list behind `logging.Allowed`, and the AC-1 value rules |
| listener, OTel error, HTTP server lines | `cause` | `cause.Of`, the generic half of `internal/agent/cause.go` moved to the leaf package `internal/cause` (`none`, `deadline`, `canceled`, `postgres_<sqlstate>`, the `Causer` interface that `internal/slack`'s `slackError` implements, `network`, `other`) |
| HTTP server error line | message | constant `http_server_error`; the stdlib text is dropped |
| OTel error line | rate | at most one line per minute (a constant) |
| retention pass | cutoff | the agent's injected clock now, minus `TICKET_RETENTION_DAYS` × 24h (as the Slack event purge) |
| idle close | `closed_at` | the row's own `updated_at` (the last activity, a database clock time) |
| purge | `content_purged_at` | the database clock, `now()` in the query (spec 0003) |
| retention pass | `rows` | each query's `:execrows` count |
| retention pass | cadence | at start, then every hour; the interval is a constructor argument so tests can shorten it |
| retention step | N | `TICKET_RETENTION_DAYS`, read by `LoadSeedHelpdesk` through `config.Common`, held as `Seeder.RetentionDays`, and passed as `NOORD_RETENTION_DAYS` |
| retention step | scheduler name | constant `noord-retention` (the upsert key) |
| retention step | tag | `noord-assistant`, set on every assistant ticket (spec 0007 AC-10, spec 0009 AC-4) |
| retention step | state ids | `Ticket::State` rows named `closed` and `merged`, looked up inside the Rails step |
| retention step | time plan | constant: every day, every hour, minute 0 |
| retention step | job owner, notifications | `UserInfo.current_user_id = 1` while saving; `disable_notification` true; Zammad computes the next run on save |
| seed output | M (tickets matching now) | the job's condition counted by Zammad's ticket selector in the Rails step, returned in `NOORD_RESULT`, carried in `Result.RetentionDays` and `Result.RetentionMatches` |
| canary test | the values it hunts for | `seed/directory/noord.yaml` (every user), the `agenttest` fake requester, and the scripted model's texts in the test |

**Key invariants**:
- A runtime log line or exported span attribute shows a value only under an allow listed key, outside any group, as a plain short value without whitespace or `@`. Everything else is `[redacted]`. Log messages are static strings (sloglint `static-msg`, already on), and the only bridge from the standard `log` package writes a fixed message.
- No runtime log line, span, or status description carries error text. Failures are described by `cause` tokens only.
- Message content is null exactly when `content_purged_at` is set (spec 0003). Content of a conversation with activity in the last N days is never purged.
- One setting (`TICKET_RETENTION_DAYS`) drives both purges. The start events differ: the conversation's end in Postgres (its close, or its last activity when it went idle), the ticket's close in Zammad. Chat text can expire while IT still has the ticket open.
- Only tickets tagged `noord-assistant` are ever deleted by the retention scheduler. Noord's other helpdesk history is never touched.
- Exactly one `noord-retention` scheduler exists after any successful `helpdesk-seed`. Its rules file never changes the seed marker.
- The tool server and the agent gain no new Zammad rights and no new DB grants.

**Security model**:
- Compliance scope: GDPR (Noord is a Dutch employer; the data is about its staff). This feature carries the storage limitation principle for chat and ticket text, and keeps personal data out of operational telemetry.
- The retention pass runs as `noord_agent` with its existing column grants. The Zammad scheduler is created by `cmd/seed` through the Rails runner (the admin path, like the other seed steps) and runs inside Zammad as its own job. No running binary holds a credential that can delete tickets.
- Kept on purpose (spec 0003): `audit_log` and `approvals` (Slack ids, account names, allow listed arguments) for the whole engagement, and `conversations` ids and account names. Those rows hold no names, emails, or ticket text. The runbook (AC-11) and the #22 data processing note state this.
- Not in our control, so documented: Slack history (the free plan sets no per channel retention; the IT post already masks names and departments, spec 0009), the model provider's API data retention, and Zammad's own Rails logs.
- Audit: retention is our own housekeeping, not an action on a client system, so it leaves `retention_purged` lines, not `audit_log` rows. The agent has no INSERT on `audit_log` (spec 0003). Ticket deletion is the one irreversible step, so the seed step prints how many tickets match now, before the first scheduled run.

**Observability**:
- New lines: `retention_purged` (`kind`, `rows`), `retention_purge_failed` (`kind`, `cause`), `telemetry_export_failed` (`cause`), `http_server_error` (no attributes).
- Changed lines: the five listener call sites carry `cause`, not `error`.
- A `[redacted]` value in production logs means a key is missing from the list or a value broke the value rules. It is a bug to fix, not a leak.

**Configuration required**:
- `TICKET_RETENTION_DAYS` (default `30`, 1 to 365, in `config.Common`): days after a conversation or ticket ends before its text is purged. The agent reads it for the retention pass, and `seed helpdesk` reads it for the scheduler. The tool server validates it but does not use it.

**Critical test scenarios**:
- Handler: an allow listed key with a token, an unknown key, an allow listed key inside a group, an allow listed key holding an `error`, a struct, an email, and a two word name, and a `With` attribute: only the first and the `With` (if allowed) print as is; the rest print `[redacted]`; `time`, `level`, `msg`, `service` pass, verifies **AC-1**.
- Source scan: a fixture package with `logger.Info("x", "user", v)`, a non literal key, a `"msg"` key, `attribute.String("email", v)`, and `tracer.Start(ctx, name)` fails with each line named; `http.Error(w, "unauthorized", 401)` does not trip it; the real tree passes, verifies **AC-2**.
- Causes: a dedup failure returning a `pgconn.PgError` logs `cause` `postgres_<sqlstate>` and no `error` key; a `poster.Check` failure and a `ConnectionErrorEvent` log `slack_*` tokens; a TLS handshake error on the HTTP server logs only `http_server_error`; `TestFailureCausesAreFixedTokens` stays green, verifies **AC-3**.
- Span wrapper: a span with an allow listed attribute, an `email` attribute, a `RecordError` event, a status description, a link with attributes, and scope attributes reaches an in memory exporter with `[redacted]`, no events, an empty description, a bare link, and no scope attributes, and the original span is unchanged; a burst of exporter errors logs one `telemetry_export_failed` with only `cause`; tests restore the global error handler, verifies **AC-4**.
- Canary: unlock and refusal flows in each binary produce no seed name, email, username, Slack user id, department, message text, or ticket text in any string value, and no `[redacted]`; a surname that equals a token (such as `Post` against `kind` `post`) does not false match, verifies **AC-5**.
- Config: unset gives 30; `0`, `366`, and `abc` stop the agent, the tool server, and seed helpdesk; the `config_test` case that expected 0 now expects 30, verifies **AC-7**.
- Retention (DB test, `TEST_DATABASE_URL`, fixed clock): conversations closed 31 days ago (purged), closed 29 days ago (kept), open with activity 31 days ago (closed as `resolved` with `closed_at` = `updated_at`, then purged in the same pass), open with activity yesterday (kept); `rows` per kind matches; a second pass changes 0 rows. Unit test with the `agenttest` fake: a `PgError` gives `retention_purge_failed` with `postgres_<sqlstate>` and the next short tick succeeds; a canceled context ends the loop with no failure line; the loop runs with Slack unconfigured, verifies **AC-8**.
- Scheduler upsert: fresh seed writes the marker, then creates `noord-retention` with N; rerun with another N takes the already seeded path and updates it (one job); adding `retention.rb` leaves the marker unchanged; needs reset changes nothing; the output line appears on both paths, verifies **AC-9**.
- Live Zammad: the four tickets of AC-10 after one forced run, verifies **AC-10**.
- Auth/permission: a DB test as `noord_agent` shows `DELETE` on `conversation_messages` and `conversations` is still denied; the agent's and tool server's Zammad tokens are unchanged, verifies **AC-8**, **AC-9**.

## Build plan

Tracer Bullet: the redaction nets go first, because they guard every flow already in place and every flow still to come ("lands before more data flows"). Then retention, our copy first, then Zammad's.

1. **Shared cause tokens and no error text.** Move the generic half of `internal/agent/cause.go` and `Causer` to `internal/cause` (`cause.Of`; it imports pgx's `pgconn` only, not the Anthropic or MCP SDKs). `internal/agent`'s `cause()` keeps its own checks (`not_open`, `anthropic_<status>`, `mcp_rpc_<code>`) in today's order and delegates the rest. Point `internal/slack/adapters.go` and its `var _` assertion at `cause.Causer`. Wrap `poster.Check` errors with `wrapErr`, classify `ConnectionErrorEvent` through `wrapErr(e.ErrorObj)`, and switch the five listener call sites to `cause`. Replace `internal/server`'s `ErrorLog` with a writer that logs the fixed `http_server_error` line, satisfies **AC-3**.
2. **Log net.** The unexported list, `logging.Allowed`, `logging.AllowedKeys`, `logging.Redacted`, and the `ReplaceAttr` value rules in `logging.New`. The unexported plain handler inside `cmd/seed` for its three call sites, with the seed output tests updated. The typed source scan test, satisfies **AC-1**, **AC-2**.
3. **Span net.** `telemetry.NewRedactingExporter` (embed `sdktrace.ReadOnlySpan`, override `Attributes`, `Events`, `Status`, `Links`, `InstrumentationScope`, returning new slices), the `logger` parameter on `Setup` in both mains, and the throttled error handler. Confirm that slack-go, Socket Mode, the MCP Go SDK, the Anthropic SDK, and pgx have no logger wired (none is today) and record that in the nested `AGENTS.md` files, satisfies **AC-4**.
4. **Canary.** The per binary canary tests over unlock and refusal, loading `seed/directory/noord.yaml` and matching string values only. Keep the existing hygiene tests (spec 0007 AC-20, spec 0008 AC-17, spec 0009 AC-16), satisfies **AC-5**.
5. **Setting and the Postgres retention pass.** `TICKET_RETENTION_DAYS` default and the shared range check in the three loaders, `.env.example`, and the `config_test` update. `CloseIdleConversationsBefore` in `db/queries/conversations.sql` plus `make gen`. `Store.CloseIdleConversations` and `Store.PurgeMessageContent` (pgstore and the `agenttest` fake), and `internal/agent/retention.go` started unconditionally in `cmd/agent`'s errgroup, satisfies **AC-7**, **AC-8**.
6. **Zammad scheduler.** First probe and record in `verify.md`: that a merged ticket has a close time, that the scheduler's relative date condition takes days, the tag operator name, the delete action payload, and that the compose `zammad-scheduler` service runs jobs. If a probe fails, use `last_close_at` or an hours based range and say so in `verify.md`. Then `seed/helpdesk/rails/retention.rb` (upsert the `Job` by name, count the matches), its exclusion from `Marker`, `StepRetention` after the marker (fresh) and after `checkToken` (already seeded) in `Seeder.Run`, `Seeder.RetentionDays` and the `Result` fields, the output line in `cmd/seed/helpdesk.go`, the seed and compose test fakes for the new step, and the live test, satisfies **AC-9**, **AC-10**.
7. **Runbook, live check, docs.** `docs/runbooks/data-retention.md` with the check commands; the live run with `tee` recorded in `verify.md`; nested `AGENTS.md` facts for `internal/logging`, `internal/telemetry`, `internal/cause`, and `internal/seed`. Tick or answer the #12 follow ups in specs 0003, 0007, and 0009, satisfies **AC-6**, **AC-11**.

## Consequences

**Positive**:
- Privacy in logs and traces becomes structural: a new key fails CI, and a wrong value prints `[redacted]`, never the value.
- Both copies of the text (our Postgres, Zammad) expire on one stated setting, which a data processing note can state plainly, and abandoned threads no longer keep text forever.
- No new credential, grant, table, or running process.

**Negative / tradeoffs**:
- Every new log key or span attribute needs a list entry. That is a small tax on every future feature.
- A short plain token under an allowed key is still trusted. A username logged under `reason` would pass the value rules. The source scan, the canary, and review catch that, not the handler.
- Span status descriptions are always blank, so the static "model call failed" text goes too. The `cause` attribute carries the reason instead.
- Idle conversations close as `resolved`, so the dashboard (#18) and the eval (#20) can't tell an abandoned thread from a solved one without looking at `closed_at` = `updated_at`.
- Zammad deletion is permanent, and tickets go with their whole history. After 30 days IT can't reopen an assistant ticket. The audit log still shows the ticket id and what ran.
- Postgres keeps conversation rows with Slack ids and account names for the whole engagement, and `audit_log` cannot be purged at all. Both are deliberate (spec 0003), but they are personal data kept without a time limit.
- Slack history and the model provider's retention stay outside our control. They are documented, not enforced.
- The scheduler lives in Zammad, so a Noord admin could edit or disable it there. `helpdesk-seed` restores it on the next run, but nothing alerts in between.

**Neutral**:
- The generic half of `internal/agent/cause.go` moves to `internal/cause`. Imports change, behaviour does not.
- `cmd/seed` logs stay unredacted: it is an operator tool whose user lines are how you debug a sync.
- `telemetry.Setup` takes a logger, so both mains change one line.

## Follow-up

- [ ] #18: add the Collector redaction processor as a second layer, and check live traces of one conversation for the AC-5 values (traces are only checked in tests until a Collector exists). Count idle closes separately on the dashboard.
- [ ] #22: the data processing note reuses `docs/runbooks/data-retention.md`, including the no expiry tradeoff for `audit_log` and the conversation ids, Slack history, and the model provider's retention (and its EU region follow up from spec 0001).
- [ ] #17: in the cluster, confirm `TICKET_RETENTION_DAYS` reaches both the agent and the job that runs `helpdesk-seed`, so the two purges cannot drift.
- [ ] Spec 0003: answer its #12 follow up in place (retention pass built, default 30, idle close added). Spec 0007: tick its #12 follow up (the AC-20 hygiene test stays). Spec 0009: answer its #12 follow up (Slack documented; escalation summaries follow the ticket deletion).
