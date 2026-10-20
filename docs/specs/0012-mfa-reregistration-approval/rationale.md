# 0012. MFA re-registration with approval: rationale

The decision record for [index.md](index.md). `/develop` skips this file.

## Context

Noord's IT person gets a steady trickle of "new phone, my Authenticator is gone" and "I can't get my sign in codes" tickets. The fix is always the same: clear the person's registered MFA methods (multi factor authentication, the second step after a password) in Entra, so that the next sign in asks them to register again. It is also the most abused helpdesk action in the industry: an attacker who talks a helpdesk into clearing someone's MFA owns that account at the next sign in. So this is the first action in the project that must not run on the requester's word plus the Slack identity check alone.

The plumbing for a human gate already exists but has never carried a real action. Spec 0003 built the `approvals` table with guarded transitions, `Recorder.Defer`, `Decide`, and `Expire`, and left open who runs expiry, how the agent learns to escalate, how an approval stuck in `approved` after a crash recovers, the `APPROVAL_TIMEOUT` default, and how far the approve endpoint trusts the agent. Spec 0007 routes tier `medium` tools to `Defer` and left the real medium tool, the approve endpoint outside `tools/list`, and the home of `SLACK_IT_APPROVERS` to this feature. Spec 0001 fixed the shape: a click goes from the agent straight to a tool server endpoint, never through the model, and the model loop is never resumed.

Forces:
- **The credential boundary.** Only the tool server holds Graph (Microsoft's API for Entra) credentials; Slack belongs to the agent. Clicks arrive on the agent's Socket Mode connection (Slack's outbound WebSocket), whose payloads are not signed, so the tool server cannot check on its own that a click came from Slack.
- **Least privilege in Entra.** The tool server's app can today write phone methods only (spec 0005). A real "lost my phone" ticket needs the Authenticator app method gone too, and the demo users sign in with the Authenticator app. Spec 0005 AC-11 promises the app cannot issue a Temporary Access Pass (a one time sign in code); any widening must keep that.
- **No audit row, no write; durable state in Postgres.** The approval, its decision, its execution, and its expiry must each leave an audit pair; nothing that matters may live only in memory, because both binaries restart.
- **The agent cannot read `approvals`** (spec 0003 grants), yet it must update the Slack card and tell the user when an approval ends, including after a restart and when nobody clicked.
- **One escalation path** (spec 0009): denials, timeouts, and failures should reach IT through the `escalate` tool and the IT channel post, not a second mechanism.
- **GDPR.** The card and audit rows carry account names and Slack ids; phone numbers must stay masked; the audit log is permanent.
- **Timing.** Week 2 of a three week build; this is slice 4 and GA tier (fresh model review and documentation). It must reuse the existing pieces rather than add infrastructure.

## Options considered

### Option 1: Agent posts the card and runs the clock; the tool server decides and executes behind plain JSON routes (chosen)

The model calls a medium tool `mfa_clear`; the dispatcher prechecks and defers it. Agent code posts a Block Kit card (Slack's JSON layout for messages with buttons) to the IT channel and remembers it in a new agent owned `approval_cards` table. A click goes to `POST /approvals/{id}/decision` on the tool server, which decides and, on approve, runs the clear inside the recorder. A 30 second agent tick calls `POST /approvals/expire` and `GET /approvals/{id}` for every unsettled card, so timeouts, missed clicks, and restarts all settle through one function.

**Pros**: reuses every existing piece (Defer, Decide, Expire, escalate, the IT channel); Slack stays in the agent and Graph in the tool server; one settle loop covers every way an approval can end.
**Cons**: the approve endpoint trusts the agent's word for who clicked; the agent polls (a few cheap requests every 30 seconds); a new agent table.

### Option 2: Tool server owns the clock and escalates itself

A tool server ticker expires approvals and escalates the Zammad ticket directly; the agent polls only to update Slack.

**Pros**: expiry does not depend on the agent being up.
**Cons**: two loops instead of one; escalation leaves the single `escalate` path; the agent still has to poll to update the card and tell the user, so nothing is saved.

### Option 3: Tool server owns the Slack card

The tool server gets its own Slack app, posts cards, and hears clicks on its own socket, so it can verify the clicker without trusting the agent.

**Pros**: real independence from a compromised agent for the approval step.
**Cons**: a second Slack app, token, and socket; Slack credentials move into the process that holds every client credential; a large change for one slice. Better as a hardening step when a real client relies on it.

### Option 4: Phone methods only, no permission change

Keep the tool server app as it is and clear phone methods only.

**Pros**: no change to Entra permissions.
**Cons**: the Authenticator app stays registered, so the ticket this feature exists for is not actually fixed, and the demo shows nothing changing.

## Rationale

Option 1 fits the forces with the least new machinery. The approval rules that matter (only listed approvers, never the requester, once, before expiry) are already enforced in SQL by spec 0003, so the remaining question is plumbing, and the agent already owns Slack, the IT channel, the per thread workers, and the one escalation path. Making the agent the clock means the thing that must tell people about an outcome is the thing that discovers it; the `approval_cards` row with a null `settled_at` is the durable "still owes an announcement" marker, so a crash anywhere between expiry and announcement is recovered on the next tick.

The clicker trust gap is real but is the same trust the tool server already places in the agent for `_meta` identity (spec 0007): a compromised agent could already put any email in `_meta`. Option 3 closes it at the cost of a second Slack integration in the most sensitive process; it is the named hardening step, not this slice.

For methods, the granular `UserAuthMethod-MicrosoftAuthApp.ReadWrite.All` next to the phone one fixes the real ticket while still denying Temporary Access Pass issue, which the broad `UserAuthenticationMethod.ReadWrite.All` would allow. The exact permission name is verified live on day one, with the broad one as a journaled fallback, the same pattern spec 0005 used.

Approving executes synchronously in the decision request so the approver's click and the audit pair for the clear sit next to each other, and a stuck `approved` row fails after a grace instead of running again, because a delete that may have half happened must never be replayed blind (spec 0003: an approval executes at most once).
