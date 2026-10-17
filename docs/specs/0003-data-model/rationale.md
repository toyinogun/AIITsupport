# 0003. Agent owned data model: rationale

Decision record for [index.md](index.md). `/develop` builds from the index; this file explains why.

## Context

The agent acts on a real company's directory: it unlocks accounts, clears MFA, and adds people to groups. The whole engagement's claim is that it does so safely and that every action can be answered for. That puts the audit log at the centre: if it can be edited by the app, or a write can happen without a row, the claim is false. Spec 0001 already fixed three cross component rules this feature must make concrete: no audit row means no write, approvals go around the model, and durable state lives in Postgres.

The forces:
- **Two processes with different trust.** The agent talks to the model and Slack; the tool server holds client credentials. The database has to respect the same split, or the boundary is only as strong as the agent's code.
- **Append only conflicts with "record before you act".** A row must exist before the action, but the action's result is only known after it, and rows can't be updated.
- **Approvals must change state** (pending, then decided, then executed) and survive restarts, while clicks can race and arrive late.
- **Personal data** (GDPR, a Dutch employer's staff): message text and ticket bodies need a retention path, while the audit record must never be deleted.
- **The eval must compare v1 and final**, but CI runs against a throwaway database.
- **Tiny scale**: 80 users, a few hundred calls a week. Partitioning, queues, or caching would be cost without benefit.
- **Local dev today** runs both binaries from one `.env` as the Postgres superuser, so any role separation must work in Compose, in tests, and in the cluster.

## Options considered

### Option 1: Append only intent and outcome rows, guarded mutable approvals, per binary roles (chosen)

The audit log takes an intent row before the action and an outcome row after, linked by `call_id`; grants plus a trigger make it append only. Approvals are one mutable row with guarded `UPDATE … WHERE status = …` transitions. Each binary has its own login role with narrow grants.

**Pros**:
- Fully append only and write first at once; a crash shows honestly as an intent with no outcome.
- Races resolve in the database with no locks or extra code.
- The database enforces who writes what, independently of app code.

**Cons**:
- Two rows per call, read with a self join.
- Four roles to create in every environment, and a config rename.

### Option 2: One audit row, outcome filled in by a narrow column update

Insert before acting, then `UPDATE` only the outcome columns through a column level grant, guarded by `WHERE outcome IS NULL`.

**Pros**:
- One row per call; simpler reads.

**Cons**:
- The table is no longer append only; "the app can't change audit rows" becomes "the app can change some columns once", which is weaker and harder to show.

### Option 3: Event sourced everything (append only approvals too, hash chained audit)

Approvals become an event table whose latest event is the state; audit rows carry a hash of the previous row.

**Pros**:
- The strongest tamper evidence and a complete approval history in one place.

**Cons**:
- Every state check becomes a "latest event" query, races need explicit locking, and audit inserts must be serialised for the chain. A lot of machinery for 80 users when the audit log already records every approval move.

### Option 4: One shared app role, enforcement in code

Owner role for migrations plus one role for both binaries.

**Pros**:
- The simplest setup; no config rename.

**Cons**:
- The agent could insert audit rows or approve requests at the database level; the boundary rests entirely on code review.

## Rationale

Option 1 is the only one that satisfies both spec 0001 rules (write first, and app cannot change audit rows) without weakening either. Option 2 trades the append only claim, the thing the video and handover lean on, for a slightly simpler read. Option 3 buys tamper evidence the threat model doesn't need: the realistic risk is an app bug or a prompt injection driving a bad write, not an attacker with owner access, and the trigger already blocks the owner from casual edits. Option 4 throws away the cheapest real defence the two process design gives us.

Smaller calls made with full context:
- **`text` plus `CHECK` over enum types**: adding a value is a plain constraint swap, with no `ALTER TYPE` ordering rules. The runner up (enum types) gives nicer generated Go types but makes evolution clumsier.
- **uuid for approval ids and `call_id`, bigint identity elsewhere**: approval ids travel in Slack button values and must not be guessable; everything else benefits from compact, ordered keys. The runner up (uuid everywhere) costs index size and loses natural ordering.
- **`clock_timestamp()` for `audit_log.occurred_at`**: `now()` is the transaction start, so a refused pair written in one transaction would share a time and hide ordering. `id` still gives the strict order.
- **Thread columns instead of an FK from audit to conversations**: the tool server only receives the thread from `_meta`, and an FK would make an audit insert fail if the agent had not yet written the conversation, which would block a write for a bookkeeping reason.
- **Roles outside migrations**: migrations then never need superuser or `CREATEROLE`, and no password ever lands in a migration. The runner up (migrations create `NOLOGIN` roles) keeps role names in one place but widens what the migration user can do.
- **Per binary URL names**: one `.env` drives both binaries locally, Kubernetes sets each per pod, and the `.env.example` drift test keeps covering every name. Overriding in the Makefile would have needed a drift test exception.
- **Cost stored at insert time**: Grafana can sum it with plain SQL; `pricing_date` keeps the basis honest. The runner up (compute at read time) keeps prices fixable but Grafana can't read `pricing.yaml`.
- **Eval in Postgres plus a committed JSON export**: Postgres gives SQL comparisons and dashboards; the JSON export survives CI's throwaway database and makes v1 versus final a reviewable diff.
- **The audit log is kept for the engagement, never purged**: at this volume there is no storage pressure, and any purge path would need a hole in the trigger.
- **Allow list, not omit list, for stored keys**: an omit list fails open. A tool that forgets to omit `body` writes ticket text into a table that can never be erased. An allow list fails closed. The runner up (omit secrets only) was the first draft, until the cross check showed it contradicted the security model.
- **Execution after approval gets a new `call_id`**: the original call is already closed with `pending_approval`, and append only forbids a second outcome. The `approval_id` ties the request, decision, and execution together.
- **`last_seq` counter for message order**: the row lock serialises appends, so the order is gapless. The runner up (`MAX(seq) + 1`) is correct but makes the losing writer fail, and then someone has to retry.
- **Micro dollars as `bigint`**: exact, and it sums cleanly in Grafana with no numeric type to map into Go. The runner up (`numeric(12,6)`) is also exact, but it drags `pgtype.Numeric` into every caller.
- **Mark Slack events seen before processing**: at most once. A dropped message costs the user one retype, while a duplicate could repeat a write. The runner up (mark after) gives at least once delivery and leans on audit for safety.

## Cross check

An independent read only review (a different model) found the design sound on Postgres 17: the grants and trigger give the claimed errors, identity columns need no sequence grants, the audit to approvals FK check runs as the table owner, and the approve against deny race is safe under READ COMMITTED. It also found gaps, all closed in the index before acceptance:
- CI ran `goose up` as the superuser.
- The test helper was unsafe when packages run in parallel.
- How approvals tie into audit `call_id`s was unspecified.
- Outcome rows had no stated source for their identity columns.
- `Run` had cancel and panic edge cases.
- The omit list leaked personal data.
- Several conversation and eval query semantics, the sqlc type overrides, and the trigger's SQLSTATE were unstated.
