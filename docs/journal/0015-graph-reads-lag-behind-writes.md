# 0015. Graph's reads lag its writes, so drift takes two syncs to repair

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~30 min (estimate) · **Story worthy:** both

## What I saw
The live drift test (`TEST_ENTRA=1`) changed a department and soft deleted a user, then synced. Three runs, three different failures:

1. The listing still showed the deleted user, and reading its phones gave `404 resourceNotFound: The specified user could not be found.`
2. After a fix, the sync saw no drift at all (`writes: 0`): Graph still returned the old department and still listed the deleted user with readable phones.
3. After waiting until a `$filter` lookup showed the drift, the restore worked, but the full `/users` listing still returned the old department, so it was not updated.

In every run, the next sync repaired everything.

## What I thought it was
First, a bug in the sync's read of a deleted user. Then, that waiting until Graph showed the change would be enough.

## What it actually was
Graph is eventually consistent. For tens of seconds after a write, some reads return the old state, and different read paths (`$filter` lookups, the full listing, phone methods) catch up at different times. A sync can only fix drift it can see.

## How I fixed it
- The sync treats a listed user whose phone read returns 404 as gone for that run (the restore path takes over), and ignores one object listed as both live and deleted, so a lagging read can never stop a run with a false duplicate error.
- The live drift and MFA tests now sync until a run makes no writes (at most 4 runs, 15 s apart), then check the totals: 1 restore, 0 creates, the department back, the same object id. The last run passed: run 1 saw nothing, run 2 repaired everything, run 3 made 0 writes.
- AC-6 says "the next run puts it back"; on the real service, that means the next run that can see the drift. TODO (you): decide whether to clarify that in the spec.

Not committed yet.

## What I would tell a team doing this for real
(draft) A reconciler on an eventually consistent API converges over runs, not in one; test the convergence, not a single pass.

## Evidence
- Error text or screenshot: the 404 above and the three live test runs (Oct 18)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
