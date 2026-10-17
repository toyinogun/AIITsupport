# 0007. Editing a migration that already ran locally breaks goose down

**Date:** 2026-10-17 · **Week:** 0 (before week 1) · **Feature:** #5 Data model
**Time lost:** ~5 min (estimate) · **Story worthy:** no (suggestion)

## What I saw
The fix for the review's major finding edited migration `00002_data_model.sql` in place (the branch was not merged yet). It added the trigger function `approvals_arguments_only_cleared` and a matching `DROP FUNCTION` in the Down section. Then rebuilding the local dev database failed:

```
goose run: ERROR 00002_data_model.sql: failed to run SQL migration: failed to execute SQL query
"DROP FUNCTION approvals_arguments_only_cleared();": ERROR: function approvals_arguments_only_cleared() does not exist (SQLSTATE 42883)
```

The Down ran in a transaction, so it rolled back and the database stayed on the old version 2.

## What I thought it was
TODO (you): what did you first think it was? (The test suite was green, since CI and the DB test helper start from fresh databases; it only showed up on the long lived Compose volume.)

## What it actually was
The dev volume had run the earlier version of 00002. goose runs the Down section of the file as it is now, and that section drops a function the old Up never created. goose tracks migrations by version number, not by content, so it can't tell the file changed.

## How I fixed it
Locally only: I created a stub `approvals_arguments_only_cleared()` function as `noord_owner`, then ran `goose down-to 1` and `goose up`. The dev database now has the new schema. The DB tests are unaffected, since `dbtest` names its template by a hash of the migrations and rebuilds it when they change. Not committed yet.

## What I would tell a team doing this for real
(draft) Editing a migration is only safe before it merges, and even then every local database that ran it needs a rebuild. After merge, add a new migration instead.

## Evidence
- Error text or screenshot: the SQLSTATE 42883 error above
- Commit or PR: not committed yet (fix for the major finding in `docs/reviews/2026-10-17-feat-data-model.md`, PR toyinogun/AIITsupport#3)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
