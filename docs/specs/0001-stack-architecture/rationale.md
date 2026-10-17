# 0001. Stack and architecture: rationale

Decision record for [index.md](index.md). `/develop` builds from the index; this file explains why.

## Context

> ⚠️ Premise note: The client in this story is one overloaded IT generalist, and the build hands that person Kubernetes, ArgoCD, an operator managed Postgres, and an observability stack. For a real 80 person company, operating a cluster alone is a known failure mode: most of the time goes to infrastructure instead of support. A single VM running Docker Compose would be kinder to that person. Kubernetes stays here for two reasons: the cluster already exists and runs these platforms, and the engagement exists partly to show GitOps and EU hosting skills. The runbook (feature #22) must therefore cover cluster basics, not just the app, and the README should say plainly that a real Noord could run the same Compose file on one VM.

Noord Logistics needs an AI agent that resolves identity and access tickets from Slack, with a ticket for every conversation, a human approval for every medium risk action, privileged changes blocked in code, and an append only audit row for every tool call (see [discovery](../../discovery.md)). The build is three weeks of evenings by one engineer, with a Tracer Bullet approach: prove the whole pipe (Slack, agent, tool server, AD, ticket, audit log) on one real ticket first, then thicken it one strand at a time.

The forces are unusually concrete. **Safety:** the brief's central claim is that guardrails live in the tool layer, not the prompt, so the architecture must give client credentials to exactly one component and make that component the only path in. **Compliance:** Noord holds EU customer data, so GDPR applies, and success criterion 3 promises that data and inference stay in the EU. **Handover:** the client's IT person must be able to run, tune, and switch off the agent alone. **Reproducibility:** the repo is public, and a stranger must be able to rebuild Noord from seed scripts and install everything with one command. **Evidence:** a 50 ticket eval runs in CI on every prompt or tool change, against a fresh seed each run.

There are also fixed facts. The engineer works in Go and TypeScript and has Kubernetes, GitOps, AD, and Entra experience. An existing k3s cluster (four Proxmox VMs, physically in the EU) already runs ArgoCD, ArgoCD Image Updater, CloudNativePG, Sealed Secrets, cloudflared, cert-manager, ingress-nginx, Longhorn, MetalLB, the Tailscale operator, and Cilium, with about 17 GB of memory free on its workers. It has no Prometheus, Grafana, Tempo, or Forgejo. The repo already lives on GitHub. Development happens on an arm64 Mac; the cluster nodes are amd64.

Without a decision here, every later feature (#5 to #23) would pick its own tools, and the project would drift into several languages and duplicate platforms.

## Options considered

### Option 1: All Go, MCP tool server, on the existing k3s cluster

One Go module with three binaries. The agent loop is hand written on the Anthropic Go SDK, Slack arrives over Socket Mode with `slack-go`, the tool server uses the official MCP Go SDK over streamable HTTP, Postgres runs on the cluster's CloudNativePG, Zammad is the ticketing system and KB, and everything installs through an ArgoCD bootstrap app in this repo. Local development uses Docker Compose.

**Pros**:
- One language, toolchain, and CI pipeline for agent, tools, and eval.
- The credential boundary is a real process and network boundary.
- Reuses almost every platform the cluster already runs.
- Socket Mode means no inbound route at all, a stronger "no open ports" claim than a tunnel.

**Cons**:
- No official Slack Bolt for Go; Block Kit approval cards are built by hand on a community library.
- Kubernetes is a heavy thing to hand over to one IT person.
- The Go agent loop is more code than an agent framework would need.

### Option 2: TypeScript agent with Bolt, Go tool server

The brief's alternative. The agent and Slack bridge are TypeScript on Slack's official Bolt SDK (optionally the Claude Agent SDK), and the tool server stays in Go. The platform is the same as Option 1.

**Pros**:
- Official Bolt: first class Socket Mode, interactive approval handling, and the most examples.
- A higher level agent framework is available, so there's less loop code.
- Shows two languages, which some interviewers like.

**Cons**:
- Two languages, two build pipelines, two sets of images, two linter configs, and two test runners, for a single engineer in three weeks.
- The eval harness has to straddle both, or pick one and mock the other.
- A framework managed loop hides the tool call path the video needs to show.

### Option 3: Lean single VM, Postgres tickets, Compose in production

The smallest possible build. A Go agent with tools as an in process package (no MCP), tickets in our own Postgres with a minimal UI instead of Zammad, and Docker Compose on one EU VM in production, with no Kubernetes.

**Pros**:
- By far the least to build and operate; the kindest handover to a single IT generalist.
- Fewest moving parts to fail in the demo.

**Cons**:
- Loses the "client's own messy helpdesk" story and Zammad's built in knowledge base, so slice 6 needs its own KB.
- With no tool server process, credentials and guardrails share a process with the model loop, which weakens the central safety claim.
- Drops the GitOps and one command cluster install that success criterion 5 and the portfolio rely on.

## Rationale

Option 1 wins on the forces that matter most here. The safety claim needs one component that alone holds client credentials and alone can reach client systems. Option 1 makes that a separate process in its own namespace, away from the client systems in theirs, with a NetworkPolicy and a bearer token, so "blocked in code" is also "blocked by the network". Option 3 cannot make that claim honestly. Option 2 can, but it pays for it with a second language that a single engineer, in three weeks of evenings, cannot afford: every hour spent keeping two pipelines and two test setups green is an hour not spent on the eval. The one real cost of Go, the missing official Bolt, is narrow: `slack-go` supports Socket Mode and interactive payloads, and approval cards are a few Block Kit structures.

The platform choices follow from reuse. The cluster already runs ArgoCD, CloudNativePG, Sealed Secrets, Longhorn, and Tailscale, so adopting them adds no new operational surface. Adding SOPS, External Secrets, Forgejo, or a separate registry would add components to run, with no gain for this build. The one new platform is the observability trio (Collector, Tempo, Grafana), the smallest set that gives per conversation traces with redaction in the pipeline. Pointing Grafana at the audit log in Postgres means the ops dashboard counts what actually happened, rather than a second set of metrics that could disagree with it. Socket Mode was chosen over the Events API through the existing tunnel because it removes the inbound path entirely, and the app will never be listed in the Slack Marketplace.

Two of your picks need to be stated against the forces. **Anthropic API direct**: the engineer chose it for simplicity, and it is the quickest path to a working week 2. However, success criterion 3 promises EU inference, and that could not be verified for the first party API at decision time. Bedrock with an EU inference profile is the option that meets the criterion as written. The compromise in the spec (a provider interface, plus a follow up to verify EU residency or switch before the final eval) keeps the simple path now while keeping the claim honest later. Do not let the follow up slide: the "EU infrastructure" LinkedIn post depends on it. **Kubernetes for a one person IT team**: covered in the premise note above. It is accepted because the cluster exists and the engagement is partly a showcase, and it is mitigated by the runbook and by the Compose file that already describes a single VM install.

The smaller picks each favor the boring, reviewable option. `pgx` with `sqlc` keeps the audit log SQL in plain sight, so no ORM can quietly emit UPDATE or DELETE (runner up: hand written pgx). `goose` for plain SQL migrations (runner up: golang-migrate). A Helm chart for one tuning file (runner up: Kustomize). The official MCP Go SDK because it is v1 and stable (runner up: mark3labs/mcp-go, pre v1). A thin Graph client because only about four calls are needed (runner up: the full msgraph SDK). Our own Samba image so the public repo contains no unaudited entrypoint (runner up: a community image). GHCR plus GitHub Actions because the repo already lives there and a stranger can pull images without credentials (runner up: Forgejo, which would cost an evening to stand up). Calls made at write time: Postgres 17; one agent replica, to keep conversation handling simple; image rollout by a `make release` commit rather than ArgoCD Image Updater (it is on your cluster, but a stranger's would not have it); `Makefile` over a task runner; and `log/slog` for logging.

## Landscape check (2026-10-17)

A quick web check ran before the stack questions. What it found:

- **MCP Go SDK (official):** v1, marked stable, v1.8.0 published 2026-09-04, with a streamable HTTP handler. `mark3labs/mcp-go` supports the same transports but describes itself as under active development, with no v1.
- **Anthropic Go SDK:** supports Bedrock (`bedrock` package). Vertex support and a tool runner or MCP helper appear in the repo tree, but their docs were not confirmed.
- **Claude on Bedrock in the EU:** EU regions include Frankfurt, Ireland, Paris, Stockholm, Zurich, Milan, and Spain, with EU inference profiles. The Global endpoint routes across all regions and is not EU safe. In region endpoints carry about a 10% premium. Sonnet 5.5 and Haiku 5.5 need per model access requested.
- **Claude on Vertex AI in the EU, and EU residency on the first party API:** not confirmed on official pages in this check.
- **slack-go/slack:** v0.30.1 (2026-10-04), Socket Mode supported. No official Bolt for Go was found.
- **Zammad and GLPI:** current versions and Helm chart status were not confirmed. GLPI 11 has been stable since late 2025, per the vendor (snippet only, not fetched).

## Cross check (2026-10-17)

An independent read only review on a different model (Sonnet 5.5) found 13 gaps. All of its recommended fixes were applied, on the engineer's pick. The ones that changed the design:

- **Identity across the MCP seam.** If the model supplied the requester, a prompt injection could forge it, and a shared token would let a compromised agent claim an approval. Now the agent's code attaches the requester in `_meta`, and approvals go through an endpoint the model never sees.
- **State and redelivery.** Approvals can be pending for a long time, and Slack redelivers unacknowledged events. Now state lives in Postgres keyed by thread, events are acknowledged and then deduplicated, and the pending approval row stores the exact call.
- **EU inference timing.** The check moved from "before the final eval" to week 1, and `internal/llm` became a thin factory over the SDK's own clients.
- **Fresh cluster install.** The bootstrap now carries optional child apps for the operators your cluster already has, plus overridable repo and image settings.
- **Kill switch.** It moved from an env var (which needs a rollout, and which ArgoCD would revert) to a ConfigMap file read on every write.
- **Samba namespace.** Samba got its own namespace, because pod security is enforced per namespace.
- **Observability timing.** The install was deferred to #18, since nothing before it needs the collector.
- **Smaller fixes.** Version pin rules, a scaffold definition of done, loop bounds, the full config list, Postgres role provisioning, the Zammad two phase install, earlier backups, and the eval trigger.

The review confirmed MCP stays defensible (it keeps the credential boundary, and the deferred multi agent feature needs it). It also confirmed that Slack would spread Socket Mode events across replicas, so the single replica choice is about state, not a protocol limit.

## References

**Project sources**:
- [docs/discovery.md](../../discovery.md): success criteria 2, 3, and 5 drive the credential boundary, the EU hosting, and the one command install.
- [docs/scope/scope.md](../../scope/scope.md): the Tracer Bullet approach, the Beta and GA tiers, and features #3, #5 to #9, #17, #18, #20.
- The existing k3s cluster, inspected read only on 2026-10-17: ArgoCD, CloudNativePG, Sealed Secrets, Tailscale, Longhorn, cloudflared; no observability stack.

**Practices & standards**:
- Least privilege: a single component holds client credentials.
- Defense in depth: a NetworkPolicy and a bearer token in front of the tool server, on top of the risk tiers in code.
- Monolith first: one module, split only at the credential boundary.
- Boring technology: reuse the platforms the cluster already runs.
- GDPR data minimisation and processor transparency (the data processing note).

**Links** (web verified on 2026-10-17):
- MCP Go SDK: https://github.com/modelcontextprotocol/go-sdk
- MCP Go SDK package docs: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp
- mark3labs/mcp-go: https://github.com/mark3labs/mcp-go
- Anthropic Go SDK: https://github.com/anthropics/anthropic-sdk-go
- Claude in Amazon Bedrock (regions, EU inference profiles): https://platform.claude.com/docs/en/build-with-claude/claude-in-amazon-bedrock
- slack-go/slack: https://github.com/slack-go/slack
- slack-go package docs: https://pkg.go.dev/github.com/slack-go/slack
