# 0040. The test clock and the fake store's clock disagreed, so every approval was born expired

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #14 MFA re-registration with approval
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
Every MFA test that approved a request failed at the approve step:

```
approve ... = {... Status:pending ... ExpiresAt:2026-10-18 12:30:00 +0000 UTC ...} Moved:false Reason:expired}
```

and the "pending in another thread" case got a fresh approval instead of `already_pending`.

## What I thought it was
TODO (you): what did you first think it was? (The session went straight to the cause.)

## What it actually was
The tools test harness pins the dispatcher's clock to 2026-10-18 12:00 UTC, so a 30 minute approval expired at 12:30 that day. The in memory audit store compares expiry with the real clock, as the SQL compares with `now()`, and the real date was 2026-10-20. Every approval was already expired when it was created.

## How I fixed it
The MFA test harness uses a real clock truncated to the minute, and the expected expiry strings are built from it. Not committed yet.

## What I would tell a team doing this for real
(draft) When a fake mirrors the database's `now()`, the code under test has to share that clock, or time based guards pass and fail by date.

## Evidence
- Error text or screenshot: above, from `go test -race -run MFA ./internal/tools/`
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
