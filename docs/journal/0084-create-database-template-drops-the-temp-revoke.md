# 0084. `CREATE DATABASE ... TEMPLATE` does not copy database privileges, so the temp table revoke was missing in tests

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #18 Observability & ops dashboard
**Time lost:** ~15 min (estimate) · **Story worthy:** no

## What I saw
The new test for the view only role `noord_grafana` failed:
`CREATE TEMP TABLE t (a int): want permission denied, got <nil>`
Migration 00008 had run `REVOKE TEMPORARY ON DATABASE ... FROM PUBLIC`, so the role should not have been able to create a temporary table.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
The test helper migrates a template database once and makes each scratch database with `CREATE DATABASE ... TEMPLATE`. Postgres copies the template's contents but not its database level privileges, so the revoke only ever applied to the template. Real databases (Compose and the cluster) run the migration directly and are not affected.

## How I fixed it
`dbtest.New` now repeats the `REVOKE TEMPORARY ... FROM PUBLIC` on each scratch database. A separate migration test runs the migration on an empty database and checks `has_database_privilege('noord_grafana', ..., 'TEMP')` is false, so the migration itself is still proven. Not committed yet.

## What I would tell a team doing this for real
(draft) If your tests clone a template database, anything that lives outside the database's contents (privileges, role settings) has to be set again on the clone, or the test proves less than you think.

## Evidence
- Error text or screenshot: `CREATE TEMP TABLE t (a int): want permission denied, got <nil>`
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
