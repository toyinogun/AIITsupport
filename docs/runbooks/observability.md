# Runbook: observability and the ops dashboard

This is how you read the "Noord assistant, last 24 hours" dashboard, how you follow one conversation, and what to do when the monitoring itself misbehaves. The design is spec [0016](../specs/0016-observability-ops-dashboard/index.md).

## Where it is

| Place | Address |
|---|---|
| Grafana on the cluster | `https://noord-<cluster>-grafana.<TAILNET>` (tailnet only, then the admin login). Set `observability.tailscaleHost` in the cluster's `values.yaml` to change the host. |
| Grafana on your laptop | `make obs-up`, then `http://127.0.0.1:53000`. The local login is the throwaway one in `compose.yaml`. |
| Traces | Grafana, Explore, the Tempo datasource. |

On the laptop, point the binaries at the Collector to get traces: `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:54318`. Without it `make dev` sends nothing.

## Reading the dashboard

The range picker (default: the last 24 hours, Europe/Amsterdam) moves every tile except the two that show "now": approvals waiting and the recent conversations table.

| Tile | What it counts | When to look closer |
|---|---|---|
| Resolved, Escalated, Refused, Abandoned | Conversations closed in the range, by how they ended. Refused is a privileged request the assistant turned down. Abandoned is a thread that went idle; it is never added to Resolved. | A jump in Escalated with no matching IT tickets. Refused is normal, but a sudden rise means someone is probing. |
| Approvals waiting | Pending approvals right now, and the age of the oldest. | Anything older than the approval window (`APPROVAL_TIMEOUT`, 30 minutes by default) means the card was missed or Slack is down. |
| Errors, Errors by stage | Tool calls that ended in an error (from the audit log) plus agent failures: `llm` (a model call failed), `tool` (a call got no answer), `slack` (a post failed), `sweep` (a restart interrupted a conversation). `tool_result_error` is the audit log's own error rows. | Any `llm` failures (model outage or key problem), a growing `slack` count (Slack outage or rate limit), any `sweep` (the agent restarted mid conversation). |
| Account changes, Mode since | The kill switch mode the tool server enforces, and when it last changed as the tool server saw it. After a fresh start, "since" means "first seen". **unknown** means the tool server has not checked in for 3 minutes. | Unknown: the tool server is down or cannot reach the database. Writes on when you expected read only: flip it back with `kubectl` (see [deploy.md](deploy.md)). |
| Cost, Cost per conversation | Model spend in US dollars for calls made in the range, and the same divided by the conversations that made calls. "no data" means no calls in the range. | Cost per conversation well above the eval's number. |
| Pending approvals | Tool, tier, approver kind, and age of each open approval. No requester is shown on purpose. | Same as the tile. |
| Tool calls by tool and outcome | The audit log's outcome rows. | A tool that is mostly `error` or `denied_policy`. |
| Recent conversations | The 50 newest, whatever the range. | A conversation with many model calls or a long duration. |

The dashboard reads only the `ops` views in Postgres, so it keeps working when Tempo is down and it cannot show a name, an email, a Slack id, or message text.

## Following one conversation

1. In Recent conversations, click **open trace** on the row. Grafana opens the trace in Tempo.
2. Every Slack thread is one trace. Each message in the thread is a sibling `agent.turn` span, with its `llm.call` spans and the tool server's `tool.call` spans under it. Nothing in it is personal data: ids, counts, and fixed words only.
3. A row with no link has no trace id yet (a conversation from before spec 0016 gets one on its next message). You can also search Tempo by trace id directly.
4. Tempo shows the root as "not yet received". That is expected: the root is a synthetic parent made from the stored trace id. Search by trace id works; searching by root span name does not.

**Approved actions are separate traces.** An approval click, a cancel, the approved tool call that runs after it, and a password reset submit all start from a Slack click or a browser request, with no conversation above them, so they are not inside the conversation's trace. Follow an approval by its id instead: in Explore, run the TraceQL query `{ span.approval_id = "<approval id>" }`. Code started calls with no turn above them (the restart sweep, the approval settle) are also in their own traces.

