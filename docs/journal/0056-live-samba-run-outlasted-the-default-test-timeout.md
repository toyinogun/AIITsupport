# 0056. The live Samba run outlasted Go's default test timeout

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #15 Access requests via group owner
**Time lost:** ~20 min (estimate) · **Story worthy:** no

## What I saw
While checking the fix for the review minors (the batched `group_search` LDAP reads), the live directory tests ran with:

```
TEST_SAMBA=1 go test -race -count=1 -run 'TestLiveBatchedGroupReads|TestLiveGroupReads' ./internal/ldap/
```

After 11 minutes Go killed it before any test ran:

```
*** Test killed with quit: ran too long (11m0s).
FAIL	github.com/toyinogun/AIITsupport/internal/ldap	660.018s
```

## What I thought it was
At first it was not clear whether a new in chain query hung or the Samba container never came up. The dump that was kept showed only registers, so it did not say which.

## What it actually was
Neither. `TestMain` builds the `noord-samba:test` image through testcontainers, then waits up to 4 minutes for LDAPS, then seeds. On the rerun the image build alone took about 5.5 minutes, so build, start, and seed together can go past `go test`'s default 10 minute limit before the first test starts.

## How I fixed it
Reran with `-v -timeout 25m`. It passed in 407s: `TestLiveGroupReads` and the new `TestLiveBatchedGroupReads` both green. No code change; not committed yet.

## What I would tell a team doing this for real
(draft) Give container backed test runs an explicit `-timeout` that covers the image build, and always run them with `-v` so a stall shows which step it hit.

## Evidence
- Error text: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
