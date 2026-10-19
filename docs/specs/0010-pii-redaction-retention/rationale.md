# 0010. Rationale: PII redaction and retention

## Context

> ⚠️ Premise note: the scope says "old ticket bodies are purged", but ticket text lives in Zammad, Noord's helpdesk and system of record, not in our database. Deleting a client's helpdesk records is the client's policy call, not an engineering default. So deletion is limited to tickets the assistant created (the `noord-assistant` tag), it runs as a visible Zammad scheduler that Noord's IT person can read and edit, and the runbook names it. Noord's own ticket history stays theirs.

Noord is a Dutch employer, so GDPR applies to everything the assistant learns about its staff. Specs 0003, 0007, 0008, and 0009 already keep personal data out of the audit log, log lines, and span attributes, one call site at a time, each guarded by a hygiene test for its own flow. Nothing enforces the rule across the system. `internal/logging` hands back a plain JSON handler, the OTLP exporter sends whatever a span holds, and four Slack listener lines already log raw error text. Every slice from here on (reset links, MFA, access requests, KB answers) adds log lines and spans, so the gap grows with each feature.

Personal text is also kept with no end date. The user's message, the model's replies, tool results with display names and departments, and the ticket title, body, and escalation summary the model wrote all sit in `conversation_messages.content`. `PurgeMessageContentBefore` exists (spec 0003) but nothing calls it. The same ticket text also lives in Zammad. Escalation summaries also reach the private IT Slack channel, and the whole conversation goes to the model provider.

The forces: deny by default beats remembering, because the next feature's author is a model working from a spec. The tool server is the only holder of client credentials, and the agent must not gain new rights (AGENTS.md rules). `audit_log` is append only even to its owner (spec 0003), so it can't be part of a purge. And the solution has to work in dev, where no OpenTelemetry Collector runs until #18.

### Where personal data lives (inventory, from a read only code scan on 2026-10-19)

| Place | What | Today | After this spec |
|---|---|---|---|
| Runtime log lines | ids, counts, fixed tokens; 5 listener call sites log `err.Error()`; `http.Server.ErrorLog` passes stdlib text into `msg` | no structural guard | allow list and value rules in the handler, `cause` tokens, a fixed HTTP error line |
| Exported spans | `tool.call`, `agent.turn`, `llm.call` attributes, all tokens and counts | no filter | allow list in the exporter wrapper; no events or status text |
| `conversation_messages.content` | message text, model text and thinking, tool inputs (ticket text), tool results (names, departments) | kept forever | purged 30 days after close; idle open threads closed after 30 days without activity, then purged |
| `conversations` | Slack user id, account name, channel and thread ids | kept | kept (spec 0003) |
| `audit_log`, `approvals` | Slack ids, account names, allow listed arguments | kept, append only | kept (spec 0003) |
| `approvals.arguments` | the full call | cleared when the approval ends | unchanged |
| Zammad tickets | title, first article, notes, escalation summary | kept | assistant tickets deleted 30 days after their last close, or after the merge for a merged one |
| IT Slack channel, user DMs | escalation summaries (names masked), bot replies | workspace plan | documented |
| Model provider | the whole conversation per call | provider's terms | documented |
| MCP `_meta` | requester email, agent to tool server | not logged | unchanged |
| `cmd/seed` logs | fictional usernames during sync | operator terminal | unchanged (plain handler) |

## Options considered

### Option 1: Call site discipline and hygiene tests only

Keep the per flow rules and their tests, fix the four listener lines, call the existing purge query, and leave Zammad alone.

**Pros**: least code; nothing new to learn.
**Cons**: a new log key leaks until someone writes a test for that flow; fails the scope's done line for ticket bodies; the rule lives in reviewers' heads.

### Option 2: Structural nets plus retention where each copy lives (chosen)

A key allow list enforced in the slog handler and in a span exporter wrapper, a source scan test, a canary test, an agent purge loop for Postgres, and a Zammad scheduler for assistant tickets, both on one setting.

**Pros**: deny by default; a missing key fails CI or prints `[redacted]`, never the value; no new credential, grant, or process; works in dev without a Collector.
**Cons**: every new key needs a list entry; values under allowed keys are trusted; Zammad deletion is permanent and lives in a system Noord can edit.

