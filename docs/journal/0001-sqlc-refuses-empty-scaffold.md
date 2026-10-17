# 0001. sqlc refuses to generate on an empty scaffold

**Date:** 2026-10-17 · **Week:** 0 (before week 1) · **Feature:** #3 Stack & architecture
**Time lost:** about 5 minutes (estimate) · **Story worthy:** no

## What I saw
The spec says the scaffold is done when `make gen` (sqlc) leaves no diff. On a repo with one empty migration and no queries yet, it failed:

```
# package queries
error parsing queries: no queries contained in paths .../db/queries
```

## What I thought it was
Expected it. sqlc needs at least one query, and a scaffold has none yet.

## What it actually was
sqlc treats an empty queries folder as an error, not as "nothing to generate". So a fresh scaffold cannot pass `make gen` without at least one real query.

## How I fixed it
Added one real query, `Ping` (`SELECT 1::int4 AS ok`) in `db/queries/health.sql`. `internal/db.Open` runs it at startup, so the generated code is used, and it proves pgx plus sqlc work end to end on day 1. Not committed yet.

## What I would tell a team doing this for real
(draft) Give code generators one real input on day 1, and make that input do a job (here, the startup database check) so it is not dead code.

## Evidence
- Error text or screenshot: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
