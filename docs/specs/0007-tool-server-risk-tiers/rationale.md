# 0007 rationale: tool server and risk tiers

The decision record for [index.md](index.md). `/develop` does not need this file.

## Context

Feature #9 is the first piece of the safety claim the whole engagement rests on: the agent can only change Noord's systems through a tool layer that refuses unsafe actions in code, whatever the model was told. Spec 0001 fixed the shape (a separate Go binary holding every client credential, MCP over streamable HTTP, a bearer token, requester identity in `_meta`, approvals outside the model, a ConfigMap kill switch) and left the semantics to this feature. Spec 0003 built the audit recorder the tool server must use. Spec 0004 fenced the tool server's AD account (`svc-itsupport`) so it can only write `lockoutTime` under `OU=Users` and `member` under `OU=Groups`.

The forces:
- **The model is untrusted input.** Prompt injection in a ticket body, a clever privilege request, or a hallucinated argument must not reach a write. Anything the model can fill in is something an attacker can fill in.
- **The AD fence has gaps.** AD does not stop `svc-itsupport` writing `member` on `APP-Helpdesk-Agents` (Zammad Admin), `SYNC-Entra-Users`, or distribution lists, and a group nested into `SG-IT-Admins` would grant admin through membership.
- **Identity.** Nothing in the seed links a Slack user to a directory user except email (Slack's `users:read.email` scope, AD `mail`, the demo overlay). The tool server must know who is asking without trusting the model.
- **Operability.** One IT generalist inherits this. Pausing the agent must be one command, take effect at once, and not lose the requests that arrive meanwhile.
- **Audit as evidence.** The eval and the video read the audit log as ground truth, so refusals must be recorded with a fixed reason, not just logged.
- **Tracer Bullet and time.** Week 2 needs the locked out thread (#10) working end to end, so #9 must stay thin enough to land early in the week.
- **GDPR.** The data is about Noord's staff; nothing personal may land in logs, spans, or the unerasable audit table.

Assumption: the requester's Slack email is a reliable link to their directory account. No spec records Slack workspace email policy; #10 carries that check (see index Follow-up).

## Options considered

### Option 1: one audited dispatcher, tiers in a code registry, code floor plus config fence, identity by email (chosen)

Every tool is a registry entry in Go (tier, account change flag, schema, Store keys, handler). One dispatcher runs the same ordered checks for every call and then `audit.Recorder`. The fence is a code floor of built in privileged groups plus config settings for Noord's OUs and denied groups. The tool server resolves the requester from an email that agent code puts in `_meta`.

**Pros**:
- Each safety rule is written once and applies to every tool, including tools added later.
- Tiers change only through code review; the IT person can tighten the fence by config but not open the floor.
- Self only tools with no target argument make impersonation through the prompt impossible, not just unlikely.

**Cons**:
- The dispatcher is a central piece; a bug there affects every tool, so it needs the most tests.
- An LDAP bind and search on every call, even reads.
- Identity trusts Slack's email and the agent process.

### Option 2: per tool checks, no shared dispatcher

Each handler does its own identity, kill switch, fence, and audit calls, with shared helpers.

**Pros**:
- Each tool reads top to bottom on its own; no indirection.
- Tools with unusual needs bend the rules locally.

**Cons**:
- Every new tool can forget a check, and "a call against a denied group fails whatever the prompt says" becomes a promise per handler.
- The ordering of checks drifts between tools, so audit outcomes stop being comparable for the eval.

### Option 3: policy file (YAML) for tiers and lists

A mounted `policy.yaml` holds each tool's tier, the OU lists, and the denied groups; the dispatcher reads it.

**Pros**:
- One readable file for the handover; the IT person sees the whole policy in one place.
- Tiers change without a release.

**Cons**:
- A second source of truth beside `internal/config`, against the project rule that every setting is declared once.
- Tiers become editable by anyone who can edit a ConfigMap, outside code review, which undoes "enforced in code".

### Option 4: OPA or a policy engine sidecar

Express the rules in Rego and ask a policy engine before each call.

**Pros**:
- A mature, auditable policy language; decisions are logged by the engine.

**Cons**:
- A new component to run, learn, and hand over, for about a dozen rules.
- The rules still need directory lookups (nested membership, identity) that the engine cannot do alone, so half the logic stays in Go anyway.

## Rationale

Option 1 is the only one that makes the safety claim structural rather than a habit. The model is untrusted, so the strongest move is to give it nothing to forge: account tools take no target and ticket tools take no ticket id, and identity arrives in `_meta` from agent code. Once arguments cannot name a victim, the remaining checks (kill switch, fence, rate limit) are uniform, and a single dispatcher with a fixed order is the simplest way to guarantee every tool gets them and every outcome is comparable in the eval. Option 2 spreads the same rules across handlers, which is exactly how a fifth tool ships without a check. Option 3 breaks the one place rule for settings and puts tiers outside review, and Option 4 adds a component the IT person must run for a rule set that fits on one page.

The code floor plus config split answers the operability force without weakening the fence. The built in AD admin groups never change, so they live in code where no ConfigMap edit can drop them; Noord's own sensitive groups (`SG-IT-Admins`, `APP-Helpdesk-Agents`, `SYNC-Entra-Users`) and OUs are settings with safe defaults, so a fork or a different client tightens them without a release. Users are fenced by OU alone, your choice: with unlock limited to the requester and the match requiring an enabled user under `OU=Users`, admin and service accounts can never be targets, and the nested group check is reserved for group targets where it matters (#15).

The kill switch pauses account changes and leaves tickets flowing, your choice over the literal "all writes". A paused agent that still files every request as a ticket is what the IT person actually wants during an incident; a paused agent that can only say "email IT" pushes the work back onto them. Failing closed on anything but the exact text `read_write` means a deleted, empty, or garbled ConfigMap pauses the agent rather than freeing it.

### The smaller calls

- **Identity by email in `_meta`, matched in AD** over the agent resolving the account: the agent holds no directory access (credential boundary), and the match rule (one enabled user under `TOOL_USER_OUS`) also keeps admin accounts out. Runner up: leaving identity to #10, rejected because #9's refusal claims would be hollow in between.
- **Self only unlock and lookup** over "any allowed user": least privilege, and it matches the locked out flow exactly. A colleague's request becomes an escalation.
- **Blocked tools are visible** over hidden: a privileged request then leaves a real `blocked` row in the audit log, which #11 and the video need. Hidden tools record only injected or hallucinated calls.
- **Medium tier is routing only:** #9 sends medium tools to `Defer` and proves it with a test tool; #14 builds the approve endpoint and Slack card, where the approver and expiry decisions belong. Runner up: building the endpoint now, rejected as speculative before any medium tool exists.
- **Refusals as `isError` tool results** over JSON RPC errors: the model reads the fixed sentence and can explain and escalate; the agent loop needs no translation.
- **Rate limit from `audit_log`** over memory: durable, restart safe, and it reuses the table that already records every outcome. Counting only `ok` outcomes keeps the query simple; runner up was counting intents, which would also punish refused attempts.
- **One ticket per thread** keeps `conversations.zammad_ticket_id` one to one, so `ticket_update` can safely take its ticket only from `_meta`.
- **Dial and bind per call** over a shared connection: a few calls per ticket, no reconnect logic, and a Samba restart needs no recovery.
- **Required config, no reachability probe:** a typo fails at startup, while `make dev` still starts without the client environment up.
- **`internal/ldap` extracted from the seeder** over a fresh copy: one LDAPS dialer that refuses unverified TLS, tested once. The tool server must never import `internal/seed`, which holds admin capable code.
- **`internal/mcpclient` built in #9:** the `_meta` contract is the trust boundary, so its client side is designed and tested with the server, not discovered in #10.
- **Unlock verifies by reading the flag again,** so the user only hears "done" when AD agrees.
- **Unknown tool names are left to the SDK** rather than audited: they carry no tier, and the SDK rejects them before any of our code runs. Accepted as a small gap in "every call writes an audit row".
- **Typed handler deps** over an "account change" boolean: a forgotten flag would silently skip the kill switch; a handler that never receives the directory writer cannot write.
- **Soft rate limits** over an advisory lock per requester: the limit is a brake on loops and abuse, not a security wall, and an extra lock adds a failure mode for little gain.

## Cross check (Oct 18, 2026)

An independent read only review on a different model found 20 points; all were applied with your approval. The load bearing ones: the blocked check now runs before identity, so a blocked call never depends on AD; arguments that fail validation are no longer stored in the audit log; the audit failure paths (`ErrAuditUnavailable`, `ErrAuditIncomplete`) and the error classification are named; `_meta` formats are exact and checked in SDK middleware; `ticket_create` also checks the audit log for a ticket already made in the thread and has its own rate limit; a `TOOL_CALL_TIMEOUT` keeps every call inside the agent's timeout, and the kill switch is read again just before the write; account change handlers are typed so the kill switch cannot be skipped. It also pinned the Zammad state names, article types, the closed ticket refusal, the exact login search, the identity LDAP filter, DN comparison, unresolved denied group handling, the tool descriptions, and the `mcpclient` lifecycle. The OU check inside `account_unlock` was dropped as unreachable, since the identity match already requires the OU.
