# Verify: observability and the ops dashboard · spec 0016 · updated 2026-10-31
_Steps derived from spec 0016 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones. A ticked box was run on 2026-10-31 on a laptop (Compose and `go test`); the cluster boxes are still open._

## Commands
- [x] `go test -race -run 'TestConversationIsOneTrace|TestTurnSurvivesThreadTraceFailure' ./internal/agent/` and `TEST_DATABASE_URL=... go test -race -run TestPgThreadTrace ./internal/agent/` → one trace id on both `agent.turn` spans, every `llm.call`, and the tool call contexts; a failed `ThreadTrace` still replies; the store keeps the first id and fills an old row → AC-1, AC-2
- [x] `go test -race -run 'TestDecideSpansCarryApprovalID|TestCancelSpanCarriesApprovalID' ./internal/approvalapi/` → `approval.decide`, `approval.cancel`, and the executed `tool.call` carry `approval_id` → AC-3
- [x] `go test -race ./internal/telemetry/` → the Collector's `allowed_keys` equal `logging.AllowedKeys()` plus `service.name`; redaction is silent, drops unlisted keys, and its patterns match a whole email and a Slack id → AC-4
- [x] `go test -race -run 'TestCanary' ./internal/agent/` → no seed name, email, Slack id, department, message text, or `[redacted]` in any log or span value, and every exported span has a 32 hex trace id → AC-14
- [x] `TEST_DATABASE_URL=... go test -race ./db/ ./internal/db/` → the pairing CHECK, the old style close, the backfill, `agent_failures`, the one row kill switch table, the grants for every role, `noord_grafana` reads only `ops` and writes nothing (not even a temp table), its limits, and no sensitive column name in `ops` → AC-6, AC-7, AC-8, AC-9
- [x] `TEST_DATABASE_URL=... go test -race -run 'TestOutcome|TestFailure|TestPgOutcomesAndFailures' ./internal/agent/` → each closing path stores its outcome (resolved, escalated, refused, abandoned), each failure stage inserts one row, an `IsError` result inserts none, and a failed insert never stops the turn → AC-6, AC-7
- [x] `go test -race -run 'TestKillSwitchMode|TestSyncKillSwitch' ./internal/tools/` and `-run KillSwitchSyncInterval ./internal/config/` → the mode, the sync loop, an unreadable switch recorded as read_only with a `cause` only, the 5s to 60s range naming the setting → AC-8, AC-15
- [x] `TEST_DATABASE_URL=... go test -race -run Dashboard ./internal/db/` → every panel query runs as `noord_grafana` and reads only `ops` views; the outcome tiles count each outcome apart, abandoned is not in resolved, cost and errors add up, pending approvals show no requester, the recent table ignores the range and has a trace only where one is stored → AC-6, AC-10, AC-11
- [x] `go test -race -run 'Observability|Grafana' ./internal/install/` → the chart's ConfigMaps are the files, the three pods are restricted (non root, read only root, no privilege), the Collector runs our config and the binaries send to it, the network allows are exactly the spec's, the role and the tailnet Ingress, and Grafana's Secret follows `observability.enabled` → AC-5, AC-9, AC-11, AC-12
- [x] `helm lint deploy/helm/noord` and `helm template` of the agent, collector, tempo, and grafana parts for k3sprox with stand in sealed values → every part renders → AC-12
- [x] `go test -race ./...` and `make lint` after the last change → green (`make gen-check` only reports the uncommitted generated files until the commit)

## UI / manual (Compose, `make obs-up`)
- [x] Send a span with a name, an email, a Slack id, message text, and an unlisted resource key to `http://127.0.0.1:54318/v1/traces`, then read it from Tempo and from the Collector's debug output → none of them appears; the span keeps only allowed keys, with unsafe values shown as `****`. First run found the bare `@` pattern left the name and domain in place (journal 0083); after the whole token pattern, nothing leaked. Repeated on the Collector 0.162.0, Tempo 2.9.0, and Grafana 12.3.1 images → AC-4
- [x] Open `http://127.0.0.1:53000/d/noord-ops` with demo rows in the dev database → the tiles, both tables, and the chart show the right numbers; "open trace" links point at the Tempo datasource with the stored trace id; a conversation with no trace id shows no link; the kill switch tile reads "unknown" when `checked_at` is older than 3 minutes and "writes on" when it is fresh → AC-8, AC-10, AC-11
- [x] `docker compose --profile observability config` and `make obs-up` start the same Collector, Tempo, and Grafana from the same base files, with the debug overlay on the Collector only → AC-13
- [ ] `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:54318 make dev`, then send one real Slack message with a tool call → the conversation opens as one trace in Grafana, with two turns as sibling `agent.turn` spans if you reply in the thread → AC-1, AC-2, AC-5
- [ ] With the Collector stopped (`docker compose --profile observability stop otel-collector`), send a Slack message → the turn still completes, and the only trace of the fault is the throttled `telemetry_export_failed` log line. (No automated test covers this yet.) → AC-5
- [ ] With Tempo stopped, the dashboard's tiles and tables still load; only "open trace" fails → AC-10

