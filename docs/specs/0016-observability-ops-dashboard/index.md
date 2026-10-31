# 0016. Observability and the ops dashboard: one trace per conversation, a redacting Collector, Tempo, and a Grafana dashboard fed by a locked down Postgres view layer

**Date**: 2026-10-31
**Status**: Proposed

## Summary

This spec adds the eyes to the system. Every Slack conversation becomes one trace you can open and follow, from the first message through each model call and tool call, with personal data stripped twice (once in our code, once in a Collector, the service that receives and filters traces). A Grafana dashboard, reachable only over Tailscale and behind a login, shows Noord's IT person the last 24 hours at a glance: conversations handled, escalated, refused and abandoned, approvals waiting, errors, whether writes are on, and cost. The dashboard reads Postgres through a small set of views that hold no names, ids of people, or message text. Three small additions to the data make the numbers honest: a trace id and an outcome on each conversation, a table of agent failures, and a one row record of the kill switch.

## Rationale

Reasoning, the options weighed, the smaller calls with their runners up: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As Noord's IT person (Ruben), I want one page that says whether the assistant is working, what needs me (approvals waiting), and what it cost, so that I can check it in ten seconds without reading logs.
- As the engineer, I want to open one conversation and see every model call and tool call in order, with no personal data in it, so that I can explain a bad answer and show the trace in the video.
- As the person accountable for the safety claim, I want the dashboard and the traces to be unable to leak a name, an email, a Slack user id, or message text, so that watching the system never weakens the privacy rules.
- As the engineer, I want abandoned threads counted apart from solved ones, so that the resolution rate the eval reports is not flattered by idle closes.

**Acceptance criteria**:

*Traces*
- **AC-1**: One trace per conversation. `conversations` gains `trace_id` (32 lowercase hex characters, nullable). Before it starts the `agent.turn` span, `runTurn` asks the store for the thread's trace id (`Store.ThreadTrace(channel, thread_ts)`, empty when the thread has no conversation yet or the row has no id). When empty it generates a random 16 byte id (`crypto/rand`, never all zero). It then starts `agent.turn` as a child of a remote parent span context built from that trace id and a fixed parent span id (the last 8 bytes of the trace id, with the lowest bit set if they are all zero), as `SpanContextConfig{Remote: true, TraceFlags: FlagsSampled}` so the default parent based sampler keeps it. `UpsertConversation` stores the id on insert and fills it with `COALESCE` when the existing row has none, so a row from before this spec gets one on its next message. Every later turn of the thread therefore lands in the same trace, as a sibling `agent.turn`. A turn that ends before the upsert (an empty message) leaves an unused id behind; that is accepted. Turns are serial per thread, so there is no race. If `ThreadTrace` fails on an existing thread, that turn lands in a stray trace (the stored id is kept); this is accepted and the runbook says so.
- **AC-2**: Everything a turn does is in its trace. This covers calls that a turn starts. Code initiated calls with no turn above them (the restart sweep, the approval settle, `closeByCode`) run in their own traces, and their `audit_log.trace_id` is that trace, not the conversation's. The `llm.call` spans are children of `agent.turn` (already true). The tool server's `tool.call` spans join through the `traceparent` header the MCP client already sends (`internal/mcpclient`, `internal/mcpserver`), and each `audit_log.trace_id` equals the conversation's `trace_id`. A test drives two turns of one scripted conversation with a tool call in each and, through an in memory exporter, finds one trace id on both `agent.turn` spans, every `llm.call`, every `tool.call`, and the audit rows.
- **AC-3**: Approvals and reset links are separate traces, by design. The `approval.decide`, `approval.cancel`, `approval.tick`, `reset.submit`, and the executed `tool.call` of an approved action stay in their own traces (they start from a Slack click or a browser request, with no turn above them). Each of these spans that has an approval id carries it as the attribute `approval_id` (already on the allow list), so the dashboard can link an approval to its trace by `{ span.approval_id = "<id>" }`. The runbook states that an approved call is not inside the conversation's trace.
- **AC-4**: Two layers of redaction. The Collector runs the contrib `redaction` processor with `allow_all_keys: false` and an `allowed_keys` list equal to `logging.AllowedKeys()` plus the fixed OpenTelemetry keys the pipeline needs (`service.name`), and with `blocked_values` patterns for an `@` and for a Slack user id (`U` or `W` followed by 8 or more capital letters and digits). A drift test (`internal/telemetry`) parses the Collector config and fails when its `allowed_keys` differ from that set, so adding a key to the log allow list and not to the Collector fails CI. A live check on the compose stack sends the spec 0010 AC-5 canary values (a name, an email, a Slack user id, message text) through a Slack free scripted conversation and shows none of them in Tempo, in the Collector's debug output, or in any dashboard panel; the result goes in `verify.md`.
- **AC-5**: Wiring and retention. The chart (and the compose profile) set `OTEL_EXPORTER_OTLP_ENDPOINT` for `cmd/agent` and `cmd/toolserver` to the Collector's OTLP HTTP port. The Collector sends to Tempo. Every trace is kept (no sampling). Tempo keeps blocks for 168 hours (7 days) and nothing else is backed up. With the Collector or Tempo down, a turn still completes and the only trace of the fault is the existing throttled `telemetry_export_failed` log line; no readiness probe depends on telemetry.