### Option 3: Value pattern scrubbing and the Collector only

Regexes for emails and Slack user ids on every log value, and the Collector's redaction processor for spans (#18).

**Pros**: no list to maintain; catches an email under any key.
**Cons**: can't recognise names or ticket text; a regex on every log call; spans are unprotected until #18, and dev exports bypass the Collector entirely.

### Option 4: Tool server purge job for Zammad

Same nets as Option 2, but the tool server deletes old assistant tickets over REST and audits each delete.

**Pros**: every deletion leaves an audit row; the schedule lives in our code.
**Cons**: the tool server needs a token that can delete tickets, a wider credential on the one process that talks to client systems; more code, failure handling, and rate limiting for something Zammad does on its own.

## Rationale

Option 2 is the only one that makes privacy hold without depending on the next author remembering it, which is the force that matters most in a codebase mostly written from specs. An allow list over keys works because every earlier spec already narrowed log and span values to ids, counts, and fixed tokens; the list just makes that the enforced default. Patterns (Option 3) chase values and miss the data that matters most here, names and free text. The exporter wrapper protects spans from day one and in dev, and the Collector in #18 then adds a second layer rather than being the only one.

For retention, each copy is purged by the system that holds it. The agent already owns and can update the content column, so an hourly loop beside the Slack event purge needs no new grant. Zammad's own scheduler can delete tickets, so the tool server keeps its narrow token (Option 4 would widen the most sensitive credential in the system to save an audit row that adds little: the audit log already records the ticket id and what ran). Starting the clock at close keeps live work safe, and one setting drives both purges, so they can't drift. The build probes (2026-10-19) found that Zammad gives merged tickets no close time and that `close_at` keeps the first close, so the condition counts closed tickets from `last_close_at` and merged ones from the merge (`updated_at`); the engineer chose this over leaving merged tickets out (they would then be kept forever) during `/develop`.

Choices made during the build of this spec (each with its runner up):
- `[redacted]` replaces the value and keeps the key, so a missing list entry is visible and tests can assert none appear. Runner up: drop the attribute silently.
- Span events are dropped, not filtered: no current span uses them, and `RecordError` puts error text there. Runner up: filter event attributes through the same list.
- `cause` moves to a leaf package so `internal/slack` and `internal/telemetry` share the agent's token vocabulary. Runner up: a local classifier in each package.
- A source scan test turns a missing list entry into a CI failure with a file and line, instead of a `[redacted]` found in production. Runner up: rely on the canary only, which sees only the flows it drives.
- The Zammad scheduler is written by a Rails runner step, like `bootstrap` and `backdate`, so it needs no new REST client code or token. Runner up: Zammad's `/api/v1/jobs` endpoint with the seed admin token.
- `cmd/seed` keeps a plain handler, built inside `cmd/seed` so runtime code can't reach it: it is run by hand, its user lines are how a sync is debugged, and the users are fictional. Runner up: the same allow list with seed keys added.

Added after the cross check (a read only review by another model, 2026-10-19):
- Allowed keys also pass only plain short values (no whitespace, no `@`, at most 128 bytes, no errors or structs), and anything in a group is redacted. A key list alone would let `"cause", err` or a name under `reason` through. Runner up: keys only, trusting review.
- The source scan uses type information, so `http.Error` and similar calls don't trip it, and it also rejects non literal keys, the built in keys, and non literal span names. Runner up: a plain syntax match, which false fails on the real tree.
- `http.Server.ErrorLog` writes a fixed line: the standard library's text (TLS errors, panic values) would otherwise land in `msg`, which the log handler passes as is.
- Idle open conversations close as `resolved` after N days without activity, with `closed_at` set to the last activity, so the same pass purges them. Otherwise an abandoned thread keeps its text forever, a hole in the storage limitation claim. Runner up: document the gap.
- `rails/retention.rb` is left out of the seed marker, so adding it doesn't force every existing Zammad into a reset, and the day count can change without one. Runner up: accept a one time reset.
- The seed step prints how many tickets the condition matches now, since deletion is the only irreversible step here. Runner up: ship the scheduler disabled until someone turns it on.
