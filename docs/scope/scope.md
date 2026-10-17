# Scope: AI IT Support Engineer for Noord Logistics

A mock Forward Deployed Engineer engagement: you deploy an AI IT support agent into Noord Logistics B.V., a fictional 80 person Dutch freight forwarder, then measure it honestly and hand it over. It serves the client's one overloaded IT person and, publicly, your portfolio, a LinkedIn post, and a video about what went wrong and how you fixed it. The source brief is a private PRD kept outside this repo.

**Build approach:** Tracer Bullet (prove the whole pipe, Slack to agent to tool server to AD to ticket to audit log, on one real ticket first, then thicken one strand at a time).
**Workflow:** Beta (after `/develop`: `/check verify`, then `/test`). Features that carry the safety claim (tool server, identity check, refusal, approvals, audit log, PII) are tagged `· GA` and add a fresh model `/check review` and `/document`. `/architect` is the recommended first stop for a feature with a real decision, but skippable when you already know the build.

_These are recommendations to keep your build orderly, not requirements. Skip anything that does not fit: if you already know how to build a feature, use `/develop` and skip `/architect`. You decide when a feature is `done`._

## Timeline and ground rules

| Week | Dates (2026) | Goal | Done when |
|---|---|---|---|
| 1 | Oct 19 to Oct 25 | Foundations and the client environment (#1 to #8) | You can log in as a Noord user, get locked out, and raise a ticket by hand |
| 2 | Oct 26 to Nov 1 | Slices 1 and 2, then as far into slices 3 to 6 as you get (#9 to #16) | Unlock and refused escalation both work end to end in Slack |
| 3 | Nov 2 to Nov 8 | Production, eval, handover (#17 to #23) | One command installs it into a fresh cluster and the eval passes its targets |
| 4 | Nov 9 to Nov 15 | Content (#24, #25) | Video and launch post are live, around Nov 16 |

- **Entra timebox:** give the Entra tenant and sync (#7) two evenings, ending Wednesday Oct 21. If it is not syncing by then, switch to AD only and show Entra as a documented phase 2. Either way, it goes in the journal.
- **Cut order if you fall behind:** deferred list first, then slice 6 (KB answers), then slice 3 (reset links). Never cut the guardrails, the audit log, or the eval.
- **Log failures as they happen** in [docs/journal/](../journal/README.md). That journal is the raw material for the post and video.
- **Fictional data only.** Nothing from a real employer or client goes in the repo, video, or posts. The repo is public, so check before every push.

## At a glance

| # | Feature | Phase | Status |
|---|---------|-------|--------|
| 1 | Discovery doc | Foundation | done |
| 2 | Build journal | Foundation | in-progress |
| 3 | Stack & architecture | Foundation | in-progress |
| 4 | Coding standards & tooling | Foundation | planned |
| 5 | Data model | Foundation | planned |
| 6 | Noord directory & seeded mess | Client environment | planned |
| 7 | Entra tenant & sync (timeboxed) | Client environment | planned |
| 8 | Ticketing, knowledge base & Slack workspace | Client environment | planned |
| 9 | Tool server & risk tiers | Slice 1 | planned |
| 10 | Locked out user, end to end | Slice 1 | planned |
| 11 | Privileged request refusal & escalation | Slice 2 | planned |
| 12 | PII redaction & retention | Slice 2 | planned |
| 13 | Password reset links | Slice 3 | planned |
| 14 | MFA re-registration with approval | Slice 4 | planned |
| 15 | Access requests via group owner | Slice 5 | planned |
| 16 | Knowledge base answers | Slice 6 | planned |
| 17 | One command GitOps install | Production | planned |
| 18 | Observability & ops dashboard | Production | planned |
| 19 | Eval ticket set | Evaluation | planned |
| 20 | Eval harness in CI | Evaluation | planned |
| 21 | Eval v1 to final | Evaluation | planned |
| 22 | Handover package | Handover | planned |
| 23 | Public repo & README | Handover | planned |
| 24 | Engagement video & 60 second cut | Content | planned |
| 25 | LinkedIn launch & follow ups | Content | planned |

## Foundations

### 1. Discovery doc · done
The one page brief written as if sales just handed you the account: who Noord is, what hurts, what success means. Everything else is measured against it.
**Done when:** one page covers the client profile, the five pain points, the five success criteria, in scope, and out of scope (hardware, admin tier changes, mailbox and SharePoint permissions).
- [x] Write it: `/develop discovery doc`
code in `docs/discovery.md`

### 2. Build journal · in-progress
A running log of everything that breaks, written the moment it happens: symptom, root cause, fix, time lost, and the clip that shows it. This is the spine of the post and the video.
**Done when:** every incident during the build has an entry, and the index marks which ones make the post and the video.
- [x] Set up the journal and entry template
- [ ] Log each incident as it happens (ongoing, all four weeks)
- [ ] Pick the top three stories for the post and video (week 4)
code in `docs/journal/`

### 3. Stack & architecture · in-progress
Choose the agent language, ticketing tool (or the Postgres fallback), model endpoint (EU residency checked in week 1), cluster host, and packaging, then scaffold a runnable repo. This is the only place tools get picked.
**Done when:** the choices and trade offs are recorded in a spec (this becomes the 3 to 5 key decisions in the architecture doc), and the empty repo builds and runs locally under git.
- [x] Decide the stack (spec): `/architect stack & architecture`
- [x] Scaffold from the decision: `/develop stack & architecture`
- [ ] Check EU residency for the Anthropic API and record the outcome (week 1, spec Follow-up)
- [ ] Verify it: `/check verify stack & architecture`
- [ ] Test it: `/test stack & architecture`
spec [0001](../specs/0001-stack-architecture/index.md) · code in `cmd/`, `internal/`, `db/`

### 4. Coding standards & tooling
Capture conventions from the real scaffold, then install lint, format, pre commit hooks, and a CI skeleton the eval will later run in.
**Done when:** root `AGENTS.md` reflects the real stack, and lint, format, and pre commit run clean in CI.
- [ ] Capture conventions and tooling choices: `/audit`
- [ ] Install the tooling: `/develop tooling`
- [ ] Check it runs clean: `/test`

### 5. Data model · needs a decision · GA
The records the agent owns: the append only audit log, pending approvals, eval runs and results, and tickets if you use the Postgres fallback.
**Done when:** every tool call can be recorded with requester, action, target, approver, and result; audit rows cannot be updated or deleted by the app; eval runs are stored so v1 and final can be compared.
- [ ] Design it (spec): `/architect data model`

## Client environment (Noord Logistics)

### 6. Noord directory & seeded mess · needs a decision
A containerised on prem directory with 80 users, OUs, and groups, rebuilt from one seed script, with the mess planted on purpose.
**Done when:** one script rebuilds the directory from scratch, including stale enabled accounts of leavers, inconsistent group names (`SG-Finance`, `Finance_Users`, `fin-readonly`), groups with no owner, and a privileged group the agent must never touch.
- [ ] Design it (spec): `/architect noord directory`

### 7. Entra tenant & sync (timeboxed) · needs a decision
A free Entra test tenant with a subset of users synced, so the hybrid, mid migration story and MFA tickets are real. Timeboxed to Oct 21; AD only fallback after that.
**Done when:** a subset of users appear in Entra, a few are deliberately left unsynced (the sync gap), and MFA methods can be cleared through the API; or the fallback is taken and recorded in the journal.
- [ ] Design it (spec): `/architect entra tenant & sync`

### 8. Ticketing, knowledge base & Slack workspace · needs a decision
The client's helpdesk as you find it: a ticketing system with an API, a deliberately outdated knowledge base, and the Noord Slack workspace with the bot app registered.
**Done when:** you can log in as a Noord user, get locked out, and raise a ticket by hand; the KB holds articles that contradict each other or reference old systems; the bot can receive a Slack message.
- [ ] Design it (spec): `/architect ticketing, knowledge base & slack`

## Slice 1: Locked out user, end to end

### 9. Tool server & risk tiers · needs a decision · GA
The only path into client systems. Every tool has a risk tier enforced in code: read, low, medium, blocked. Allow list of OUs and groups, deny list of privileged groups, kill switch to read only, and an audit row for every call.
**Done when:** `user_lookup`, `account_unlock`, and `ticket_create` / `ticket_update` work through it; a call against a denied group fails at the tool layer whatever the prompt says; flipping the kill switch blocks all writes; every call writes an audit row.
- [ ] Design it (spec): `/architect tool server & risk tiers`

### 10. Locked out user, end to end · needs a decision · GA
The thinnest real thread: a user says they are locked out in Slack, the agent confirms the Slack user matches the directory user, unlocks, opens and closes a ticket with a summary, and the audit log shows it all. This is the walking skeleton.
**Done when:** a real locked Noord user gets unlocked from Slack in under 60 seconds with a closed ticket and audit row; a Slack user who does not match the directory gets no write action.
- [ ] Design it (spec): `/architect locked out user flow`

## Slice 2: Refusal & escalation

### 11. Privileged request refusal & escalation · needs a decision · GA
The video hook. "I need Domain Admin to fix a printer" is refused, explained, offered a printer ticket, escalated to IT with full context, and flagged.
**Done when:** privileged requests are refused even when phrased cleverly; the `escalate` tool hands a human the full context; the audit log shows the blocked attempt and the escalation.
- [ ] Design it (spec): `/architect refusal & escalation`

### 12. PII redaction & retention · needs a decision · GA
Keep personal data out of logs and traces, and keep ticket bodies only as long as the retention setting says. Lands before more data flows through the system.
**Done when:** logs and traces from the unlock and refusal flows show no names, emails, or ticket bodies in clear; old ticket bodies are purged per the retention setting.
- [ ] Design it (spec): `/architect pii redaction & retention`

## Slice 3: Password reset links

### 13. Password reset links · needs a decision
Send a self service reset link after the identity check, the other half of the Monday morning lockout pile.
**Done when:** a verified user gets a working reset link from Slack, the ticket closes with a summary, and an unverified user gets none.
- [ ] Design it (spec): `/architect password reset links`

## Slice 4: MFA re-registration with approval

### 14. MFA re-registration with approval · needs a decision · GA
First medium risk action. Clearing MFA methods posts an Approve or Deny card to IT in Slack; no answer in time escalates the ticket.
**Done when:** MFA is cleared only after an IT approval in Slack; a deny or a timeout leaves MFA untouched and escalates; the audit row names the approver. If Entra was dropped, MFA tickets escalate with context instead.
- [ ] Design it (spec): `/architect mfa re-registration with approval`

## Slice 5: Access requests via group owner

### 15. Access requests via group owner · needs a decision · GA
"Can I get access to X" routed to the right owner. The agent resolves messy group names, finds the owner, asks them to approve, and escalates when nobody owns the group.
**Done when:** a request for "finance" resolves to the right group (or asks which one), the owner gets the approval card, unowned groups escalate to IT, and privileged groups are never offered.
- [ ] Design it (spec): `/architect access requests`

## Slice 6: Knowledge base answers

### 16. Knowledge base answers · needs a decision
Answer common questions from the KB with links, and handle contradictory or outdated articles honestly instead of guessing.
**Done when:** answers cite the article they came from; when articles contradict, the agent says so and escalates or prefers the newer one by a stated rule.
- [ ] Design it (spec): `/architect knowledge base answers`

## Production

### 17. One command GitOps install · needs a decision
Package everything and deploy through GitOps to an EU hosted cluster, with Postgres, secrets management, and Slack over Socket Mode so nothing listens on the internet.
**Done when:** a single command installs the whole thing into a fresh cluster; secrets are not in git; Slack reaches the agent over Socket Mode with no inbound route; model inference goes to an EU region endpoint.
- [ ] Design it (spec): `/architect gitops install`

### 18. Observability & ops dashboard · needs a decision
One trace per conversation and a simple dashboard the IT person can read: tickets handled, escalations, approvals waiting, errors, cost.
**Done when:** you can follow one Slack conversation through every tool call in a trace, redacted; the dashboard shows the last 24 hours at a glance.
- [ ] Design it (spec): `/architect observability & ops dashboard`

## Evaluation

### 19. Eval ticket set · needs a decision
Fifty realistic tickets with the expected outcome for each: about 20 routine, 10 MFA, 10 access, 5 KB, 5 adversarial (privilege escalation, impersonation, prompt injection in a ticket body), with Dutch, typos, vague wording, and sync gap users mixed in.
**Done when:** the 50 tickets live in one data file, each with message, user, expected outcome (resolve, escalate, refuse), and expected tool calls; the mix matches the plan.
- [ ] Design it (spec): `/architect eval ticket set`

### 20. Eval harness in CI · needs a decision
Replays every ticket against a fresh seed of the environment, scores it automatically, and runs on every prompt or tool change.
**Done when:** one command runs all 50 and prints resolution rate, correct escalation, unsafe actions, wrong actions, time to resolve, and cost per ticket; CI runs it on prompt or tool changes; each run is saved.
- [ ] Design it (spec): `/architect eval harness`

### 21. Eval v1 to final
Run v1, publish it as is, fix the top failures, run again. The gap between the two runs is the story.
**Done when:** v1 and final runs are both saved; unsafe and wrong actions are 0; every failure category is explained in the journal, fixed or not.
- [ ] Run and save v1 (log every failure in the journal)
- [ ] Fix the top failures: `/debug` per failure
- [ ] Run and save final, compare to the targets

## Handover

### 22. Handover package
What an FDE leaves behind so the client's IT person can run, tune, and switch off the agent alone.
**Done when:** the architecture doc, the tool and risk tier spec (in sync with code), the eval report (v1 vs final, every failure category), the runbook (deploy, monitor, tune prompts, add a tool, rotate secrets, switch to read only), the data processing note, and the ROI estimate with every assumption stated all exist.
- [ ] Write it: `/develop handover package`

### 23. Public repo & README
The public face: the GitHub repo with public CI, a README with the problem, a 30 second demo GIF, the headline eval numbers, and the one command install.
**Done when:** the repo is public, a stranger can rebuild Noord from the seed scripts, and a final check confirms no real company data is anywhere in it.
- [ ] Publish it: `/develop public repo & readme`

## Content

### 24. Engagement video & 60 second cut
The 6 to 8 minute engagement story that opens on the refusal, plus a 60 second vertical cut of the hook and the numbers. The "what broke" section comes from the journal.
**Done when:** both cuts are published on YouTube and LinkedIn, with real timestamps visible in the demo.
- [ ] Record clips as you go (environment tour week 1, first refusal week 2, eval run week 3)
- [ ] Ship the 60 second cut first
- [ ] Ship the long video

### 25. LinkedIn launch & follow ups
A launch post that leads with the real eval numbers and the two or three most interesting failures, then three weekly follow ups.
**Done when:** the launch post is live with the repo and video in the first comment, and the three follow ups (risk tiers in code, the eval set, EU infrastructure) are scheduled.
- [ ] Draft the launch post: `/anthropic-skills:linkedin-authority-post`
- [ ] Schedule the three follow ups

## Deferred
Out of scope for this build pass, kept so the plan stays honest. Pick up only if you finish early.
- **Shadow mode**: the agent drafts, a human approves, before going autonomous · needs a decision
- **Weekly report**: the agent posts tickets handled, escalations, time saved · needs a decision
- **Dutch first replies**: answer Dutch tickets in Dutch
- **Teams channel**: a second channel next to Slack · needs a decision
- **Multi agent handoff**: another agent calls these tools with scoped permissions · needs a decision
- **Mailbox and SharePoint permissions**: stretch from the PRD · needs a decision · GA

Never in scope: hardware, printers, on site fixes, and admin tier changes such as Domain Admins or Global Admin.

## Legend

**The decision box.** Every feature carries exactly one, the sub task whose label ends with `(spec)`. Its wording varies (`Design it (spec)` normally, `Decide the stack (spec)` on Stack & architecture), so skills locate it by that `(spec)` suffix, never by an exact label. Every other box is an execution box and `/architect` never ticks one.

**Feature lifecycle**: the scope updates as a feature moves; each row is what it shows and who sets it:

| State | Set by | The feature shows |
|---|---|---|
| `planned` · needs a decision | `/scope` | one box: `Design it (spec): /architect <feature>` |
| `in-progress` (designed) | **`/architect` at spec capture** | `Design it` ticked; spec linked; `Build it: /develop <feature>` plus 2 to 5 milestones; the tier's closing boxes (`Verify it` Alpha+, `Test it` Beta+, `Review it` and `Document it` GA) |
| `in-progress` (building) | `/develop` | milestone sub boxes tick one by one; code pointer filled |
| `in-progress` (verified) | `/check verify` | `Build it` and milestones ticked; `Verify it` ticked |
| `done` | **you, when you decide it is**; `/sync` reconciles | boxes you ran ticked, skipped ones marked skipped; for Beta and GA the suggested point to call it done is after `/test` |

- **Next step** = the first unticked box (always a command or a tracked milestone).
- **needs a decision** = run `/architect` first; otherwise straight to `/develop` (or `/audit` for standards & tooling). The tag drops once the spec is captured.
- **Atomic build tasks live in the spec's `## Build plan`, not here**: the scope carries only the milestone rollup.
- **Status** `planned` → `in-progress` → `done`, plus `existing` (pre workflow) and `dropped` (de scoped, kept for history).
- **Workflow tier tag** beside a heading (`· GA`) sets that feature's rigor above the project default; no tag inherits Beta.
- **Workflow** (header line) is the project default, what runs after `/develop`: **Beta** = `/check verify` then `/test`; **GA** adds a fresh model `/check review` then `/document`. A feature built on an unratified decision (an `Assumed` spec) stays flagged, but that never blocks `done`.
- **Pointer line** (`spec <n> · code in <path>`): the spec link added by `/architect`, the code path by `/develop`.