*Data*
- **AC-6**: Conversation outcome. `conversations` gains `outcome` (`resolved`, `escalated`, `refused`, `abandoned`), null exactly while the conversation is open, set in the same statement as `status` and `closed_at`. A database CHECK enforces the pairing: `open` with null; `escalated` with `escalated` or `refused`; `resolved` with `resolved` or `abandoned`. Code sets it so: a closed ticket (`ticket_update` to `closed`) or a sent reset link gives `resolved`; an `escalate` whose `reason` is `privileged_request` gives `refused`, wherever it comes from (the model, the backstop, the failure path, the sweep); any other `escalate`, the gate, directory match and hourly cap refusals (`refuse()`), and every other failure path or sweep close give `escalated`; the idle close (`CloseIdleConversations`) gives `abandoned`. Each of these is a named call site:
  - The model's own `escalate`: `statusChangeFor` takes the call's `reason` argument as a new parameter, and the loop passes it.
  - The failure path and the sweep: the outcome follows the reason they choose, not whether the `escalate` call then succeeds. The sweep works out the reason from the stored history before it claims the row, then claims with the outcome already chosen (the claim only updates `open` rows, so the outcome cannot be fixed afterwards).
  - The approval settle (`SettleCard`, spec 0012 and 0013): it takes a full `StatusChange` with an `Outcome`; `resolved` gives `resolved` and `escalated` gives `escalated`. A denied access request closes `resolved`, as today.
  The migration adds the column, backfills closed rows from `status` (`resolved` to `resolved`, `escalated` to `escalated`), and only then adds the CHECK. An idle close from before this spec cannot be told apart and stays `resolved`. During a rolling deploy old pods still write a close with no outcome, so a `BEFORE INSERT OR UPDATE` trigger fills a null `outcome` from `status` for a closed row (`resolved` to `resolved`, `escalated` to `escalated`); the trigger stays, and new code always sets the outcome explicitly. `StatusChange` gains an `Outcome` and `SetConversationStatus` fills it.
