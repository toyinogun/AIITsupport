# Tools and risk tiers

_Generated from the tool registry by `Catalog` in `internal/tools/catalog.go`. Do not edit by hand:
`TestCatalogMatchesHandoverDoc` fails when this file and the code disagree. After a registry or
refusal change, run `go test ./internal/tools/ -run TestCatalogMatchesHandoverDoc -update`._

This is every tool the tool server can serve, with its risk tier, what guards it, and what the audit
log keeps. How the pieces fit is in [architecture.md](architecture.md); how to add a tool is in
[runbook.md](runbook.md#add-a-tool).

## Risk tiers

| Tier | Meaning |
|---|---|
| read | Reads only, or writes to this conversation's own ticket. Runs right away. |
| low | A small change to the requester's own account or ticket. Runs right away, inside its guards. |
| medium | A change a human must approve in Slack first. The tool only files the request; the change runs after the click. |
| blocked | Never runs. The call is refused and audited, and the agent escalates. |

Every call, whatever its tier, passes the same dispatcher steps and writes an intent row to `audit_log`
before it runs and an outcome row after. If the intent row cannot be written, nothing runs. If the
outcome row fails after the action ran, only the intent row is left.

## Summary

| Tool | Tier | What it does | Guards beyond identity and schema | Registered |
|---|---|---|---|---|
| `user_lookup` | read | reads, or writes this conversation's ticket | none | always |
| `account_unlock` | low | changes the requester's own directory account | kill switch, `TOOL_WRITE_LIMIT` | always |
| `ticket_create` | low | reads, or writes this conversation's ticket | `TICKET_CREATE_LIMIT` | always |
| `ticket_update` | low | reads, or writes this conversation's ticket | none | always |
| `privileged_access_change` | blocked | nothing, it never runs | always refused | always |
| `escalate` | low | reads, or writes this conversation's ticket | none | always |
| `kb_search` | read | reads, or writes this conversation's ticket | none | always |
| `group_search` | read | reads, or writes this conversation's ticket | none | always |
| `group_access_request` | medium | adds the requester to a directory group | group owner approval in Slack, kill switch, `TOOL_WRITE_LIMIT` | always |
| `password_reset_link` | low | issues a single use password reset link | kill switch, `TOOL_WRITE_LIMIT` | only when the `RESET_*` settings are set |
| `mfa_clear` | medium | clears the requester's Entra MFA methods | IT approval in Slack, kill switch, `TOOL_WRITE_LIMIT` | only when the `GRAPH_*` settings and `SLACK_IT_APPROVERS` are set |

### `user_lookup`

**Tier:** read · **Acts on:** the requester's own account

What the model reads: Look up the requester's own Noord account: whether it is enabled, whether it is locked, when the lock ends, and the directory note IT keeps on the account. Takes no arguments and can only return the requester's own account.

Takes no arguments.

### `account_unlock`

**Tier:** low · **Acts on:** the requester's own account

What the model reads: Unlock the requester's own Noord account if it is locked. Takes no arguments and only ever acts on the requester's own account; it cannot unlock anyone else. Reports whether the account was locked.

Takes no arguments.

Result keys kept in the audit log: `was_locked`, `locked`.

### `ticket_create`

**Tier:** low · **Acts on:** the requester's own account

What the model reads: Open a helpdesk ticket for the requester for this conversation, with a short title and a plain text description of the problem and what was done. One ticket per conversation; use ticket_update after that.

| Argument | Required | Rule | Kept in the audit log |
|---|---|---|---|
| `title` | yes | one line, 1 to 200 characters | no (free text) |
| `body` | yes | 1 to 10000 characters | no (free text) |

Result keys kept in the audit log: `ticket_id`, `ticket_number`.

### `ticket_update`

**Tier:** low · **Acts on:** this conversation's ticket

What the model reads: Update this conversation's ticket: add an internal note, set the state (open, pending with pending_until, or closed), or both, and with kb_review tag it for a knowledge base review. Only this conversation's ticket can be changed.

| Argument | Required | Rule | Kept in the audit log |
|---|---|---|---|
| `state` | no | one of `open`, `pending`, `closed` | yes |
| `pending_until` | no | one line, 1 to 64 characters | yes |
| `note` | no | 1 to 10000 characters | no (free text) |
| `kb_review` | no | true or false | yes |

Result keys kept in the audit log: `ticket_id`, `state`, `kb_review_tagged`.

### `privileged_access_change`

**Tier:** blocked · **Acts on:** nothing named

What the model reads: Use for any request for admin rights, privileged group membership, or changes to admin accounts. It is always refused; tell the user, and escalate to IT.

| Argument | Required | Rule | Kept in the audit log |
|---|---|---|---|
| `summary` | yes | 1 to 1000 characters | no (free text) |

### `escalate`

**Tier:** low · **Acts on:** this conversation's ticket

What the model reads: Hand this conversation to IT with a summary: for a refused privileged request, a request to act for someone else, or a security concern. Tags this conversation's ticket for IT, or opens one if there is none yet, and notifies IT. Use it once, after privileged_access_change when that applies. Ends the conversation.

| Argument | Required | Rule | Kept in the audit log |
|---|---|---|---|
| `reason` | yes | one of `privileged_request`, `other_person`, `security_concern`, `assistant_failure`, `restart`, `manual_action`, `approval_denied`, `approval_expired`, `approval_failed`, `approval_cancelled`, `no_group_owner` | yes |
| `title` | yes | one line, 1 to 200 characters | no (free text) |
| `summary` | yes | 1 to 10000 characters | no (free text) |

Result keys kept in the audit log: `ticket_id`, `ticket_number`, `reason`, `initiator`, `privileged`, `created`, `privileged_attempts`, `privileged_window_hours`.

### `kb_search`

**Tier:** read · **Acts on:** nothing named

What the model reads: Search Noord's IT knowledge base (the help articles) for the requester's how to question. Returns at most a few published articles, best match first, each with its id, title, category, updated date, a stale flag set by code, and its text. Cite an article only with the marker [[kb:<id>]]; never write a link.

| Argument | Required | Rule | Kept in the audit log |
|---|---|---|---|
| `query` | yes | one line, 2 to 100 characters | no (free text) |

Result keys kept in the audit log: `count`, `answer_ids`, `stale_ids`.

### `group_search`

**Tier:** read · **Acts on:** nothing named

What the model reads: Find groups the requester can ask to join, by 1 to 3 short English search terms (translate and fix typos first). Returns only groups for the requester's department. Never guess a group name; use this.

| Argument | Required | Rule | Kept in the audit log |
|---|---|---|---|
| `terms` | yes | a list of 1 to 3 items, each one line, 2 to 40 characters | no (free text) |

Result keys kept in the audit log: `count`, `truncated`.

### `group_access_request`

**Tier:** medium · **Acts on:** one group, once the precheck resolved it

What the model reads: Ask a group's owner to add the requester to it. Use an exact name from group_search and a short reason in the requester's words. Needs this conversation's ticket first. Nothing changes until the owner approves in Slack; the assistant's code tells the requester the outcome. Never say access is granted.

| Argument | Required | Rule | Kept in the audit log |
|---|---|---|---|
| `group` | yes | one line, 1 to 64 characters | yes |
| `reason` | yes | 1 to 200 characters | no (free text) |

Result keys kept in the audit log: `added`, `already_member`, `ticket_closed`.

Kept while the approval is pending: `expires_at`, `reused`, `approver_account`.

### `password_reset_link`

**Tier:** low · **Acts on:** the requester's own account

What the model reads: Send the requester a single use link to set a new password themselves; it also clears a lockout. Use it when they forgot their password or it expired, or when they report a lockout but the account is not locked. Needs this conversation's ticket first (call ticket_create). The assistant's code posts the link; never write or repeat a URL yourself. Ends the conversation.

Takes no arguments.

Result keys kept in the audit log: `link_id`, `expires_at`, `expires_in_minutes`, `voided_previous`, `ticket_noted`.

### `mfa_clear`

**Tier:** medium · **Acts on:** the requester's own account

What the model reads: Ask IT to clear the requester's MFA sign in methods (phone and Authenticator app) so they can register again, for example after a new or lost phone. Needs this conversation's ticket first (call ticket_create). Nothing changes until an IT person approves in Slack; the assistant's code tells the requester the outcome. Never say the methods are cleared.

Takes no arguments.

Result keys kept in the audit log: `cleared_authenticator`, `cleared_phone`, `ticket_closed`.

Kept while the approval is pending: `expires_at`, `method_count`, `reused`.

## Refusals

A refusal is a safe stop, not an error. The model sees only the code and the fixed sentence; the audit
log stores the outcome and the fixed reason, never request data. Failures (`unavailable`, `failed`,
`not_confirmed`, `invalid_arguments`) are not refusals and are not listed here.

| Code | Audit outcome | Stored reason | What the model is told |
|---|---|---|---|
| `blocked` | denied_policy | tool is blocked | Privileged changes are never made by the assistant. Escalate to IT. |
| `not_matched` | denied_identity | requester not matched | No single active Noord account matches this Slack user's email. |
| `ambiguous` | denied_identity | requester ambiguous | More than one Noord account matches this Slack user's email. |
| `no_helpdesk_account` | denied_identity | no helpdesk account | This account has no helpdesk profile. |
| `read_only` | read_only | account changes are paused | Account changes are paused by IT right now. |
| `protected` | denied_policy | group is protected | That group is outside what the assistant may change. |
| `rate_limited` | denied_policy | rate limited | Too many recent requests of this kind for this user. |
| `has_ticket` | denied_policy | thread already has a ticket | This conversation already has a ticket; update it instead. |
| `no_ticket` | denied_policy | no ticket for this thread | This conversation has no ticket yet. |
| `not_yours` | denied_policy | ticket belongs to someone else | That ticket belongs to another user. |
| `ticket_closed` | denied_policy | ticket is closed | That ticket is already closed; open a new conversation for a new problem. |
| `already_escalated` | denied_policy | thread already escalated | This conversation is already with IT. |
| `escalate_first` | denied_policy | escalate first | A refused privileged request in this conversation has not been escalated yet; call escalate before closing the ticket. |
| `account_disabled` | denied_policy | account disabled | This account is disabled, so no reset link can be sent. |
| `already_pending` | denied_policy | approval already pending | An MFA reset for this account is already waiting for IT approval in another conversation. |
| `not_in_entra` | denied_policy | not in entra | This account has no cloud sign in that the assistant can change, so IT needs to handle it. |
| `nothing_to_clear` | denied_policy | nothing to clear | This account has no phone or Authenticator app sign in method to clear. |
| `other_methods` | denied_policy | other methods | This account has other sign in methods the assistant can't clear, so IT needs to handle it. |
| `protected` | denied_policy | account not eligible | That group is outside what the assistant may change. |
| `approval_pending` | denied_policy | approval pending | This ticket is waiting on an IT approval and closes by itself when IT decides. |
| `already_member` | denied_policy | already a member | The requester already has this access; tell them to sign out and back in, then close the ticket. |
| `no_owner` | denied_policy | no group owner | This group has no owner who can approve, so IT needs to handle it. |
| `owner_is_requester` | denied_policy | owner is requester | The requester owns this group, so IT needs to confirm the change. |
| `not_requestable` | denied_policy | group not requestable | This is not a group the assistant can request for this person. |
| `too_many_pending` | denied_policy | too many pending | This person already has the maximum number of access requests waiting; they can ask again once one is decided. |
| `one_per_thread` | denied_policy | one request per conversation | This conversation already has an access request waiting. Ask for another group in a new conversation. |
| `already_pending` | denied_policy | approval already pending | A request for this group is already waiting for its owner in another conversation. |
| `approval_pending` | denied_policy | access request pending | This ticket is waiting on the group owner's decision; the assistant's code handles it when they decide. |
| `protected` | denied_policy | recheck failed | That group is outside what the assistant may change. |
