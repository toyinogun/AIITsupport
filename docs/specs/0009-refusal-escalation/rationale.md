# 0009. Rationale: privileged request refusal and escalation

Decision record for [index.md](index.md). `/develop` builds from the index; this file explains why.

## Context

> ⚠️ Premise note: the scope's done when line says privileged requests are "refused even when phrased cleverly". The refusal half is already true in code: spec 0007 gives no tool any way to grant a privileged group, change an admin account, or act on another person, and `privileged_access_change` is refused before it can touch anything. What clever phrasing can still defeat is the *flag*: whether the model recognises the request and routes it to the blocked tool, so IT hears about it. So this feature is really about routing, escalation, and the flag, and it should be judged on those, not on whether the refusal holds (it always does).

Noord's IT is one person. The discovery doc puts admin tier changes (Domain Admins, Global Admin) out of scope and says the agent "refuses, explains why, and escalates", blocked in code. The scope makes the printer case ("I need Domain Admin to fix a printer") the opening of the engagement video. Today (spec 0008) the model calls `privileged_access_change`, gets `blocked`, tells the user, and opens an ordinary ticket. Nothing tells IT that a privileged request happened, nothing marks the ticket, and IT can't read the user's DM with the bot to find out.

Three forces shape the answer. **The credential boundary**: only the tool server holds client system credentials and writes `audit_log`; only the agent holds the Slack bot token. An escalation that must be audited and must reach Slack crosses that line. **"No audit row, no write"**: the scope requires the audit log to show both the blocked attempt and the escalation. **GA tier**: this feature carries the safety claim, so a flag that depends on the model remembering a step is not good enough, and every escalation has to be provable afterwards.

There is also unfinished business: spec 0007 left "escalation for `blocked`, `not_matched`, `no_helpdesk_account`, and outage refusals" to #11, and spec 0008 left "post escalations to `SLACK_IT_CHANNEL`" and "whether `not_matched`, `not_staff`, and `too_many` escalate" to #11. Escalated conversations from 0008's failure path are today only a ticket in Zammad and a log line.

Compliance scope is GDPR: the IT post and the ticket note carry personal data about the requester, and Slack history sits outside the retention control #12 will add to our own tables.

## Options considered

### Option 1: an audited `escalate` tool on the tool server, a code built IT post from the agent, a code backstop, and a close guard

The tool server gains a `low` tier `escalate` tool that tags and notes (or creates) the thread's ticket, writes its audit pair, and returns the requester's recent blocked attempt count. Agent code, on an `ok` result, ends the conversation and posts a fixed format message to a private IT channel. If the model called the blocked tool but did not escalate, code escalates; the tool server refuses to close a ticket while a blocked attempt is unescalated. Spec 0008's failure path and sweep escalate through the same tool.

**Pros**:
- Every escalation is an audit pair, from any source, satisfying the done when line directly.
- Credentials stay where they are: Zammad behind the tool server, Slack in the agent.
- The flag can't be skipped once the blocked tool is called (backstop plus close guard).
- One escalation path for the dashboard and the eval to count.

**Cons**:
- The most moving parts of the options: a new tool, three queries, a `_meta` key, agent changes, and an amendment to shipped 0008 code.
- The IT post and the escalation are two steps in two processes; a Slack failure after an `ok` escalation leaves the ticket as the only record.

### Option 2: agent only escalation

Agent code handles escalation itself: after a `blocked` result it calls `ticket_update` with a note and posts to Slack. No new tool.

**Pros**:
- Smallest change; no tool server work at all.
- The agent already has every value it needs for the post.

**Cons**:
- No `escalate` audit row: the audit log can't show the escalation, breaking the done when line.
- Tags need a new Zammad capability anyway, which would have to go through the tool server.
- The model has no way to escalate a colleague request or a security worry.

### Option 3: the tool server posts to Slack itself

`escalate` does everything: tags the ticket, writes the audit pair, and posts the IT message with its own Slack bot token.

**Pros**:
- One atomic place for the whole escalation; the audit outcome can include whether the post landed.
- The agent needs no change beyond the prompt.