- **AC-7**: Failures are recorded where the dashboard can read them. A new table `agent_failures` holds `id`, `conversation_id` (nullable FK), `stage` (`llm`, `tool`, `slack`, `sweep`), `cause` (a token from `cause.Of`, at most 40 characters of `[a-z0-9_]`), `occurred_at`. The agent inserts one row when: a model call fails after its retries (`llm`); a tool call fails in transport or comes back unanswered (`tool`; never for a tool result with `IsError`, because the tool server already wrote that as an audit `error` row and the dashboard would count it twice); posting to Slack fails after its retry (`slack`, at `t.post`, `postNoUnfurl`, and `a.post` as the sweep and the settle use it); the restart sweep escalates an interrupted conversation (`sweep`). `conversation_id` is null when there is none. Rows hold no error text and are kept on purpose (no personal data, tiny). `noord_agent` gets `INSERT` only. The insert runs on a detached context with a 2 second timeout; a failure is logged as `agent_failure_write_failed` with `cause` and never fails or delays the turn.
- **AC-8**: The kill switch is visible. A new one row table `kill_switch_state` holds `id` (fixed true, primary key), `mode` (`read_only` or `read_write`), `changed_at`, and `checked_at`. The kill switch gains `Mode() (string, error)` next to `AccountChangesAllowed()`. For `FileSwitch` an unreadable file records `read_only` (what is actually enforced) and returns the error, which is logged as `kill_switch_sync_failed` with `cause`; for the static switch (no file, Compose and dev) the mode comes from the `READ_ONLY` setting. The tool server calls it every `KILL_SWITCH_SYNC_INTERVAL` and at start, always sets `checked_at = now()`, and sets `mode` and `changed_at` only when the mode differs from the row. On the first insert `changed_at` means "first seen", not the real flip time. `noord_toolserver` gets `SELECT, INSERT, UPDATE` on this table only. The dashboard shows the mode, and "unknown" when `checked_at` is older than 3 minutes (more than three times the longest allowed interval of 60 seconds).
- **AC-9**: A view layer, and nothing else, for Grafana. A migration creates schema `ops` and the views in the interface table below, owned by `noord_owner`, and a role `noord_grafana` (login, no inherit, created in `db/roles.sql` for compose and by CNPG `managed.roles` from a Secret in the cluster) with `USAGE` on `ops` and `SELECT` on its views only. The views are plain owner rights views, never `security_invoker`, or Grafana would be denied the tables underneath. The role has `statement_timeout` 10s and `CONNECTION LIMIT` 5, and the migration revokes `TEMP` on the database from PUBLIC so the role cannot create even a temporary table. In the cluster, the migration Job runs after the CNPG managed role exists (wave order, with the Job waiting on it); a missing role fails the Job with a clear message instead of creating one. A DB test as `noord_grafana` shows: every `ops` view readable; `SELECT` on any `public` table denied; no write of any kind, no temporary table. A second test reads `information_schema.columns` for the `ops` schema and fails if any column name contains `slack`, `requester`, `account`, `argument`, `result`, `content`, `email`, or `name` other than `tool_name`. Adding a view needs a reviewed migration; nothing Grafana runs can widen its own access.

*Dashboard*
- **AC-10**: The dashboard "Noord assistant, last 24 hours" shows, from the top: (1) a tile row: conversations closed by outcome (`resolved`, `escalated`, `refused`, `abandoned`, abandoned shown apart and never added to resolved), approvals waiting with the age of the oldest, errors (the `error` outcome rows of `audit_log` plus `agent_failures`) with the split by stage, the kill switch mode with its `changed_at`, and cost in US dollars plus cost per conversation; (2) a table of pending approvals (tool, tier, approver kind, age, no requester); (3) a bar panel of tool calls by tool and outcome; (4) a table of the 50 most recent conversations (id, started, status, outcome, model calls, cost, duration, and a link that opens the trace in Tempo by `trace_id`). The default range is `now-24h` with Grafana's range picker for any other; the dashboard timezone is `Europe/Amsterdam`. Cost is `cost_microusd / 1e6` summed over `ops.llm_calls` rows created in the range; cost per conversation divides by the distinct conversations with usage in the range, and shows "no data" rather than 0 when there are none. The pending approvals table and its tile show the current state, whatever the range (`status = 'pending'` and `expires_at > now()`). The recent conversations table orders by `created_at` descending, ignores the range, joins cost with a left join, and shows no link when `trace_id` is null. `duration_s` is `closed_at - created_at` in seconds, null while open. The dashboard refreshes every 30 seconds. The trace link is a field data link on the Tempo datasource's fixed `uid`, with query `${__value.raw}`, not a hand built URL.
- **AC-11**: The dashboard is code. The Tempo and Postgres datasources and the dashboard JSON are provisioned from files kept under `deploy/helm/noord/files/observability/` (`datasources.yaml`, `dashboards/noord-ops.json`, `collector.yaml`), because `.Files.Get` can only read inside the chart directory; Compose mounts the same path. The chart's `noord-agent` Application owns the ConfigMaps that the vendored Grafana chart mounts (`dashboardsConfigMaps`, no sidecar). The datasource file takes its host, password, and `sslmode` from environment variables (`$__env{...}`) so it stays one file. Nothing is edited by hand in the UI. Grafana has one admin login whose password comes from a sealed deploy only Secret, anonymous access off, sign up off, and its ingress uses `ingressClassName: tailscale`. The Postgres datasource connects as `noord_grafana` with a password from a second sealed Secret, host `<cluster>-rw.<namespace>.svc`, and `sslmode=verify-full` with the CNPG CA mounted (the existing `_helpers.tpl` CA volume). Compose uses `sslmode=disable`.

