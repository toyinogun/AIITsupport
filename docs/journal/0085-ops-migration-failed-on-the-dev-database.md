# 0085. Migration 00008 failed on the Compose database, and the new role ran the tests out of connections

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #18 Observability & ops dashboard
**Time lost:** ~25 min (estimate) · **Story worthy:** no

## What I saw
`make migrate` on the Compose Postgres:
`ERROR 00008_ops_data.sql: ... "CREATE SCHEMA ops;": ERROR: permission denied for database noord (SQLSTATE 42501)`
Later, a full `go test ./...` failed in an unrelated package:
`connect as noord_grafana: ... FATAL: too many connections for role "noord_grafana" (SQLSTATE 53300)`

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
(1) All the migration tests passed because their scratch databases are owned by `noord_owner`. The real Compose database `noord` is owned by the superuser, so `noord_owner` could not create a schema, and its `REVOKE TEMPORARY` would silently have done nothing. In the cluster CloudNativePG makes `noord_owner` the database owner, so it only broke locally. (2) `dbtest.New` opened a pool for every role, including the new `noord_grafana`, which is capped at 5 connections for the whole server. Parallel test binaries used them all up.

## How I fixed it
`db/roles.sql` now makes `noord_owner` the owner of the database it runs in (`make migrate` runs it first). `dbtest` gives `noord_grafana` a lazy pool of one connection that only connects when a test uses it. Three full test runs in a row passed. Not committed yet.

## What I would tell a team doing this for real
(draft) Run each new migration against the real dev database, not only against the scratch copies your tests build, and treat a per role connection cap as a shared resource when tests run in parallel.

## Evidence
- Error text or screenshot: the two errors above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
