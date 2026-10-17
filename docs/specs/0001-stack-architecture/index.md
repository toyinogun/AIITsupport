# 0001. Go monolith with an MCP tool server on the existing k3s cluster

**Date**: 2026-10-17
**Status**: In Progress

## Summary

This spec picks the tools for the whole build. Everything we write is Go, in one repo: a Slack facing agent that talks to Claude, and a separate tool server that is the only thing allowed to touch the client's systems (Active Directory, Entra, Zammad). It runs on your existing k3s cluster (Kubernetes, the system that runs our containers), installed by ArgoCD (a tool that keeps the cluster in sync with git), and it can also install the few platform pieces a fresh cluster lacks. One known gap: the model is called through the Anthropic API directly, which is not yet proven to stay in the EU, so you check that in week 1 and the model client stays swappable for Bedrock in an EU region.

## Rationale

Reasoning, the options weighed, the landscape check, and the cross check: see [rationale.md](rationale.md).

## Decision

**Chosen option**: Option 1: All Go, MCP tool server, on the existing k3s cluster

One Go module produces three binaries (`agent`, `toolserver`, `eval`). The agent reaches Slack over Socket Mode (an outbound connection, so nothing listens on the internet) and calls the tool server over MCP (Model Context Protocol, a standard way for an agent to call tools) using streamable HTTP. The tool server alone holds the credentials for Samba AD, Entra, and Zammad. Our Postgres runs on CloudNativePG (an operator that runs Postgres inside Kubernetes). Everything ships as one Helm chart (a packaged, configurable Kubernetes install) through an ArgoCD bootstrap app kept in this repo.