*Install and operate*
- **AC-12**: In the cluster, the Collector (contrib image, pinned), Tempo (single binary, one PVC on the default StorageClass), and Grafana come from their official Helm charts, pinned and vendored, rendered as component Applications in `noord-agent` (spec 0015 pattern: sync waves, `skipTests`, finalizer, Pod Security `restricted`, non root, read only root filesystem). For Grafana that means the chart's `initChownData` off, `emptyDir` for `/var/lib/grafana` and `/tmp` (no persistence: users and state are code, and a changed admin Secret takes effect on restart), update checks and analytics off (egress is denied), and DNS egress allowed. The Collector chart's own default config is not used: `configMap.create: false` and our ConfigMap built from the base file, so the drift test checks the config that runs. The default deny network policy gains exactly these allows: agent and tool server to the Collector (4318); the Collector to Tempo (4317); Grafana to Tempo (3200) and to Postgres (5432); the Tailscale ingress to Grafana. `observability.enabled` in the cluster values defaults to true; `make install` installs it with no extra step, and `make uninstall` removes it, including the Tempo volume claim (traces are disposable).
- **AC-13**: A Compose profile `observability` starts the same Collector, Tempo, and Grafana with the same base config files and the dashboard JSON. Compose adds one overlay file passed as a second `--config` (the files are merged) that turns on the `debug` exporter for the AC-4 live check; the cluster never has it. Redaction runs with `summary: silent`, because any other value adds `redaction.*` keys that are not on the allow list. `make obs-up` starts it and prints the Grafana address; `make dev` is unchanged and sends no traces unless the profile is up.
- **AC-14**: Privacy. No log key or span attribute is added beyond the allow list; the new log lines `agent_failure_write_failed` and `kill_switch_sync_failed` carry only `cause`. The Grafana and Collector pods log no span content (Collector `debug` exporter off in the cluster). The existing canary test (spec 0010) is extended to the conversation trace: its canary strings appear in no exported span.
- **AC-15**: `KILL_SWITCH_SYNC_INTERVAL` (tool server, default 30s, 5s to 60s) is declared once in `internal/config` and in `.env.example`; `LoadToolServer` refuses a value out of range, naming the setting. Grafana's own settings are deploy only (chart values and sealed Secrets) and are not `internal/config` fields.
- **AC-16**: A runbook `docs/runbooks/observability.md` says how to read each tile and what counts as a problem, how to find one conversation's trace, that approved actions are separate traces (AC-3), how to reset the Grafana admin password (change the sealed Secret, restart the pod), what to do when Tempo's disk fills, the 7 day retention, that a turn whose trace lookup failed lands in a stray trace, and that the idle close back dates `closed_at`, so "abandoned in the last 24 hours" reads near zero and counts move after the fact. The live proof on k3sprox is recorded in `verify.md`: a real Slack conversation with a tool call opens as one redacted trace, and the dashboard shows it within a minute of closing.

## Decision

**Chosen option**: Option 1: the stack spec 0001 already named (an OpenTelemetry Collector with a redaction processor, Tempo, Grafana OSS), one trace per conversation through a stored trace id, and a Postgres view layer read by a view only role.

Traces answer "what did it do in this conversation"; Postgres views answer "how is it doing", so the dashboard keeps working when Tempo is down and holds no personal data. The three new data items (conversation outcome, failure rows, kill switch row) exist because the questions the IT person asks have no durable answer today.

**Implementation skills**: `opentelemetry` · `tempo` · `grafana-oss` (`grafana/skills`, `.claude/skills/<name>/`) · `golang-database` · `golang-security` · `golang-concurrency` (`samber/cc-skills-golang`, `.claude/skills/<name>/`, the sync loop and the failure insert) · `senior-kubernetes-engineer` (`~/.claude/skills/senior-kubernetes-engineer/`, the charts, policies, Pod Security). Open each only when building in its area.

## Feature design

