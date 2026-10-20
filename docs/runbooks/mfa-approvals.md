# Runbook: MFA approvals

This page is for Noord's IT person. It says what you see when someone asks the assistant to clear their MFA methods, how you approve or deny, what each ending means, and how you clear an account by hand when the assistant could not (spec [0012](../specs/0012-mfa-reregistration-approval/index.md)).

MFA methods are the extra sign in steps on a Microsoft 365 account: the Microsoft Authenticator app and phone numbers for codes or calls. Clearing them lets the person register again, for example after a new or lost phone. The assistant never clears anything on its own. It only asks, and nothing changes until you click Approve.

## What you see

When an employee tells the assistant their Authenticator is gone or they can't get codes, the assistant opens a ticket and posts a card in the IT channel (`SLACK_IT_CHANNEL`) as a new message:

- `MFA reset request`
- `Requester:` the person's Slack name and their account
- `Ticket:` a link to the helpdesk ticket (or its id)
- `Methods:` how many of each method they have, for example `Authenticator app (1), phone (2)`. A card reposted later says `Methods: listed in the ticket note.` instead; the ticket's internal note has the same summary.
- `Expires:` the time the request ends on its own (UTC)
- `Confirm with the person by phone or in person before approving.`
- the **Approve** and **Deny** buttons

The card never shows a phone number or a device name, only types and counts.

## Before you click Approve

We recommend you check that the person really is who the card names, by a channel you trust: call them on a number you already know, or see them in person. A Slack message from their account is not enough, because a stolen account is exactly the case where someone would ask for this. That check is your judgment; the assistant cannot make it for you.

## Approve or deny

- **Approve** asks you to confirm ("Clear this person's MFA methods?"). The assistant then deletes their Authenticator and phone methods, adds a note to the ticket naming you, closes the ticket, and tells the person to set up the Authenticator again at their next sign in (KB article "Set up the Authenticator app"). The card changes to `Approved by <you>. Cleared n Authenticator and m phone methods. Ticket closed.`
- **Deny** changes nothing on the account. The ticket is escalated to you, the person is told IT didn't approve and someone will follow up, and the card says `Denied by <you>. Escalated.`

Only the Slack ids in `SLACK_IT_APPROVERS` (on the tool server) may decide, and nobody may approve their own request. The list is fixed when the request is made, so a later change to the setting does not affect requests already waiting. If you are on the list and the request is your own, you can still Deny it (that cancels it), but not Approve it. A cancel still escalates the ticket to IT, under its own headline.

If a click is refused, only you see why:

| You see | It means |
|---|---|
| `Only IT approvers can decide this request.` | your Slack id is not in `SLACK_IT_APPROVERS` for this request |
| `You can't decide your own request.` | you asked for it yourself |
| `This request has expired.` | the time on the card has passed; nothing was changed |
| `This request was already decided.` | someone clicked first; the card updates shortly |
| `Your decision is being processed. The card will update when it's done.` | the decision is recorded but the answer took long; the card updates within a minute |
| `I couldn't record your decision. Please try again in a minute.` | the tool server or the database did not answer; nothing changed, click again later |
| `I can't find this request anymore.` | the card is not one the assistant knows (for example from an old deployment) |

## How a request can end

| Card says | What happened | What you do |
|---|---|---|
| `Approved by ... Ticket closed.` | the methods are cleared and the ticket is closed | nothing |
| `Approved by ... Ticket left open.` | the methods are cleared, but the ticket could not be closed (it was closed or merged already, or Zammad did not answer) | close the ticket if it is still open |
| `Denied by ... Escalated.` | you or another approver denied it | follow up with the person on the escalated ticket |
| `Cancelled by the requester ... Escalated.` | the person who asked denied their own request (they are on the approver list) | check whether they still need help, then close the ticket |
| `Expired with no decision. Escalated.` | nobody decided within `APPROVAL_TIMEOUT` (default 30 minutes) | follow up on the escalated ticket; the person can ask again |
| `Approved by ..., but the clear did not finish (<reason>). Escalated, check the account by hand.` | the approve was recorded but the clear stopped | check the account by hand (below) |

