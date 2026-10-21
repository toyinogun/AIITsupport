# 0057. Migration 00006 could not go down, then up, with access requests in the table

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #15 Access requests via group owner
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
The review flagged that `00006`'s Down rebuilds the old `approvals_one_pending_idx` on `(requester_account, tool_name)`, which fails when one account has two pending access requests. The new test for that fix then failed one step later, going up again:

```
up again: partial migration error (type:sql,version:6): ERROR: check constraint
"approvals_group_target_check" of relation "approvals" is violated by some row (SQLSTATE 23514)
```

## What I thought it was
Only the Down step: expire the pending access requests before the index rebuild and the round trip works.

## What it actually was
Two problems. The Down could not rebuild the one per account index with two pending access requests. And the Down drops `approvals.target`, so going up again re-adds the column empty and the new check (`group_access_request` needs a target) fails on every access request kept from before. `TestMigrationGoesDownAndUpCleanly` never saw either, because it ran on an empty table.

## How I fixed it
The Down now expires pending `group_access_request` rows (arguments cleared) before it rebuilds the index. The target check is added `NOT VALID`: Postgres still checks every new or changed row, but not the rows from before. `TestMigrationDownExpiresPendingAccessRequests` covers the round trip with two pending requests, and checks that a new request with no target is still refused. Not committed yet.

## What I would tell a team doing this for real
(draft) Test a migration's down and up with real rows in the table, not just on an empty schema.

## Evidence
- Error text: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