**Data model sketch** (the target; migration `00007` adds only `trace_id`, `00008` the rest):

| Entity | Change | Fields |
|---|---|---|
| `conversations` | add | `trace_id text` null, CHECK `~ '^[0-9a-f]{32}$'`; `outcome text` null with the pairing CHECK of AC-6; index on `(closed_at)` already exists |
| `agent_failures` | new | `id bigint identity PK`, `conversation_id bigint null FK conversations`, `stage text` CHECK in (`llm`,`tool`,`slack`,`sweep`), `cause text` CHECK `~ '^[a-z0-9_]{1,40}$'`, `occurred_at timestamptz default now()`; index on `occurred_at` |
| `kill_switch_state` | new | `id boolean PK default true CHECK (id)`, `mode text` CHECK in (`read_only`,`read_write`), `changed_at timestamptz`, `checked_at timestamptz` |

Relationships: `conversations` 1:N `agent_failures`. `kill_switch_state` has no relations (one row). The `ops` views and the `noord_grafana` role are in AC-9.

Grants: `noord_agent`: `INSERT` on `agent_failures`; its existing `UPDATE (...)` column list on `conversations` gains `outcome` and `trace_id`. `noord_toolserver`: `SELECT, INSERT, UPDATE` on `kill_switch_state`. A `BEFORE` trigger on `conversations` fills a null `outcome` for a closed row (AC-6). `noord_eval` is unchanged. A DB test shows `DELETE` on the new tables is denied to every app role.

**State transitions** (`conversations`, extends specs 0008 and 0009; terminal stays final):
- `open` (outcome null) to `resolved` with `resolved` (ticket closed, reset link sent) or `abandoned` (idle close).
- `open` to `escalated` with `escalated`, or with `refused` when the `escalate` reason is `privileged_request`.
- No other pairing is storable.

**Interface surface.** This feature adds no HTTP endpoint and no MCP tool. Its interfaces are the views Grafana queries, two Store methods, and the config below.

| View (schema `ops`) | Columns | Built from |
|---|---|---|
| `conversation_outcomes` | `conversation_id`, `created_at`, `closed_at`, `status`, `outcome`, `duration_s`, `trace_id` | `conversations` |
| `llm_calls` | `conversation_id`, `created_at`, `cost_usd`, `input_tokens`, `output_tokens` | `llm_usage`, one row per call (the range tiles) |
| `conversation_cost` | `conversation_id`, `model_calls`, `cost_usd` | `llm_usage` grouped (the recent conversations table) |
| `approvals_open` | `approval_id`, `tool_name`, `risk_tier`, `approver_kind`, `status`, `created_at`, `expires_at` | `approvals` |
| `tool_calls` | `occurred_at`, `tool_name`, `risk_tier`, `outcome`, `trace_id` | `audit_log` rows with `kind = 'outcome'` |
| `failures` | `occurred_at`, `stage`, `cause`, `conversation_id` | `agent_failures` |
| `kill_switch` | `mode`, `changed_at`, `checked_at` | `kill_switch_state` |

| Store method | Inputs | Output | Errors |
|---|---|---|---|
| `ThreadTrace` | channel id, thread ts | trace id, or empty | store unavailable (the turn proceeds with a fresh id) |
| `SetConversationStatus` (existing) | id, `StatusChange` incl. `Outcome` | conversation | `ErrNotOpen`, constraint violation |

**Value sourcing**:
| Action | Value produced or displayed | Source |
|---|---|---|
| Start a turn | trace id | `conversations.trace_id`, else `crypto/rand` (AC-1) |
| Start a turn | parent span id | derived from the trace id (AC-1) |
| Close a conversation | `outcome` | the tool that closed it (`ticket_update` closed, `password_reset_link`, `escalate` with its `reason` argument), `retention` idle close, `sweep`, `failure`, gate refusals: fixed per path (AC-6) |
| Record a failure | `stage`, `cause` | the failing call site (fixed), `cause.Of(err)` |
| Dashboard handled count | conversations in range | `ops.conversation_outcomes.closed_at` within the Grafana range |
| Dashboard errors | count | `ops.tool_calls.outcome = 'error'` plus `ops.failures`, in range (no overlap: the `tool` stage excludes `IsError` results) |
| Dashboard cost | USD | `ops.llm_calls.cost_usd` by `created_at` in range (from `llm_usage.cost_microusd`, computed at call time by `eval/pricing.yaml`, spec 0003) |
| Dashboard time | range and timezone | Grafana range picker, dashboard timezone `Europe/Amsterdam` |
| Dashboard approvals age | seconds | `now() - ops.approvals_open.created_at` |
| Kill switch tile | mode, since, unknown | `ops.kill_switch`; unknown when `checked_at` older than 3 minutes |
| Trace link | Tempo URL | the Tempo datasource uid plus `ops.conversation_outcomes.trace_id` |

