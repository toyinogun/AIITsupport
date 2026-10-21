# 0013. Access requests via group owner: rationale

The decision record behind [index.md](index.md). `/develop` builds from `index.md`; this file says why.

## Context

Noord's groups are a mess on purpose (spec 0004): three names for finance (`SG-Finance`, `Finance_Users`, `fin-readonly`), some groups with no owner, a share owned by a leaver whose AD account is still enabled, and a privileged group the agent must never touch. People ask for access in their own words, in Dutch and English, with typos. Today every such request is a ticket Ruben (Noord's one IT person) handles by hand, even though the person who can say whether access makes sense is usually the group's business owner.

The pieces this feature needs mostly exist. Spec 0012 built approvals for MFA: a medium tool that defers, approval routes on the tool server, a card, a click, a tick, and a settle. Its schema already allows `approver_kind` `group_owner`, and `Fence.CheckGroup` was built and left unused for this feature. What is missing: no tool reads or writes groups, no code reads `managedBy`, the card always goes to the IT channel, and nothing links an AD owner to a Slack account. The tool server holds no Slack credential and must not gain one lightly (spec 0001: it holds client system credentials; Slack belongs to the agent).

Forces: the model must never pick the approver or decide anything (root `AGENTS.md`: approvals go around the model); a leaver can still own a group in AD; owners are business people who are not in the IT channel and answer within hours, not minutes; the eval (#19) needs deterministic refusal codes; and free text the user types must not reach the tables spec 0010 keeps forever.

## Options considered

### Option 1: Department scoped search, owner DM card, AD account as approver (chosen)

The model calls a read tool that returns candidate groups for the requester's department and asks the user to pick and give a reason. The medium tool resolves `managedBy`, refuses when there is no valid owner, and defers with the owner's AD account as the approver. Agent code finds the owner in Slack by email, posts the card as a DM, and on a click sends the clicker's confirmed email so the tool server maps it to an account and checks it.

**Pros**:
- Approver identity uses the same join key as requester identity (mail), and the Slack credential stays in the agent.
- Reuses spec 0012's routes, tick, and settle; only the approver kind and the card destination are new.
- A leaver owner is caught by Slack, which AD cannot do.

**Cons**:
- The agent's report of the clicker's email is trusted, as `_meta` is.
- Needs department tags written into AD, a new seed field to keep accurate.
- More turns: the model searches, then asks, then requests.

### Option 2: Code picks the group, card to the IT channel with the owner mentioned

The tool server fuzzy matches "finance" to one group and posts an IT channel card that @mentions the owner, who must be in that channel.

**Pros**:
- Fewer turns, and no DM handling.
- Approver Slack ids could be resolved by IT once and kept in config.

**Cons**:
- A silent wrong match grants the wrong access, and the planted ambiguity is exactly where it would happen.
- Owners would have to join the IT channel, and every reason becomes visible to everyone there.
- A config map of owner Slack ids goes stale the day an owner changes.

### Option 3: Every access request becomes an IT ticket

The agent collects the group and reason and escalates with `manual_action`. IT decides and adds by hand.

**Pros**:
- No new write, no new approval kind, almost no code.
- Nothing can be added by a wrong click.

**Cons**:
- Does not meet the scope's done when (the owner gets the card), and keeps Ruben as the bottleneck.
- The privileged and unowned routing that is the interesting part of the demo disappears.

### Option 4: The tool server gets its own Slack token

As Option 1, but the tool server calls `users.lookupByEmail` itself and stores the owner's Slack id, so the click check stays a plain Slack id comparison.

**Pros**:
- The decide check is unchanged from spec 0012.
- A step toward the hardening follow up (the tool server owning the card).

**Cons**:
- A second Slack credential, on the process that holds AD, Entra, and Zammad credentials, widens the blast radius.
- Half of the hardening without the other half (the agent still posts and relays the click) buys little.

## Rationale

Option 1 fits the forces best. The safety claim rests on code deciding who approves and code checking who clicked, and Option 1 does both with the identity rule the project already trusts (exact `mail`, enabled, in the user OUs). It sends the decision to the person who can judge it, in the place they will see it (a DM), which the scope asks for. It also turns the planted mess into visible behavior: the ambiguous finance names make the model ask, the unowned groups and the leaver's share route to IT for different, checkable reasons, and the privileged group never appears.

Option 2's silent match is the failure mode the planted mess is built to catch. Option 3 is the honest fallback if Slack DMs or `managedBy` turn out unreliable, and it remains the path for every refusal. Option 4 moves a credential for little gain; the real hardening (the tool server owning the whole card) stays a follow up of spec 0012.

The engineer chose department scoping over showing every non privileged group. The recommendation was to show every group, since an 80 person company's group list is not secret and cross team access is a normal ask. Department scoping is stricter and keeps the model's view small, but it sends every cross team request to IT and needs department tags that must be kept accurate. This spec builds the engineer's choice and records the cost in Consequences.

### Calls made while writing (the recommended items)

- **Where the department tag lives**: the group's `info` (Notes) attribute in a strict `noord-departments: A, B` form, parsed fail closed. Runner up: a config map from department to groups, which duplicates the directory and drifts. `extensionAttribute*` is not in a default Samba schema.
- **Search does the filtering in Go**: one LDAP read of every group in the allowed OUs (about 25 at Noord), then department, security bit, fence, and term matching in code. No user text enters an LDAP filter. Runner up: an LDAP substring filter, which is faster at scale but adds escaping risk for no benefit at this size.
- **How the agent learns the owner's email**: the single approval view (`GET /approvals/{id}`), read before every group card post (first post, repost, adopt alike: one path). Runner up: carrying it in the pending result and stripping it before the model, like `reset_url`; that needs a second path for adopted approvals.
- **The `Approval` hook takes the precheck's result**: one signature change, so the owner found in the precheck reaches the approval request. Runner up: a second hook type for group tools, which doubles the registry validation.
- **Owner unreachable ends as `expired`**: reusing the status avoids a new value across several CHECK constraints in specs 0003 and 0012; the `approval.cancel` audit pair keeps it distinguishable. Runner up: a new `cancelled` status.
- **A deny closes the ticket through `ticket_update` by code**: the existing audited tool and the existing code initiator, rather than a new side effect inside the decide route.
- **One `not_requestable` code** for missing, outside the OUs, distribution, and other department groups, so refusals reveal nothing about groups a person cannot see. `protected` stays separate because it routes differently (`privileged_request`).
- **Search terms are not audited**: they are free text and spec 0010 keeps `audit_log` free of it; the `count` is enough to see the call happened.
- **Department tag values**: the table in `index.md`, following who uses each system at a freight forwarder (Management sees finance reports, the CRM serves sales and customer service, operations shares serve warehouse and transport). The engineer can change any row in the seed without touching code.

### Changes from the cross check (2026-10-20)

An independent read on another model found gaps the first draft missed. The engineer chose to apply every recommended fix:

- **The demo would fail.** Every Finance user was already in `SG-Finance`, so the seed takes `noor.elamrani` out of it and the live runs use her.
- **Check order.** `Fence.CheckGroup` calls a missing group `protected`, so the precheck checks denied names, then resolves the group (`not_requestable`), then checks nesting.
- **The medium plumbing was MFA only.** A group handler family now sits beside the MFA one; the execute timeouts move to the dispatcher; there is a `group` audit target kind and a list field kind for `terms`.
- **The decide flow.** It is now spelled out: `Dispatcher.ResolveDecider`, a `Decider` value, and `approvals.decider_slack_id`, so every audit `approver_slack_id` stays a Slack id.
- **Crashes and Slack errors.** A cancel survives a crash through `cancel_reason`. Slack errors are classed (only "not found" cancels, a transient error retries, and three failed attempts cancel).
- **Smaller fixes:**
  - one pending request per conversation, so one ticket never serves two approvals;
  - canonical group names;
  - a settle table keyed by tool;
  - a strict `info` grammar;
  - search filters cheapest first;
  - the card shows nested access, labels the reason unverified, uses Amsterdam time, and accepts clicks only from the Slack id it was sent to;
  - an approval after an escalation leaves IT's ticket open.
