# Architecture

This page shows you what runs, where it runs, and why it is split the way it is. The full decision record is spec [0001](../specs/0001-stack-architecture/index.md); the tool contract is spec [0007](../specs/0007-tool-server-risk-tiers/index.md).

## The picture

```
 Slack (DMs and #it-escalations)
   │  Socket Mode, outbound only
   ▼
 ┌──────────────── agent ────────────────┐        Anthropic API
 │ Slack listener, per thread workers,   │ ─────► (model calls,
 │ the model loop, the failure path      │         global endpoint)
 │ holds: Slack tokens, model key,       │
 │        its own database role          │
 └───────────────┬───────────────────────┘
                 │ MCP over HTTP, bearer token,
                 │ identity in _meta set by agent code
                 ▼
 ┌────────────── tool server ────────────┐
 │ registry and risk tiers, dispatcher,  │ ──► Samba AD (LDAPS)
 │ kill switch, audit recorder,          │ ──► Entra ID (Microsoft Graph)
 │ approval routes, reset page           │ ──► Zammad helpdesk (REST)
 │ holds: every client system credential │
 └───────────────┬───────────────────────┘
                 ▼
          PostgreSQL 17 (audit_log, approvals, conversations,
          llm_usage, reset links, kill switch state)
```

Both programs are Go binaries built from one repo and one Dockerfile. They run in your k3s cluster, installed and kept in sync by ArgoCD from this repo.

## The two programs

**The agent** (`cmd/agent`) listens to Slack. For each message it works out who is asking, in code, before the model sees anything: it reads the Slack profile, then calls the tool server's `user_lookup`. Then the model (Claude, through `internal/llm`) picks tools one call at a time inside hard limits on turns, tokens, and time. Code, not the model, owns the conversation status, the ticket id, the fixed replies, and the failure path. When something goes wrong, the failure path always tries to leave you a ticket. Details: [internal/agent/AGENTS.md](../../internal/agent/AGENTS.md).

**The tool server** (`cmd/toolserver`) is the only process that can touch Noord's systems. It serves a fixed list of tools over MCP. Every call goes through one dispatcher, in a fixed order: blocked tool, argument schema, who is asking, kill switch, rate limit, approval, and only then the handler, wrapped in the audit recorder. The tool list, tiers, and guards are in [tools.md](tools.md). Details: [internal/tools/AGENTS.md](../../internal/tools/AGENTS.md).

## The rules the split enforces

1. **The agent never holds a client credential.** No AD password, no Graph certificate, no Zammad token. A lint rule and a test (`cmd/agent/boundary_test.go`) fail the build if agent code imports a client system package. If the agent or the model were fully compromised, the worst it could do is call the tools, under the same guards as always.
2. **Identity never comes from the model.** The requester is set by agent code in the MCP `_meta` field, checked for exact format by the tool server. No tool takes a user name, a target account, or a ticket id as an argument. Account tools act only on the person asking.
3. **Approvals go around the model.** For a medium tier tool (MFA clear, group access) the tool only files a request. The agent posts the approval card in Slack. The click comes back to agent code, which calls the tool server's approval route with the clicker's Slack id. The model cannot see or call those routes. The change runs only after the click, from the stored approval, never from what the model said.
4. **No audit row, no write.** The audit recorder writes an intent row before the handler and an outcome row after. If the intent row cannot be written, nothing runs.
5. **Durable state lives in Postgres**, never in memory, so a restart loses nothing. A sweep at start picks up conversations a restart cut off and escalates them.

## Noord's systems (the client environment)

| System | What the assistant does there | How |
|---|---|---|
| Samba AD | reads users and groups, unlocks, adds a member to a group after approval, sets a password through the reset page | LDAPS, one service account per job (`svc-pwreset` for the reset page only) |
| Entra ID | reads and clears MFA methods after IT approval | Microsoft Graph, app only, refuses any user without the managed marker |
| Zammad | creates, updates, tags, and closes tickets; searches the knowledge base | REST, through a client that never writes a Zammad user, organization, or token |
| Slack | the chat, the approval cards, the escalation posts | Socket Mode bot |

In this demo they are seeded with fictional Noord data by `cmd/seed`. At a real client they would be the client's own systems, and only the connection settings change.

## The reset page

Password reset links point at a small page the tool server serves on its own listener (spec [0011](../specs/0011-password-reset-links/index.md)). The link is single use and short lived. The page sets the password as `svc-pwreset` and writes its own audit row. The link reaches the requester only in their Slack DM, posted by agent code; the model never sees it, and the audit log drops it.

## Where it runs

| Piece | Where |
|---|---|
| Agent, tool server, seed Jobs | your k3s cluster, namespace `noord-agent`, from the `noord` Helm chart |
| PostgreSQL | your cluster, run by CloudNativePG, with backups to an EU jurisdiction Cloudflare R2 bucket |
| Samba AD, Zammad | your cluster (`noord-directory`, `noord-client`) in the demo |
| Secrets | sealed in git with Sealed Secrets; the only plain copy is your `secrets.env` |
| Traces and dashboard | OpenTelemetry Collector, Tempo, and Grafana in your cluster (spec [0016](../specs/0016-observability-ops-dashboard/index.md)) |
| Model inference | Anthropic, global endpoint (see [data-processing.md](data-processing.md#where-model-inference-runs)) |

The install is one command, `make install CLUSTER=<name>`, and after that git is the only way the cluster changes ([runbook.md](runbook.md#deploy-and-release)).

## What is deliberately not here

These came up and were left out on purpose. Each is listed in the scope's deferred list with the reason.

- **Tool server owned approval cards.** Today the agent posts the card and hears the click. A compromised agent could in theory forge a click for a pending approval. Moving the card to the tool server closes that gap.
- **EU bound model calls.** `internal/llm` is built to swap the provider (Vertex AI `eu` or Bedrock `eu.`), not done yet.
- **A second factor on the reset page.** The link travels over Slack only. Before a real client relies on reset links, add a code over a channel Slack does not control.
- **Admin tier changes, mailbox and SharePoint permissions, hardware.** Out of scope, and the first one is blocked in code.
