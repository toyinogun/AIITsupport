# 0008. Rationale: locked out user flow

The decision record behind [index.md](index.md). `/develop` builds from the index; this file explains why.

## Context

Feature #10 is the scope's walking skeleton: the first time a real Noord employee's message goes from Slack through the agent and the tool server into Active Directory and Zammad, and back. Everything before it built the pieces in isolation: the data model (spec 0003), the directory (0004), the helpdesk and the Slack listener with its holding reply (0006), and the tool server that refuses anything outside its fence (0007). What is still missing is the part in the middle that decides, for one message, who is asking, what to do, in what order, and what to say when it can't.

The forces are specific. The done when is two sided: a real locked user is unlocked from Slack in under 60 seconds with a closed ticket and an audit row, and a Slack user who doesn't match the directory gets no write action at all. Spec 0001 already fixed the loop's shape (a hand written loop on the Anthropic Go SDK, at most 8 model turns, a 20 second tool timeout, a 45 second wall clock, 2 retries, 30000 tokens per ticket) and the rule that agent code, never the model, attaches the requester to every tool call. Spec 0007 already makes every tool act only on the requester, so the tool layer is safe whatever the model does; what it can't do is make the experience good, keep IT informed, or stop a stranger from costing model calls.

Compliance scope is GDPR: Noord is a Dutch employer and the messages are about its staff. A user's message, name, department, and account name go to the model provider, a processor outside the cluster, and the conversation history is stored as personal data that #12 will purge. Audit logging is not negotiable here; every tool call is already audited by the tool server, and every conversation the agent gives up on has to be visible to IT.