**Key invariants**:
- `outcome` is null if and only if `status = 'open'`; the pairing in AC-6 always holds (CHECK).
- `noord_grafana` can read nothing outside `ops`, and `ops` columns never include a person, an argument, a result, or message text (AC-9 tests).
- Telemetry never blocks or fails a turn (AC-5, AC-7).
- `audit_log` and `approvals` are read, never changed, by anything here.

**Security model**: Grafana is reachable only through the Tailscale ingress and needs the admin login (two layers). Its database role sees views of counts and states only. Traces carry ids, fixed tokens, counts, and flags (the allow list), are redacted in code and again in the Collector, and live 7 days. Compliance scope: GDPR (Slack user ids and names are personal data); the design keeps them out of every telemetry and dashboard surface by construction, which is what the data processing note can state. The audit log stays the compliance record; this feature adds no writer to it.

**Configuration required**:
- `KILL_SWITCH_SYNC_INTERVAL`: tool server, default `30s` (AC-15).
- `OTEL_EXPORTER_OTLP_ENDPOINT`: existing; the chart sets it to the Collector.
- Chart values `observability.enabled`, `observability.tempo.retention` (`168h`), `observability.tempo.storage` (PVC size), and the pinned chart versions and image tags.
- Deploy only sealed Secrets: `GRAFANA_ADMIN_PASSWORD`, `GRAFANA_DB_PASSWORD` (also the CNPG managed role Secret for `noord_grafana`). Prerequisite: none outside the cluster.

**Critical test scenarios**:
- Happy path: two turns and two tool calls of one conversation give one trace id across spans and audit rows, verifies **AC-1**, **AC-2**.
- Happy path: a closed ticket, a privileged refusal, an idle close, and an ordinary escalation store `resolved`, `refused`, `abandoned`, `escalated` and the dashboard queries count them apart, verifies **AC-6**, **AC-10**.
- Failure case: Collector unreachable, a turn completes and logs only `telemetry_export_failed`, verifies **AC-5**.
- Failure case: `agent_failures` insert fails (database error), the turn still replies and logs `agent_failure_write_failed`, verifies **AC-7**.
- Failure case: the switch file is unreadable, `read_only` is recorded and `kill_switch_sync_failed` is logged; a stopped tool server turns the tile "unknown" after 3 minutes, verifies **AC-8**.
- Rolling deploy: an old style close with no outcome is accepted and gets the matching outcome, verifies **AC-6**.
- Closing paths: the approval settle, the sweep with a blocked history, and the model's own `escalate` with `privileged_request` each store the right outcome, verifies **AC-6**.
- Auth and permission: as `noord_grafana`, `public` tables and any write are denied; no sensitive column name in `ops`, verifies **AC-9**.
- Privacy: canary values reach no span, no Tempo query, no panel, verifies **AC-4**, **AC-14**.
- Drift: a key added to `logging` and not to the Collector config fails CI, verifies **AC-4**.

## Build plan

Tracer Bullet, as the project default: first a thin thread that shows a real trace in Grafana on a laptop, then the conversation join, then the data and the dashboard, and the cluster last.