The reasons on a clear that did not finish:

- `account changes are paused`: the kill switch was set to read only after the approve. Nothing was deleted.
- `account not eligible`: the account was disabled, moved, or added to a protected group after the request. Nothing was deleted.
- `not in entra`: the account is no longer a synced cloud user. Nothing was deleted.
- `other methods`: a method the assistant can't remove appeared (for example a code only app, a security key). Nothing was deleted.
- `nothing to clear`: the methods were already gone when the clear ran.
- `method delete failed`: Microsoft Graph refused a delete partway through. Some methods may be gone and others not; the audit row has the counts.
- `execution not confirmed`: the approve was recorded but the clear never confirmed within `APPROVAL_EXECUTE_GRACE` (default 5 minutes), for example because the tool server restarted mid clear. The assistant never runs it again. The account may be cleared, partly cleared, or untouched, so look.

Every ending that is not a clean approve escalates the ticket, so it lands in your queue. If the assistant could not match the person to their account any more (for example their Slack account was deactivated), you get a "not matched" post in the IT channel instead and the person is told to contact IT directly.

## Check or clear an account by hand

We recommend you look before you change anything:

```bash
make entra-mfa NAME=<username>
```

That lists the person's methods with the tool server app (types only). If phone or Authenticator methods remain and you want them gone:

```bash
make entra-mfa NAME=<username> CLEAR=1
```

Or use the Entra admin center: **Users**, the person, **Authentication methods**, then delete each method. Graph will not delete the default method while others remain, so delete the phones first and the Authenticator last (journal 0041).

A method the assistant can't clear (a code only app, a security key, Windows Hello) is yours to remove in the admin center. Clearing MFA does not sign the person out; if a phone was stolen, also revoke their sign in sessions in the admin center (**Revoke sessions**).

## Where the record is

Every step is in `audit_log`, joined by the approval id. Look one up:

```sql
SELECT tool_name, outcome, error, approver_slack_id, created_at
FROM audit_log WHERE approval_id = '<id>' ORDER BY id;
```

You will see the `mfa_clear` request pair (`pending_approval`), the `approval.approve` or `approval.deny` pair naming the clicker, and for an approve the execution pair (`mfa_clear`, with the approver) or an `approval.expire` or `approval.abandon` pair. The `approvals` row holds the status (`pending`, `approved`, `denied`, `expired`, `executed`, `failed`).

## Settings

| Setting | Where | Default | What it does |
|---|---|---|---|
| `SLACK_IT_APPROVERS` | tool server | empty | who may decide; empty means `mfa_clear` is not offered at all and MFA requests escalate |
| `APPROVAL_TIMEOUT` | both binaries | `30m` | how long you have to decide (5m to 24h). Set the same value on both: nothing checks it, and a shorter agent value can let a request expire with no card |
| `APPROVAL_EXECUTE_TIMEOUT` | tool server | `60s` | the deadline for one clear |
| `APPROVAL_EXECUTE_GRACE` | tool server | `5m` | when an approve whose clear never confirmed is marked failed |
| `MFA_DENIED_GROUPS` | tool server | `SG-IT-Admins,APP-Helpdesk-Agents` | members (at any depth) never get a clear; they always escalate |
| `APPROVAL_POLL_INTERVAL` | agent | `30s` | how often the assistant checks for timeouts and outcomes |
| `APPROVAL_DECIDE_TIMEOUT` | agent | `90s` | how long a click waits for an answer; keep it above `APPROVAL_EXECUTE_TIMEOUT` |

While the agent is down, cards are not updated and timeouts are not announced; a late click is still refused. When the agent comes back, its first check catches up.

The tool server app's Graph permissions are in [entra-tenant.md](entra-tenant.md#the-mfa-clear-spec-0012).