## Live on k3sprox (after the merge and CI images; `make install CLUSTER=k3sprox` seals the two new passwords)
- [ ] `make install CLUSTER=k3sprox` → seals `PG_GRAFANA_PASSWORD` and `GRAFANA_ADMIN_PASSWORD`, the Applications `noord-tempo`, `noord-collector`, and `noord-grafana` become Synced and Healthy, the migration Job applies 00008, and `make smoke CLUSTER=k3sprox` still passes → AC-9, AC-12
- [ ] Run the one `ALTER ROLE noord_grafana SET statement_timeout = '10s'` from the runbook, then `SELECT rolconfig, rolconnlimit FROM pg_roles WHERE rolname = 'noord_grafana'` → `statement_timeout=10s` and `5` → AC-9
- [ ] The three pods start under Pod Security `restricted` with the read only root filesystem (no `FailedCreate`, no crash loop) → AC-12
- [ ] `https://noord-k3sprox-grafana.<TAILNET>` asks for the login; the datasource health check says OK over `verify-full`; nothing is reachable from outside the tailnet → AC-11
- [ ] Both binaries export to the Collector (`telemetry_export_failed` is absent) and `kubectl -n noord-agent exec` to a pod outside the allows cannot reach the Collector, Tempo, or Grafana → AC-5, AC-12
- [ ] **The live proof:** a real Slack conversation with a tool call opens as one redacted trace, and the dashboard shows it within a minute of closing → AC-1, AC-4, AC-10, AC-16
- [ ] Run the canary values (a seeded name, an email, a Slack id, message text) through a Slack conversation and search Tempo, the Collector's logs, and every dashboard panel → none appears → AC-4, AC-14
- [ ] `make uninstall` (on a scratch cluster, or by reading the plan) removes the three Applications and Tempo's volume claim → AC-12

## Value sourcing (one step per row of the spec's table)
- [x] Start a turn: trace id → the stored `conversations.trace_id`, else random (`TestConversationIsOneTrace`, `TestPgThreadTrace`); vary: a thread with no row, a row with no id, a failed lookup
- [x] Start a turn: parent span id → derived from the trace id (the last 8 bytes, lowest bit set if all zero); `agent.turn` spans of one thread share the trace
- [x] Close a conversation: outcome → by the closing tool or path (`TestOutcome*`); vary: privileged versus other escalate reason, the sweep with a blocked history, the idle close
- [x] Record a failure: stage and cause → the failing call site and `cause.Of` (`TestFailureRows`); vary: a cause that is not a fixed token is stored as `other`
- [x] Dashboard handled count → closed conversations in the Grafana range (`TestDashboardQueriesCountWhatTheyClaim`); vary: a row outside the range
- [x] Dashboard errors → audit `error` rows plus `ops.failures`, no overlap (the `tool` stage excludes `IsError` results)
- [x] Dashboard cost → `cost_usd` by `created_at` in range; vary: no calls gives "no data", not 0
- [x] Dashboard time → the range picker and `Europe/Amsterdam` (checked in the dashboard JSON test)
- [x] Dashboard approvals age → `now() - created_at` of pending, unexpired rows
- [x] Kill switch tile → `ops.kill_switch`; vary: a stale `checked_at` shows "unknown"
- [x] Trace link → the Tempo datasource uid plus `trace_id`; vary: a null `trace_id` shows no link

## Acceptance-criteria coverage
- AC-1, AC-2 … the two turn test and the live trace step · AC-3 … the approval span tests · AC-4 … the drift test and the canary check (Compose done, cluster open) · AC-5 … Compose wiring done, Collector down and cluster open · AC-6 … DB and agent tests · AC-7 … agent and DB tests · AC-8 … DB, tools, config tests and the tile · AC-9 … DB tests, chart test, cluster role check open · AC-10 … dashboard tests and the live view · AC-11 … provisioning and chart tests, cluster login open · AC-12 … chart tests, cluster open · AC-13 … Compose · AC-14 … canary tests · AC-15 … config test · AC-16 … `docs/runbooks/observability.md` written, the k3sprox live proof open