**Cons**:
- Slack credentials in two processes, widening what a tool server compromise exposes and blurring the boundary the depguard rule and boundary test enforce.
- The tool server has no conversation history, so the post could not list what ran.

### Option 4: code only escalation on every `blocked` result

No `escalate` tool for the model. Whenever a `blocked` result appears, agent code escalates.

**Pros**:
- Fully deterministic for privileged requests; the model can't skip or misuse escalation.
- Fewer tokens in the tool list.

**Cons**:
- The model can't escalate colleague requests or security concerns, which spec 0007 wanted escalated.
- The escalation summary is a fixed text, so IT loses the model's description of what was actually asked and what the real problem was.

## Rationale

Option 1 is the only one that meets all three forces at once. The audit requirement rules out Option 2: the scope asks the audit log to show the escalation, and only the tool server writes it. The credential boundary rules out Option 3: the project's central rule is that one process holds client system credentials and the other holds Slack, and every review gate (depguard, the boundary test) is built around it. Option 4 is attractive for its determinism, but it throws away the two things that make an escalation useful to a one person IT team: the model's summary of the real problem (the printer) and escalation for cases that are not admin requests.

GA is what earns Option 1 its extra parts. Option 1 keeps the model in charge of the summary and the wording, where it is good, and puts code in charge of the parts where a miss would break the safety claim: the backstop guarantees the flag once the blocked tool is called, the close guard stops the one sequence (refuse, then close) that would make the backstop too late, and the `_meta` initiator key keeps the code only reasons out of reach of a prompt injection. The conversation going `escalated` with its result, in one transaction, reuses the exact pattern spec 0008 already uses for `resolved`, so there is no new state handling to get wrong.

Routing 0008's failure path and sweep through `escalate` costs a change to shipped code, but it means "escalated" means one thing everywhere: an audit pair, the `escalated` tag, and an IT post. Without it the dashboard would have to count escalations two ways, and failure escalations would never reach the IT channel, which is the 0008 follow up this feature owns.

The calls made without asking, each with its runner up:
- **`escalate` takes `reason`, `title`, `summary`, with `title` used only on create** (runner up: a separate optional `title`): a required field keeps the schema simple and the create path always has a title.
- **Code only reasons gated by a `_meta` initiator key** (runner up: an agent side filter on the model's tool input): `_meta` is already the trusted channel from agent code (spec 0007), so the tool server can enforce it; an agent side filter would put policy on model input in two places.
- **`ThreadTicketID` reads the ticket id from the `ticket_create` audit result** (runner up: refuse when the id is missing from `_meta`): the id is only missing after a tool call with no answer (spec 0008 AC-11), exactly when the backstop and failure path most need to find the ticket.
- **Plain mrkdwn with `&`, `<`, `>` escaped** (runner up: Block Kit, deferred to #14): exact string tests, and escaping is the one rule that stops model text firing `@channel`.
- **`footer_escalated` as a fixed footer** (runner up: reuse `footer_open`): the open footer invites replies in a thread that is now closed.
- **`privileged_attempts` counted from `denied_policy` outcomes of `privileged_access_change` per Slack id** (runner up: per directory account): blocked rows have a null `requester_account` (spec 0007 AC-12), so the Slack id is the only key they carry.
- **The IT post goes out right after the `ok` escalation is stored** (runner up: at the end of the turn, the first draft): the cross check showed a later failure in the turn, or a crash, would otherwise swallow the post, since a second call only gets `already_escalated`.
- **No startup membership probe for the IT channel** (runner up: `conversations.info` at startup, suggested by the cross check): a private channel needs the `groups:read` scope, a manifest change and reinstall for a warning; the first failed post logs `slack_not_in_channel` instead.
- **`PRIVILEGED_ATTEMPT_WINDOW` as a setting with a `1h` floor** (runner up: a constant 24h): the project rule is no hardcoded limits.

Declined during the design conversation, by your choice: a code tripwire or a classifier model call for detection; an offer to open the ticket instead of opening it; fixed refusal text; a dedicated escalations table or flag column; priority changes or a separate Zammad group; durable retry of the IT post; posting when the escalation failed; posting for `not_staff` and `too_many`; personal data beyond the Slack mention and account in the IT post.
