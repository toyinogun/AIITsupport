# 0018. Eval harness: rationale

## Context

> ⚠️ Premise note: the scope says "each run is saved" and spec 0003 stores runs in `eval_runs`, but a CI job that starts its own Postgres loses that database when the job ends. A saved run that disappears cannot support "v1 against final". The design therefore makes the report file in the repo the durable record, and treats the database rows as the working copy (plus a long lived copy when `EVAL_DATABASE_URL` is set).

The scope promises an eval that "passes its targets" and a gap between a first run and a final run that tells the story. Spec 0017 fixed the 50 tickets and what "right" means for each in terms the audit log can show. What is missing is the machine that plays them: it has to put the agent in a known world, send each ticket, play the human approvers, read what happened from the audit log, score it, and put the world back for the next ticket.

Four facts shape it. The agent was built to run without Slack, with fakes behind every interface (spec 0008), so the loop can be driven in process. The tool server alone holds the credentials for AD, Entra, and Zammad, so the harness must not hand those to the agent. The fresh seed machinery exists but is slow for Zammad (about 2 minutes), and tickets share senders, so state would leak between tickets unless something undoes it. And every full run spends real API money, on a public repo whose CI must never expose the key to a fork.

## Options considered

### Option 1: Agent in process, real tool server as a child process, throwaway containers

The harness starts Postgres, Samba, and Zammad itself, runs the production `toolserver` binary against them with a fake Graph in front of Entra, and runs the agent in the harness process with the real model and fake Slack. Scoring reads the audit log.

**Pros**:
- The tools, the audit log, the approval routes, and the credential split are the production ones, so the score means something.
- The harness cannot point at a real stack, because it has no setting for one.
- Laptop and CI run the same way.

**Cons**:
- Slow start (Zammad) and a heavy runner (three services and a child process).
- A second binary (`cmd/eval`) needs Administrator access to the throwaway container for the undo step.
- Entra is still faked, so MFA behaviour against the real tenant is covered only by the live verify runs.

### Option 2: Everything faked in process

The agent runs against `agenttest` fakes, including a fake tool layer and a fake directory.

**Pros**:
- Fast, free of containers, and runnable anywhere.

**Cons**:
- A fake tool cannot show a wrong tool behaviour, a refusal that the real policy would make, or an audit row that the real recorder would write. Spec 0017 defined unsafe and wrong actions from the audit log, which a fake would have to invent.
- The headline numbers would describe the fake, not the system.

### Option 3: Drive the live stack through a real Slack workspace

Post the tickets into a real Slack workspace and watch the running Compose or cluster stack.

**Pros**:
- The most realistic path, including Slack itself.

**Cons**:
- Slow, flaky, rate limited, and tied to a real workspace; impossible to reset between tickets; hard to run in public CI; it would put adversarial text into a real workspace and a real helpdesk.

## Settled during the interview

| Question | Pick | Runner up |
|---|---|---|
| Environment fidelity | Option 1 | everything faked |
| CI trigger | path filtered push to `main` plus manual dispatch | nightly |
| Models | one model per run, Sonnet by default, Haiku by flag | both in one run |
| CI gate | hard fail on unsafe or wrong actions; report the rest | gate on every target |
| Reset | fresh Samba and Zammad once; a cloned database and a restarted tool server child per attempt; outside undo and verify for AD, Zammad, and the fake Graph (changed after the cross check; first pick was undoing database rows) | fresh Samba per ticket |
| Approver | click through the real approval path | call the tool server routes directly |
| Saved runs | tables plus a report file in the repo | tables only |
| Outcome source | `conversations.outcome`, refuse from the audit log | keep the 0017 status inference |
| Metrics | per expected outcome, counts first | one overall pass rate |
| Repeats | once by default, `--repeat` flag | always three |
| Cost cap | hard cap from config | print only |
| Runner | `k3sprox`, same repo triggers only | GitHub hosted |
| Result columns | `attempt`, `failed_checks`, `actual_refusal_code`, `approval_wait_ms` (`conversation_id` dropped: with a database per attempt it would always be 1) | fewer columns |
| Run columns | `repeats`, `prompt_hash`, `tools_hash` | add cost cap and stop reason columns |
| Transcripts | failed tickets only, in the report file | all or none |
| Provisioning | own throwaway containers | the running Compose stack |
| Undo credentials | `internal/seed/evalreset`, called by `cmd/eval` | run `cmd/seed` as a child |
| Subcommands | `run`, `compare`, `report` | add `list` |
| Ticket error | score it, keep going, stop after three model failures in a row | stop on the first error |
| Dirty reset | stop the run as failed | warn and continue |
| API key | GitHub Environment `eval` limited to `main` | plain repository secret |

## Rationale

Option 1 is the only one that measures the system the client would run. The scope's own cut order says never to cut the eval, and the headline claim is about unsafe and wrong actions, which exist only as audit rows written by the real tool server. Option 2 would be quick but would score a fake; Option 3 is realistic but cannot be reset, which breaks the rule that each ticket starts clean.

The cost of Option 1 is time and weight, and the design handles both without fighting them: Zammad is seeded once and then cleaned by an undo step that a verify step polices, instead of rebuilding 50 times. The database is the part that cannot be undone: the audit log is append only and approvals are referenced by it, and rate limits and the privileged attempt window are derived from audit rows. The cross check pointed this out, so each attempt gets a clone of a migrated template database and a restarted tool server, which is cheap and makes every attempt start from the same empty state. The outside undo step is where the credential rule bends, so the bend is made small and visible: one package, one allowed binary, a container the harness started itself, and both deny lists updated.

Running the tool server as the real child process, not in process, keeps the rule that the agent half never receives client credentials, and it exercises the real config loading. Reading `conversations.outcome` from spec 0016 replaces a status inference that would have drifted, while keeping the refusal rule from the audit log that 0017 chose. Making the report file the durable record, and the database the working copy, follows from the throwaway CI database and keeps the evidence public and reviewable, which the post and the handover report need.

The engineer's choices were followed throughout. One was refined: `stop_reason` is kept inside the run's `summary` JSON, not as a column, so the migration stays small and `CompareEvalRuns` is untouched.

## Evidence: how the pieces already fit

- `internal/agent/agenttest` holds fakes for `Users`, `Poster`, `Tools`, `Approvals`, a scripted `Model`, and a `Store` (spec 0008, "the eval drives the agent with fakes"). The harness swaps in the real model, store, and MCP clients and keeps the Slack fakes.
- `eval_runs` and `eval_results` exist with the `noord_eval` grants (migration `00002_data_model.sql`). The role reads only the audit log, conversations, and usage, which is not enough for approval cards, approvals, or failure rows, so the harness reads evidence as the owner of the attempt database and uses `noord_eval` only to write results.
- The cross check (an independent model) found 21 decision gaps; this spec applies its fixes. Findings that changed the design: approvals cannot be deleted (audit foreign key), limits and windows live in audit rows, and the settle signal and the approval id needed named sources.
- `eval/pricing.yaml` already prices both models; the cost cap and the cost per ticket use the stored `llm_usage` cost, so no price is hardcoded.
- `ticketset.Load`, `CheckLock`, and `tools.CodeForReason` exist from spec 0017 and are the only inputs the harness takes about expectations.
- The seed code already builds a fresh Samba and a fresh Zammad and refuses a partial top up, which is why the harness starts fresh once and undoes narrowly, never tops up.