The operating reality is one overloaded IT person at an 80 person company, one agent replica, Socket Mode with at most once event delivery, and an eval (#20) that will replay 50 tickets through this code and score whether it did the right thing. If this is undecided, #11 to #16 each invent their own idea of a turn, and the eval measures something other than production.

## Options considered

### Option 1: Code checks the requester, then a bounded loop lets the model drive the tools

Agent code runs the Slack profile gate and calls `user_lookup` itself before the model runs; a stranger gets a fixed reply. A match is handed to the model as context, and the model drives the ticket and the unlock through the tool server's tools, following a procedure in the system prompt. Code owns the limits, the ticket id, the conversation status, and a failure path that always tries to leave a ticket.

**Pros**:
- The "no write for a stranger" half of the done when holds before any model call, so it can't depend on the prompt.
- Saves a model turn on every conversation (the model starts knowing whether the account is locked).
- The model's choices are what the eval measures, and the same loop serves every later slice by adding tools and prompt sections.

**Cons**:
- Two extra calls before every message, and an audit pair for a lookup the user didn't ask for.
- The model can still skip or misorder the ticket; only the eval catches it.

### Option 2: A code workflow for lockouts, the model only for language

Code classifies the message (with one cheap model call or keywords), and for a lockout runs the procedure itself: lookup, ticket, unlock, close. The model only phrases replies and handles anything that isn't a lockout.

**Pros**:
- Fully predictable for the most common ticket: the same calls in the same order every time, the fastest and cheapest path.
- Ticket handling can't be forgotten.

**Cons**:
- It isn't the product the engagement sells: the eval would measure a classifier and a script, and the video's "the model chose to refuse" story disappears.
- Every later slice (MFA, access requests) needs its own coded workflow, so the code grows with each one while the model does less.
- Misclassification sends a lockout down the generic path or a non lockout into the unlock script.

### Option 3: The model does everything, code only bounds the loop

Code checks only the Slack profile; the model calls `user_lookup`, and the tool server's identity refusal is what stops a stranger.

**Pros**:
- The least agent code; the tool server is already the fence.
- One place (the prompt) describes the whole flow.

**Cons**:
- A stranger still costs a model call or two, and the "no match" reply is whatever the model writes.
- `conversations.directory_account` has no natural writer, and the model has to read a refusal and decide to stop, which is exactly the kind of thing the eval would find it doing badly.

## Decision

Option 1. See [index.md](index.md) for the build spec.

## Rationale

The done when has a safety half and a speed half, and Option 1 is the only one that settles the safety half in code while leaving the speed half to a short, ordinary loop. Spec 0001's rule that code owns the requester extends naturally from "code puts identity in `_meta`" to "code checks identity before the model runs", and it fills `directory_account` from a source that can't be argued with. Option 3 is simpler on paper, but it makes the refusal of a stranger depend on the model handling a tool error well, which is precisely what the done when says must not happen.

Option 2 is the strongest alternative and would be the right call for a product that only unlocks accounts. It loses because this engagement exists to show an agent that decides, with a fence that holds when the decision is wrong, and because each later slice would need another hand written workflow. The cost of choosing Option 1 is that the model might mishandle the ticket. You chose not to add a code backstop for that; the eval will show how often it happens, and the backstop can be added later as a small change at the end of the turn.

The remaining calls follow from the operating reality. DMs only keeps account details out of shared channels and removes the second person in a thread problem. Ticket first means a refused or failed unlock already has a ticket for IT. Closing straight after the unlock is what makes 60 seconds reachable. One turn per thread with a small concurrency cap keeps history consistent without a queue system. A started tool call finishing on its own timeout trades a few seconds of worst case latency for never telling the user a write didn't happen when it did. The restart sweep escalates instead of resuming, because resuming would mean repairing half finished tool exchanges in stored history, and replaying a write is never safe.

Smaller calls made while writing the spec, each with its runner up:
- **Per message identity check** rather than once per conversation: it catches a deactivated user or a changed email mid conversation for half a second per message. Runner up: check once and trust the stored account, which is faster but goes stale.
- **The requester block as a second, uncached system block** rather than a synthetic `user_lookup` exchange in history: the model sees verified facts without being told it made a call it didn't make, and the cached prefix stays stable. Runner up: a fake tool exchange, which some models follow more literally but which pollutes the stored history.
- **Footers appended by code** rather than written by the model: the "reply in this thread" nudge is reliable and the eval can assert it. Runner up: a prompt instruction, which drifts.
- **Fixed texts for every refusal and failure** rather than model written ones: they cost nothing, they are the same every time, and the eval asserts them. Runner up: let the model phrase them, which reads friendlier but needs a model call in exactly the cases where the model may be the problem.
- **`AGENT_MAX_RESPONSE_TOKENS` at 1024**: a lockout reply is a few sentences and a ticket note a paragraph; 1024 leaves room without letting one runaway response eat the ticket budget. Runner up: 4096, the more common default, which makes a `max_tokens` stop less likely but a runaway costlier.
- **`AGENT_MAX_CONCURRENT` at 4**: a Monday pile of lockouts at an 80 person company is a handful at once; 4 keeps each under the 60 second target without pushing the model API's rate limits. Runner up: 8, safe too, but there's no measured need.
- **Bedrock refused at startup** rather than half built: spec 0001 makes Bedrock config, not a rewrite, but the EU residency check decides whether it's needed; building it now would be untested code. Runner up: build both providers now.
- **Escalation ticket body holds the user's last message**: IT needs to know what was asked, and the text goes only to Zammad, where ticket text already lives (spec 0007). Runner up: a fixed body only, which leaves IT reading the Slack thread instead.

### From the cross check (another model, 2026-10-18)

An independent read of the first draft found load bearing gaps; you chose to apply every recommended fix. The ones that changed the design:

- **`user_lookup` hidden from the model.** Code already calls it every message, and a second call by the model would break AC-7's exact audit count and add nothing (`account_unlock` reports the lock state itself). Runner up: leave it listed, and tell the prompt not to call it.
- **A per requester hourly cap on new conversations** (`AGENT_CONVERSATIONS_PER_HOUR`). Every top level DM is a new thread with a fresh token and turn budget, so without it one staff member could run up unbounded model cost; the tool server's limits cap tools, not model calls. Runner up: accept the risk and document it.
- **The sweep reruns the identity step** instead of trusting the stored row, because the tool server needs an email in `_meta` and the row holds only the Slack id; it also rechecks a user who may have left since. Runner up: post `restart` and escalate with no ticket, which skips the rerun but leaves IT nothing to work from.
- **A stale window on the sweep,** so a new pod during a rolling deploy doesn't escalate turns the old pod is still running; the sweep runs again after the window so a recent crash is still caught. Runner up: rely only on a `Recreate` deployment, which #17 does anyway, but which a misconfigured chart would silently break.
- **The ticket id and `resolved` are stored in the same transaction as the tool result** that produced them, so a crash can't leave a ticket the conversation doesn't know about (which would turn the escalation's `ticket_create` into a `has_ticket` refusal with no id to note on).
- **Detached contexts with named budgets** for started tool calls, their stores, posts, and the escalation ticket, plus `AGENT_SHUTDOWN_GRACE`, so a shutdown or a passed deadline never cuts a write's record or a reply mid flight. The cost is a worst case turn of about 95 seconds.
- **Per thread first in, first out workers** rather than a mutex per thread, because Go mutexes don't promise arrival order; a queue cap of 10 with a `busy` reply bounds memory, and a `shutdown` reply covers messages still waiting at exit.
