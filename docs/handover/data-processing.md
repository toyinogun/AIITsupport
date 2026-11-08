# Data processing note

This note tells you, as the client, what personal data the assistant processes, why, where it goes, and how long it stays. It is written for your privacy contact and your auditor. It is a technical description, not legal advice; your own data protection officer should confirm the roles and the legal basis. Noord Logistics is fictional, and so is all the data in this repo.

## Roles

- **Noord Logistics** is the controller: it decides why the assistant exists and whose tickets it handles.
- **The engineer who built and handed over the assistant** acted as a processor during the engagement. After handover, Noord runs it on its own infrastructure, and the processor role ends except for the sub processors below.
- **Sub processors** that receive personal data: Anthropic (model inference), Slack (the chat), and Cloudflare (encrypted database backups, EU jurisdiction). Microsoft (Entra ID) and the systems Noord already runs (Active Directory, Zammad) are Noord's own systems, not new recipients.

## Purpose

First line IT support for identity and access: unlocking accounts, sending password reset links, MFA re-registration, access requests, knowledge base answers, and handing everything else to IT with the context attached. Plus the audit trail of every action, which is one of the reasons the client asked for this.

## Whose data, and which data

Noord staff who message the assistant, and the IT person and group owners who approve requests.

| Data | Where it comes from | Where it is stored |
|---|---|---|
| Slack user id, Slack profile email, DM channel id | Slack | Postgres (`conversations`, `audit_log`), Slack |
| Directory account name, display name, enabled and locked state, department, group memberships, the account description | Active Directory | read live and sent to the model each turn; the account name is kept in Postgres |
| MFA method types and counts (never the phone number itself) | Entra ID | read live; counts kept in the approval row |
| Message text the person writes, and the assistant's replies | Slack | Postgres (`conversation_messages`), Slack, and sent to the model |
| Ticket title, body, and notes | written by the assistant | Zammad |
| Who approved what, and when | Slack clicks | Postgres (`approvals`, `audit_log`) |
| Token counts and cost per model call (no text) | the agent | Postgres (`llm_usage`) |

The assistant never handles passwords. A reset link lets the person set their own password on a page served from Noord's cluster; the link is stored only as a hash, and the audit log drops it. The assistant does not process special category data, and it is told to escalate rather than act when a message is about someone else.

## Where it is stored

Everything at rest stays in Noord's own k3s cluster, plus encrypted database backups in a Cloudflare R2 bucket in the EU jurisdiction. Secrets are sealed in git and can only be opened by the cluster. The code repo and CI hold no personal data: the eval uses fictional Noord data only.

## Where model inference runs

**This is the one place data leaves the EU.** To answer a message, the agent sends the conversation so far (the person's messages, the assistant's replies, tool results, and a short block with the requester's username, display name, department, account state, and directory note) to Anthropic's API, through its global endpoint. The Anthropic API offered no EU region when this was built (checked 2026-10-21). This does not meet success criterion 3 in the [discovery doc](../discovery.md), and it is the most important open item in this note.

What limits the exposure today:

- Only the conversation goes to the model. No password, no reset link, no MFA phone number, and no client system credential ever does.
- Names and departments are masked in escalation summaries posted to the IT channel (spec [0009](../specs/0009-refusal-escalation/index.md)).
- Anthropic keeps API request data under its own retention terms; check the current terms and your agreement with them before go live.

How to close it: the model client (`internal/llm`) is built to swap the provider. A Vertex AI `eu` or Amazon Bedrock `eu.` case would keep inference in the EU, with its own price rows in `eval/pricing.yaml` and a fresh eval run. It is listed in the scope as the deferred item "EU inference".

## How long it is kept

One setting, `TICKET_RETENTION_DAYS` (default 30, allowed 1 to 365), drives the purges. The clock starts when a conversation or ticket ends. Full detail and how to check the purges ran: [data-retention.md](../runbooks/data-retention.md) (spec [0010](../specs/0010-pii-redaction-retention/index.md)).

| Data | Kept for |
|---|---|
| Message text in Postgres | N days after the conversation ended, then set to empty |
| Assistant tickets in Zammad (tagged `noord-assistant`) | N days after the ticket was closed or merged, then deleted with their history |
| `audit_log`, `approvals`, `conversations` rows, `llm_usage` | the whole engagement, append only. These hold ids, account names, and allow listed tool arguments, never message text. This is a deliberate tradeoff: they are the audit evidence the client asked for |
| Logs and traces | follow the cluster's retention; they hold no names, emails, or message text (every value outside an allow list prints as `[redacted]`) |
| Backups | the bucket's retention, set at install |
| Slack history | the Slack workspace plan's retention, outside the assistant's control |
| Model provider | the provider's retention terms, outside the assistant's control |

Your other helpdesk tickets (anything not tagged `noord-assistant`) are never touched by the purge.

## Security measures

- The agent, which talks to the model, holds no credential for AD, Entra, or Zammad. Only the tool server does, and it acts only on the person asking, as identified by code, never by the model.
- Admin tier changes are blocked in code. MFA clears need an IT approval, group access needs the group owner's approval, and every action writes an audit row before and after it runs.
- A kill switch stops every account change in about a minute ([runbook.md](runbook.md#switch-to-read-only-or-off)).
- Traffic between the components stays inside the cluster behind default deny network policies; the database roles are separate per program.

## Rights requests

- **Access:** the person's rows can be found by Slack id or account name in `conversations`, `audit_log`, and `approvals`, and their tickets in Zammad.
- **Erasure:** message text and assistant tickets go after the retention window. The audit rows cannot be deleted by the assistant (the table is append only), so an erasure request for them is a decision for Noord's data protection officer, weighed against the audit purpose.
- **Objection:** a person can always raise a ticket with IT directly instead of using the assistant.

## Open items before a real go live

1. EU bound inference (above).
2. A second factor on the reset page, so a compromised Slack account cannot reset a password by itself.
3. A retention limit for the audit tables, decided with the auditor.
4. A signed agreement with each sub processor.