**A stray trace.** If the agent could not read the thread's stored trace id for a turn (a database error at that moment), that one turn lands in a new trace of its own. The stored id is kept, so the next message goes back to the conversation's trace.

## Things the numbers do not show

- **Idle closes back date `closed_at`.** A thread is closed as abandoned after it has been idle for the retention window (30 days by default), and `closed_at` is set to its last activity. So "Abandoned in the last 24 hours" reads close to zero, and the counts for earlier days move after the fact.
- **Old history flatters Resolved.** Idle closes from before spec 0016 cannot be told apart and stay "resolved".
- **A database fault under counts errors.** A failed `agent_failures` insert is only a log line (`agent_failure_write_failed`), so the Errors tile can read low exactly when the database is the problem. The audit log and the traces still show the rest.
- **Tool failures from code made calls** (the escalation by code, the close after a denied request) are not counted as `tool` failures.

## Retention and the Tempo disk

Traces are kept 7 days (168 hours). Nothing else is backed up; traces are disposable. Tempo has one volume claim (`observability.tempo.storage`, 5Gi by default).

If the disk fills, Tempo stops accepting traces and the Collector logs failed exports. Turns still complete: the only sign in the agent and tool server logs is the throttled `telemetry_export_failed` line. To recover:

1. Check usage: `kubectl -n noord-agent exec tempo-0 -- df -h /var/tempo`.
2. Make room by raising `observability.tempo.storage` (the volume must support expansion) or lowering `observability.tempo.retention` in the cluster's `values.yaml`, then `make install CLUSTER=<name>`.
3. If you just want a clean slate, delete the claim and the pod: `kubectl -n noord-agent delete pvc storage-tempo-0 && kubectl -n noord-agent delete pod tempo-0`. Only traces are lost.

## The Grafana admin password

There is one admin login. Its password is the sealed Secret `noord-grafana` (key `GRAFANA_ADMIN_PASSWORD`). Grafana keeps no state on disk, so a new password takes effect on restart:

1. Put a new value in `GRAFANA_ADMIN_PASSWORD` in `deploy/clusters/<name>/secrets.env`.
2. `make install CLUSTER=<name>` seals it and pushes the cluster folder.
3. Restart Grafana: `kubectl -n noord-agent rollout restart deploy/grafana`.

## The database role

Grafana connects to Postgres as `noord_grafana`, which can read only the `ops` views (5 connections, a 10 second statement timeout, no temporary tables). Its password is `GRAFANA_DB_PASSWORD` in the same Secret, from the generated `PG_GRAFANA_PASSWORD`.

On a **fresh** database the role is created with its statement timeout (`bootstrap.initdb.postInitSQL`). On a database that already existed before spec 0016 (k3sprox), CloudNativePG creates the role with its password and connection limit but cannot set the timeout, so run this once as the superuser:

```bash
kubectl -n noord-agent exec noord-pg-1 -c postgres -- psql -d noord -c "ALTER ROLE noord_grafana SET statement_timeout = '10s'"
```

Check it with `SELECT rolconfig FROM pg_roles WHERE rolname = 'noord_grafana'`.

## When the monitoring is down

- **Collector or Tempo down:** the assistant keeps working. Nothing in the readiness probes depends on telemetry. The dashboard still shows everything except traces.
- **Grafana down or unreachable:** nothing else is affected. Check the pod, the `grafana` Ingress, and the Tailscale proxy.
- **The dashboard says errors but the tiles show zero:** check that the database role can still connect (`noord-pg` Healthy) and that migration 00008 ran (`make smoke CLUSTER=<name>` reports the migration version).
- **A key added to the log allow list:** add it to `allowed_keys` in `deploy/helm/noord/files/observability/collector.yaml` too. A test fails the build if the two lists differ.

## Disabling it

Set `observability.enabled: false` in the cluster's `values.yaml` and run `make install`. The three Applications, their ConfigMaps, policies, and Ingress go away. The `noord_grafana` role and its Secret stay, because migration 00008 needs the role. `make uninstall` removes everything, including Tempo's volume claim.
