# 0033. sqlc guessed the type of a nullable ticket id wrong three times

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #11 Privileged request refusal & escalation
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
The new `ThreadTicketID` query reads the ticket id out of the newest `ticket_create` audit result, which can be missing (no ticket yet, or a result the payload cap cut). sqlc kept generating the wrong Go type for it:
- `(result->>'ticket_id')::bigint` came out as a plain `int64`, which fails to scan a NULL.
- `NULLIF((result->>'ticket_id')::bigint, 0)` came out as `bool`.
- `CASE WHEN ... THEN (...)::bigint END` came out as `interface{}`, and adding `ELSE NULL::bigint` went back to a non null `int64`.

Separately, `make check` stopped at `gen-check`, because it runs `git diff --exit-code -- internal/db/queries` and the new sqlc output was not committed yet.

## What I thought it was
That a cast or `NULLIF` would tell sqlc the column is nullable, as the spec's "nullable bigint" sketch assumed.

## What it actually was
sqlc treats a cast as NOT NULL whatever is inside it, and its inference for `NULLIF` and `CASE` over a JSON expression is unreliable. `gen-check` compares against the git index by design, so it can only pass once the generated code is committed.

## How I fixed it
Wrapped the lookup in a scalar subquery with `COALESCE((...), 0)::bigint`, so the query always returns one row and 0 means "no ticket" (Zammad ticket ids start at 1). `TestPgThreadTicketID` checks none, the newest id, another thread, and a cut result against real Postgres. For `gen-check`, I confirmed `make gen` is stable by hashing `internal/db/queries` before and after a second run. Not committed yet.

## What I would tell a team doing this for real
(draft) When sqlc can't infer nullability, return a sentinel from one always present row rather than fighting its type guesser.

## Evidence
- Error text or screenshot: the three generated signatures above (`int64`, `bool`, `interface{}`)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