1. **The thread on Compose.** Profile `observability` with the Collector (pass through only), Tempo, and Grafana with the Tempo datasource, all config under `deploy/helm/noord/files/observability/` from the start; `make obs-up`; point `make dev` at it and open one existing `agent.turn` trace in Grafana. Satisfies **AC-5** (wiring), **AC-13**.
2. **One trace per conversation.** Migration `00007` (`trace_id`), `make gen`, `ThreadTrace`, the `runTurn` parenting, the grant, and the two turn test with an in memory exporter. Satisfies **AC-1**, **AC-2**.
3. **Approval and reset span attributes.** Add `approval_id` where a span has one. Satisfies **AC-3**.
4. **Collector redaction.** The `redaction` processor with the allowed keys and blocked patterns, the drift test, the canary live check on Compose. Satisfies **AC-4**, **AC-14**.
5. **The data.** Migration `00008`: `outcome` and the backfill, `agent_failures`, `kill_switch_state`, schema `ops` and the views, the `noord_grafana` role and grants; `db/roles.sql`; queries and `make gen`; the DB tests (pairing CHECK, grants, column name scan). Satisfies **AC-6**, **AC-7**, **AC-8**, **AC-9** (storage side).
6. **Agent writes.** `StatusChange.Outcome` at every closing path, the idle close, the `agent_failures` inserts at the four call sites. Satisfies **AC-6**, **AC-7**.
7. **Tool server writes.** The kill switch sync loop, the setting and its range check, `.env.example`. Satisfies **AC-8**, **AC-15**.
8. **The dashboard.** `noord-ops.json` with every panel, the provisioning files, queries tested against the seeded DB in Compose. Satisfies **AC-10**, **AC-11**.
9. **The cluster.** Vendored charts, three component Applications, Secrets and the CNPG role, network policy allows, Pod Security fit, Tailscale ingress, cluster values, `make install` and `uninstall` checks. Satisfies **AC-5**, **AC-11**, **AC-12**.
10. **Runbook, nested docs, live proof.** `docs/runbooks/observability.md`, updates to `internal/telemetry/AGENTS.md` and `deploy/AGENTS.md`, the live Slack conversation on k3sprox in `verify.md`, a journal entry for anything that broke. Satisfies **AC-16**, and closes **AC-1**, **AC-4**, **AC-10** live.

## Consequences

**Positive**:
- The scope's done line becomes literally checkable: one conversation, every call, redacted; the last 24 hours on one page.
- The dashboard survives a Tempo outage and can never show personal data, because it only reads views.
- The eval (#20, #21) gets the same outcome vocabulary and an honest resolution rate, with abandoned threads apart.
- The kill switch state and the failures are finally visible without reading logs.

**Negative / tradeoffs**:
- Three more workloads in `noord-agent`, one more PVC, and three more charts to bump by hand. Tempo's disk is a new thing that can fill.
- Every new log key or span attribute now needs a list entry in two places (code and the Collector config); the drift test makes that a failing build, not a surprise.
- A trace with no real root span: Tempo shows the root as "not yet received" because the conversation parent is synthetic. Search by trace id works; searching by root span name does not.
- Approved actions and reset submissions are not inside the conversation's trace (AC-3), so "every tool call in one trace" holds for the agent's own turns only; the IT person follows an approval by its `approval_id`.
- Idle closes from before this spec stay `resolved`, so the first days of history flatter the resolution count.
- A failed `agent_failures` insert is only a log line, so the dashboard can under count errors exactly when the database is the problem. The audit and trace views still show the rest.
- The IT person must keep one more admin password. The Postgres credential for Grafana is a new secret holder (view only).

**Neutral**:
- Spec 0015's network allow list, component list, and sealed Secret table gain entries (see Follow-up). `internal/telemetry` and `internal/logging` stay as they are.
- Grafana's own state (users, preferences) is not backed up; the dashboard and datasources are code.

## Follow-up

- [ ] Amend spec 0015: add the three components to its component list, the new network allows to its allow list, and the two deploy only Secrets to its Secret table. Run `/architect` update on 0015 or note it in the build.
- [ ] Amend spec 0003 (data model) with the new columns and tables, and spec 0010's follow up list: tick the Collector redaction layer and the idle close counting (this spec).
- [ ] Decide later whether approved actions should join the conversation trace (needs the conversation's trace id carried on the `approvals` row). Not needed for the scope's done line.
- [ ] Connect `grafana/mcp-grafana` (named in spec 0001) in your own MCP settings once the stack is up; it is not required to build this.
- [ ] Eval (#20) should read `conversations.outcome` instead of inferring from `status`.
- [ ] The `tempo`, `opentelemetry`, and `grafana-oss` skills are installed and listed in `AGENTS.md`; no skill is missing.
