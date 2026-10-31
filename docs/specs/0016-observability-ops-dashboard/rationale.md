# 0016. Rationale: observability and the ops dashboard

Build spec: [index.md](index.md).

## Context

> ⚠️ Premise note: the scope says "follow one Slack conversation through every tool call in a trace". Today each Slack event starts its own `agent.turn` trace, and an approved action runs later from a Slack click on the tool server, with no turn above it. One trace per conversation is reachable for the agent's own turns; it is not reachable for approved actions without carrying the trace id onto the approval row. This spec joins the turns and links approvals by `approval_id`, and says so plainly rather than promising more.

Noord has one overloaded IT person. The assistant already writes a strong audit trail (`audit_log`, `approvals`, `llm_usage`, `conversations`), but nobody can read it at a glance, and the tracing from spec 0001 is a no op until a collector exists. Three questions have no durable answer: how did a conversation end (the status says `resolved` for both a solved ticket and an idle close, spec 0010), did the agent fail (failures live only in logs), and are writes on (the kill switch is a ConfigMap).

Forces: the repo is public and the system serves a privacy claim, so telemetry must not become a leak (spec 0010). The stack was chosen in spec 0001 (Collector to Tempo to Grafana, Postgres datasource on a read only role, Tailscale ingress). The cluster is small, shared with Longhorn, and has hit control plane starvation before (journal 0069), so every new workload costs something. Traffic is a handful of conversations a day. Week 3 is tight and the eval comes next.

## Options considered

### Option 1: Collector, Tempo, Grafana, a stored conversation trace id, and a Postgres view layer (chosen)

Traces go through a redacting Collector to Tempo. A trace id stored on each conversation joins the turns. Grafana reads counts from Postgres views through a view only role and opens traces from Tempo.

**Pros**:
- Matches spec 0001, so no new decision for the cluster.
- Two layers of redaction; the dashboard holds no personal data by construction.
- The dashboard works when Tempo is down.

**Cons**:
- Three new workloads and a PVC.
- The trace root is synthetic, and approvals sit outside the conversation trace.

### Option 2: Postgres only, a Grafana or small page over the audit tables, no traces

Skip the Collector and Tempo. Build the dashboard on views and let `audit_log` carry the story.

**Pros**:
- One new workload (Grafana), or none with a hand written page.
- No redaction pipeline to keep in sync.

**Cons**:
- No model call timing or span view; "follow one conversation through every tool call, redacted" would be a SQL query, not a trace. It misses the scope's first done line, and the video loses its best shot.

### Option 3: Metrics first (Prometheus) with a metrics dashboard, traces later

Add counters in code, scrape with Prometheus, chart in Grafana.

**Pros**:
- Standard for latency and error rates; alerting is natural.

**Cons**:
- Everything the IT person asked for (tickets handled, approvals waiting, cost) already exists as rows, so counters would duplicate Postgres and drift from it. A fourth workload and a second source of truth for no new answer.

### Option 4: A hosted observability service

Send traces to a vendor.

**Pros**:
- No workloads to run.

**Cons**:
- Sends conversation shaped data to a third party, against the privacy claim and the EU data story in the runbook. Rejected without further weighing.

## Rationale

Option 1 is the only one that meets both done lines and keeps the privacy claim: Option 2 cannot show a trace, Option 3 adds a source of truth nobody asked for, and Option 4 breaks the rules of the engagement. The cost of Option 1 is real (three workloads on a small cluster), so the spec keeps each light: Tempo single binary with 7 day retention, no sampling, no backups, no metrics generator, no alerting.

The dashboard reads Postgres, not Tempo, for every number. That choice follows from the forces: the numbers the IT person needs already exist as rows, they must survive a telemetry outage, and the view layer is the cleanest way to promise "no personal data" (an allow list of columns, tested). Traces stay for the "why" questions.

Joining turns with a stored trace id (not a derived id, not span links) is the smallest change that meets "one trace per conversation": one nullable column, one store read, and tool spans join through the header that already exists. The cost is a synthetic root, accepted because the dashboard links by trace id and never by root name. A simpler variant was weighed and rejected: derive the trace id as a hash of the Slack channel and thread (no column, no store read, no migration `00007`). A hash of Slack identifiers that sits in Tempo is pseudonymous personal data, and anyone with the channel and thread could recompute it, which cuts against the privacy claim; a random id stored beside the row has neither problem.

## Smaller calls (the RECOMMEND items, with runners up)

| Call | Pick | Why | Runner up |
|---|---|---|---|
| Outcome and status pairing | `refused` pairs with `escalated`, `abandoned` with `resolved` | A privileged refusal ends `escalated` (spec 0009); an idle close ends `resolved` (spec 0010). Your confirmed answer paired `refused` with `resolved`; the code says otherwise, so the pairing follows the code. | A new status value (breaks three specs) |
| What counts as `refused` | Only `escalate` with reason `privileged_request` | Matches the eval's `refuse` for the adversarial set; gate and cap refusals stay `escalated` | Count gate refusals too |
| Parent span id | Derived from the trace id | No second stored value | Store a span id |
| Collector distribution | contrib image, pinned | The `redaction` processor is not in the core build | Build a custom collector (more to maintain) |
| Redaction rule | Allow list of keys equal to `logging.AllowedKeys()`, plus blocked value patterns, plus a drift test | One list, two layers, CI keeps them equal | A deny list (rots) |
| Kill switch recording | Poll every 30s and write on change, plus `checked_at` | Works while idle; `checked_at` stops a dead tool server showing a stale mode. This adds `checked_at` to the model you confirmed. | Write only on a call |
| Failure stages | `llm`, `tool`, `slack`, `sweep` | The four places a failure is not already an audited tool outcome | Log every escalation reason as a failure |
| Tempo storage | One PVC, 7 days, no backup | Traces are disposable; a restore drill is not worth a backup job | Object storage in R2 |
| Dashboard timezone | `Europe/Amsterdam` | Same rule as spec 0014's stale dates; Noord is Dutch | UTC |
| Provisioning | Files, not UI edits | Reviewable in git, same in Compose and cluster | Hand built, exported later |
| Migration split | `00007` trace id first, `00008` the rest | Tracer Bullet: the join ships before the data | One migration |
| `ops` views not materialized | Plain views | Volume is tiny; materialized views add a refresh job | Materialized |
| Rolling deploy safety | A trigger fills a null outcome for a closed row | Old pods keep working during the rollout; no release ordering | Ship the writing code one release before the CHECK |
| Grafana state | `emptyDir`, no persistence | Everything is provisioned; a password reset is "change the Secret, restart" | A PVC for Grafana's database |
| Kill switch read error | Record `read_only` and log | It is what the tool server enforces | Record "unknown" |
| Stage for `sweep` | Written by the restart sweep per interrupted conversation | The sweep is the only place that learns a turn died | Not recorded |

## Open to challenge

- Whether `abandoned` should also include conversations the user left `open` for days but which retention has not closed yet. This spec counts only closed rows, as the CHECK requires.
- Whether Grafana should keep a second, read only viewer login for Noord's IT person next to the admin. One login is simplest; the runbook can add a viewer later.