**Implementation skills**: `golang-error-handling` · `golang-security` · `golang-database` · `golang-concurrency` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `opentelemetry` · `tempo` · `grafana-oss` (`grafana/skills`, `.claude/skills/<name>/`, from feature #18 on) · `claude-api` (built in: model ids, SDK usage, tool use) · `golang-patterns`, `golang-testing`, `mcp-server-patterns`, `postgres-patterns`, `database-migrations` (`everything-claude-code` plugin) · `senior-kubernetes-engineer` (`~/.claude/skills/senior-kubernetes-engineer/`). Open each only when building in its area.

## Proposed stack

### Our code

| Layer | Choice | Reason |
|---|---|---|
| Pattern | One Go module `github.com/toyinogun/AIITsupport`, three binaries (`cmd/agent`, `cmd/toolserver`, `cmd/eval`); two deployables | A monolith in spirit; the only split is the credential boundary the brief requires. |
| Language | Go 1.27 (`go` and `toolchain` directives in `go.mod`; moved up from 1.26 in PR #2 so the image and CI build with one toolchain) | One language for the agent, tools, and eval; already installed locally. |
| Agent loop | Hand written loop on `github.com/anthropics/anthropic-sdk-go` (Messages API with tool use), bounded as in *Cross component rules* | Every tool call passes through code we can test and show; no hidden runner. |
| Model client boundary | `internal/llm` is a thin factory that returns the SDK's own client for the chosen provider (Anthropic API now, the SDK's `bedrock` package later), plus a per provider model id map. No home made request or response types. | Switching to Bedrock in an EU region becomes config, not a rewrite, and we never maintain a parallel API. |
| Model | `claude-sonnet-5-5` for the agent; the eval also runs `claude-haiku-5-5` for cost per ticket. Bedrock uses its own ids (EU inference profile prefix) from the map. | Sonnet for the fewest wrong actions; Haiku numbers make the cost tradeoff visible. Never hardcoded. |
| Model endpoint | Anthropic API direct, key in `ANTHROPIC_API_KEY`. EU residency checked in **week 1** (see Follow-up); the production default is picked then. | Your pick for simplicity, with the EU claim settled early instead of at the end. |
| Pricing | `eval/pricing.yaml`, dated, per model input and output price | Cost per ticket comes from a visible, dated table, never from constants in code. |
| Tool server | `github.com/modelcontextprotocol/go-sdk` (official, v1), streamable HTTP, **stateless** mode, plain HTTP inside the cluster | Stable v1 SDK; stateless because every call stands alone, so a tool server restart never breaks a conversation. |
| Agent to tool server auth | Bearer token checked in HTTP middleware (Sealed Secret), plus a NetworkPolicy allowing only the agent pod | Two independent locks. Identity of the human is separate, see *Cross component rules*. |
| Slack | `github.com/slack-go/slack` with its `socketmode` package; one agent replica | No official Bolt for Go; Socket Mode needs no public URL, and approval clicks arrive on the same socket. |
| AD access | `github.com/go-ldap/ldap/v3` over LDAPS (port 636) | The standard Go LDAP library; LDAPS because Samba refuses password and some account changes over plain LDAP. |
| Entra access | Thin HTTP client for the few Graph calls, tokens from `github.com/Azure/azure-sdk-for-go/sdk/azidentity` (client credentials) | About four calls are needed; the full Graph SDK is large and slow to compile. |
| Zammad access | Thin HTTP client on the Zammad REST API with a token | Small surface (tickets, KB search); no SDK worth adding. |
| Primary DB | PostgreSQL 17 | Relational, append only audit rows enforced with grants, JSON columns for tool payloads. |
| DB access | `github.com/jackc/pgx/v5` plus `sqlc` (typed Go generated from plain SQL) | The SQL that touches the audit log stays readable; no ORM emitting UPDATE or DELETE. |
| Migrations | `goose`, SQL files embedded in the binary, run by a Kubernetes Job before the agent and tool server start, connecting as the table owner role | Plain SQL; the owner role exists only for migrations, so app roles never own tables. |
| Config | `internal/config` is the single source of every setting: parsed and validated at startup, the process refuses to start on a missing required value. CI fails if `.env.example` drifts from it. | Fails loud on day 1; no setting name is invented in two places. |
| Kill switch | The tool server reads a mounted ConfigMap file on **every** write call and fails closed (any read error means read only). `READ_ONLY` env is only the boot default. | The IT person flips it with one `kubectl` command, no rollout, and it takes effect on the next call. Exact semantics are decided in feature #9. |
| Logging | `log/slog` JSON to stdout, level from `LOG_LEVEL` | Standard library; redaction rules come from feature #12. |
| Tracing | OpenTelemetry Go SDK in the code from the start, but a no op when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset | Spans exist from day 1; the collector, Tempo, and Grafana arrive in feature #18. |
| Tests | `go test`, plus `github.com/testcontainers/testcontainers-go` for throwaway Postgres and Samba | Same tests run locally and in CI; the eval reuses the fresh seed machinery. |
| Dev tools | `sqlc` and `goose` pinned as `tool` entries in `go.mod` (`go tool sqlc`, `go tool goose`) | Everyone, including CI, runs the same versions with no separate install. |
| Build tooling | `Makefile` targets (`make dev`, `make test`, `make gen`, `make seal`, `make release`) | Boring, present on every machine; the runbook points at targets, not long commands. |
| Container image | One `Dockerfile` with a `CMD` build argument (`agent` or `toolserver`), `CGO_ENABLED=0`, cross compiled with `GOOS`/`GOARCH` (no QEMU emulation), on a distroless static base, as a non root user | Two small images from one file; native cross compilation keeps CI fast. |

### Version pins

`go.mod` and `go.sum` pin every Go library and the `sqlc` and `goose` dev tools. The lint, hook, and scan tools are pinned in a separate `tools.mod` and `tools.sum` (spec [0002](../0002-coding-standards-tooling/index.md)). `deploy/helm/noord/Chart.lock` pins every subchart and third party chart. Third party images are pinned by tag in chart values, never `latest`. The scaffold records the exact versions it resolves on its first `go get`; no version is chosen by hand in this spec except Go 1.27 and Postgres 17.

### Cross component rules

These rules span the agent and the tool server. They are stack level so no later feature can drift from them; the named features design the details.

- **Trusted request context.** The agent's code, never the model, attaches the requesting Slack user id, the Slack thread, and the ticket id to every tool call, in the MCP request `_meta`. The tool server takes the requester only from there and ignores any identity a tool argument claims. (Details: features #9 and #10.)
- **Approvals bypass the model.** An approval is a pending approval row holding the exact tool name and arguments. A button click goes from the agent straight to a tool server approve endpoint that is **not** in the `tools/list` the model sees. The tool server checks the clicker against the approver allow list and the pending row, runs the stored call, and the agent only posts the result. The model loop never needs resuming. (Details: features #5, #14, #15.)
- **No audit row, no write.** If the tool server cannot write the audit row, it does not perform the write action and returns an error. (Ordering details: features #5 and #9.)
- **Durable state in Postgres.** Conversations are keyed by Slack `thread_ts`; pending approvals live in their own table. Nothing that must survive a restart lives in memory. (Tables: feature #5.)
- **Slack delivery.** Acknowledge each Socket Mode event immediately, then process it; drop duplicates by Slack `event_id`.
- **Agent loop bounds** (each one a config value): at most 8 model turns per user message, a 20 second timeout per tool call, a 45 second wall clock per user message (inside the 60 second target), 2 retries with backoff on rate limit or server errors, and at most 30,000 tokens per ticket. Hitting any bound means: tell the user, and escalate with a ticket.
- **Outages.** If the model API or a client system is down, the agent tells the user plainly, opens or updates the ticket if Zammad is reachable, and escalates. It never retries a write blindly.

### Client environment (Noord's systems)

| Layer | Choice | Reason |
|---|---|---|
| On prem directory | Samba AD DC in our own Dockerfile (Debian `samba` packages), provisioned at first boot, then seeded | We own every line of the image a stranger rebuilds; seeding is designed in feature #6. |
| Cloud identity | Entra ID free test tenant | Designed and timeboxed in feature #7. |
| Ticketing and KB | Zammad, official Helm chart, with its own bundled Postgres, Redis, and Elasticsearch | A real helpdesk with a built in knowledge base; client data stays apart from ours. Install snags are handed to #8 and #17 (see Follow-up). |
| Chat | Slack free workspace, one app with a bot token and an app level token (Socket Mode) | Feature #8 registers it. |

### Platform

| Layer | Choice | Reason |
|---|---|---|
| Local dev | Docker Compose (`compose.yaml` at the repo root) | Fastest inner loop for weeks 1 and 2. Samba AD joins in #6 and Zammad in #8. |
| Cluster | Your existing k3s cluster (Proxmox VMs, physically in the EU) | Already running; the EU location backs success criterion 3 for data at rest. |
| Namespaces | `noord-directory` (Samba AD only), `noord-client` (Zammad), `noord-agent` (agent, tool server, Postgres, later observability) | Pod security applies per namespace, so Samba's extra Linux capabilities stay isolated; NetworkPolicies make the tool server the only path into both client namespaces. |
| Packaging | One Helm chart in `deploy/helm/noord`; Zammad via its official chart | One `values.yaml` holds what the IT person tunes. Repo URL, image owner, and ingress class are values, so a fork installs without edits. |
| GitOps | ArgoCD bootstrap app of apps in `deploy/argocd/`. The one command install is `kubectl apply -f deploy/argocd/bootstrap.yaml` | The public repo is self contained. |
| Fresh cluster prerequisites | The bootstrap app has optional child apps for CloudNativePG, Sealed Secrets, and cert-manager, off by default on your cluster (already installed) and on for a fresh one. Needs ArgoCD and a default StorageClass first; the README lists both. | Keeps "one command into a fresh cluster" true without reinstalling what your cluster runs. |
| Postgres operator | CloudNativePG, one instance on the default StorageClass (Longhorn on yours). Roles created with CNPG `managed.roles` from Secrets. A `ScheduledBackup` is on before the first real audit rows land in slice 1. | The audit log is the compliance artifact, so backups are not a later nicety. |
| Secrets | Sealed Secrets; `make seal` turns a local `.env` into SealedSecret files for the right namespace and name. Back up the controller's sealing key. | Encrypted files are safe in a public repo; a stranger seals with their own controller. |
| Observability | Chosen now, installed in feature #18: an OpenTelemetry Collector (redacts in the pipeline) to Tempo (trace store), Grafana OSS for traces and the ops dashboard, with a Postgres datasource on a read only role | Nothing before #18 needs it; the audit table and `slog` carry the tracer bullet. |
| Admin UI access | Tailscale ingress for Zammad and Grafana (ingress class is a chart value) | Admin UIs never face the internet. |
| CI | GitHub Actions: test on every push; build amd64 and arm64 images (you build on arm64, the cluster is amd64). The eval's trigger (nightly or manual, path filtered) is decided in feature #20, never on fork PRs. | The repo already lives on GitHub; every eval run costs real API money. |
| Registry | GHCR, `ghcr.io/toyinogun/aiitsupport-agent` and `-toolserver`, tagged by git SHA; set both packages to public after the first push | Pull with no credentials, so a fresh cluster installs as is. |
| Image rollout | `make release` writes the new SHA tag into the chart values and commits it; ArgoCD syncs it | Every deploy is a git commit you can point to. |

### Scaffold done means

Feature #3's scaffold is done when, on a clean clone:

1. `make dev` starts Postgres in Compose, runs the goose migrations (an empty first migration is fine), and starts both binaries.
2. `agent` and `toolserver` each answer `GET /healthz` with 200, and fail to start with a clear message when a required setting is missing.
3. `make test` and `go vet ./...` pass, and `make gen` (sqlc) leaves no diff.
4. `docker build --build-arg CMD=agent .` and `CMD=toolserver` both produce images.
5. `.env.example` lists every setting in `internal/config`, and `.gitignore` covers `.env` and `docs/.agent-cache/`.

### Repo layout

```
cmd/agent/          Slack bridge and agent loop
cmd/toolserver/     MCP tool server
cmd/eval/           eval harness (feature #20)
internal/           shared packages (config, llm, mcpclient, slack, ldap, graph, zammad, audit, db)
db/migrations/      goose SQL files
db/queries/         sqlc queries
eval/               ticket set and pricing.yaml (features #19 and #20)
seed/               Samba AD, Zammad, and KB seed scripts (features #6 to #8)
deploy/helm/noord/  our chart
deploy/argocd/      bootstrap app of apps
compose.yaml        local environment
Dockerfile          one file, two images
docs/               scope, specs, discovery, journal
```

### Configuration required

`internal/config` owns these names; the feature in brackets owns the default value where it is not set here.

- Model: `LLM_PROVIDER` (`anthropic` or `bedrock`), `LLM_MODEL` (default `claude-sonnet-5-5`), `ANTHROPIC_API_KEY`; for Bedrock `AWS_REGION` (an EU region), `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` (agent only).
- Agent loop bounds: `AGENT_MAX_TURNS` (8), `AGENT_TOOL_TIMEOUT` (20s), `AGENT_MESSAGE_TIMEOUT` (45s), `AGENT_MAX_RETRIES` (2), `AGENT_MAX_TOKENS_PER_TICKET` (30000).
- Slack: `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN` (agent only); `SLACK_IT_CHANNEL` and `SLACK_IT_APPROVERS` (escalation channel and approver user ids, feature #11 and #14).
- Agent to tool server: `TOOLSERVER_URL`, `TOOLSERVER_TOKEN` (token on both sides).
- Listen addresses: `AGENT_HTTP_ADDR` (`:8080`, health only), `TOOLSERVER_HTTP_ADDR` (`:8080`, MCP and health).
- Database: one URL per role, never shared: `AGENT_DATABASE_URL` (`noord_agent`), `TOOLSERVER_DATABASE_URL` (`noord_toolserver`), `MIGRATE_DATABASE_URL` (`noord_owner`, migrations only), and `EVAL_DATABASE_URL` (`noord_eval`, feature #20). Roles, grants, and the audit settings (`AUDIT_OUTCOME_RETRIES`, `AUDIT_WRITE_TIMEOUT`, `AUDIT_MAX_PAYLOAD_BYTES`, `SLACK_EVENT_DEDUP_TTL`): [spec 0003](../0003-data-model/index.md).
- Samba AD (tool server only): `LDAP_URL`, `LDAP_BASE_DN`, `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD`, `LDAP_CA_CERT`.
- Entra (tool server only, unset if feature #7 takes the AD only fallback): `GRAPH_TENANT_ID`, `GRAPH_CLIENT_ID`, `GRAPH_CLIENT_SECRET`.
- Zammad (tool server only): `ZAMMAD_URL`, `ZAMMAD_TOKEN`, `ZAMMAD_DEFAULT_GROUP` (feature #8).
- Policy: `READ_ONLY` (boot default for the kill switch), `KILL_SWITCH_FILE` (path of the mounted ConfigMap file), `APPROVAL_TIMEOUT` (feature #14), `TICKET_RETENTION_DAYS` (feature #12).
- Telemetry: `LOG_LEVEL` (`info`), `OTEL_EXPORTER_OTLP_ENDPOINT` (unset until feature #18).

Credentials for client systems live only in the tool server's environment. The agent never receives them.

## Consequences

**Positive**:
- One language and one toolchain: one CI pipeline, one linter config, and the eval is plain Go.
- The credential boundary is real: only the tool server can reach client systems, enforced by the network as well as by code, and the model can neither fake who is asking nor approve its own request.
- Most of production already exists on your cluster, and a fresh cluster gets the missing pieces from the same bootstrap.
- No inbound internet exposure at all: Slack is outbound Socket Mode, admin UIs sit on the tailnet.

**Negative / tradeoffs**:
- **EU inference is not guaranteed yet.** Until the week 1 check passes or Bedrock EU is in use, success criterion 3 holds for data at rest but not for inference, and the data processing note must say so.
- **Slack is a US hosted service.** Message content lives in Slack whatever we do; a free workspace has no data residency option. The data processing note names this too.
- **No official Slack Bolt for Go.** `slack-go` is community maintained; approval cards are built from raw Block Kit.
- **Kubernetes is a heavy handover.** A single IT generalist inherits k3s, ArgoCD, and an operator managed Postgres. The runbook has to carry that weight.
- **Zammad is the heaviest piece** (about 4 to 6 GB with Elasticsearch) and needs that much free on **one** node, not just across the cluster. Elasticsearch also needs a kernel setting (`vm.max_map_count`), usually set by a privileged init container.
- **Samba AD usually needs extra Linux capabilities** (often `CAP_SYS_ADMIN` for file ACL storage), so `noord-directory` carries a relaxed pod security level.
- **One agent replica only.** Slack would spread events across replicas, but conversation handling is simpler with one; a restart pauses the agent until it reconnects, and the Postgres state lets it carry on.
- **Sealed Secrets ties secrets to one cluster's key.** Rebuilding the cluster means sealing again unless the key was backed up.

**Neutral**:
- `sqlc` and `goose` add a code generation step and a migration Job to the deploy order.
- Zammad's API token only exists once Zammad is running, so its install is two phase (see Follow-up).
- Images are built for two architectures because development is on arm64 and the cluster is amd64.

## Follow-up

- [ ] **Week 1:** check whether the Anthropic API offers EU data residency or regional inference. If yes, turn it on and record it. If no, decide then: Bedrock EU inference profile (never the Global one) for production and the final eval, or state the gap in the data processing note and README. Log the outcome in the journal.
- [ ] Data processing note (feature #22): list Slack and the model provider as processors outside our cluster, with what each one sees.
- [ ] Feature #5 spec: tables for conversations (by `thread_ts`), pending approvals (exact tool call), audit rows; per binary roles via CNPG `managed.roles`; owner role for migrations only; the grants that make audit rows append only; the audit then write ordering.
- [ ] Feature #6 spec: the Samba container's capabilities and pod security level for `noord-directory`, and the LDAPS certificate (a self signed CA mounted into the tool server).
- [ ] Feature #7 spec: both Entra Connect Sync and Cloud Sync need a Windows Server host for their agent. That makes the Oct 21 timebox tighter than it looks; decide early.
- [ ] Feature #8 and #17 specs: Zammad two phase install (Zammad up, then a Job mints the API token, then `make seal`); confirm the official chart's bundled Postgres, Redis, and Elasticsearch images still resolve; the Elasticsearch `vm.max_map_count` init container; check per node free memory before scheduling it.
- [ ] Feature #9 spec: kill switch semantics on top of the ConfigMap file mechanism; the approve endpoint kept out of `tools/list`; reading the requester from `_meta`.
- [ ] Feature #17 spec: bootstrap child app layout with the optional operators, overridable `repoURL` and image owner, the CNPG `ScheduledBackup` target, and backing up the Sealed Secrets key.
- [ ] Feature #20 spec: eval trigger (nightly or manual, path filtered, never on fork PRs) and the runner memory budget if it needs Zammad.
- [ ] Feature #4 (`/audit`): write root `AGENTS.md` from the real scaffold, including this stack, the installed skills above, and the MCP servers below.
- [ ] MCP servers you chose to connect later (in your own MCP settings): `grafana/mcp-grafana` (official, from feature #18 on) and a community Zammad MCP (`basher83/Zammad-MCP` or the Softoft server, review before use, from feature #8 on). Not connected yet.
