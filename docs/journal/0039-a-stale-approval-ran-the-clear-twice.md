# 0039. A stale copy of an approval ran the MFA clear a second time

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #14 MFA re-registration with approval
**Time lost:** ~5 min (estimate) · **Story worthy:** post

## What I saw
The test that calls `Dispatcher.Execute` twice on the same approved approval expected the second call to do nothing. It ran the handler again and reported success:

```
mfa_clear_test.go:302: a second execute = ok, writes []
```

No Graph write happened only because the first run had already deleted every method.

## What I thought it was
That `Execute` already refused anything not `approved`: it checked `a.Status != "approved"` on the approval it was given.

## What it actually was
The check read the status of the in memory copy the caller passed in, which still said `approved` after the first run had marked the row `executed`. The database guard on `MarkApprovalExecuted` only fires after the handler, so it could not stop a second run.

## How I fixed it
`Execute` now reads the approval row again before anything else and returns without running, or marking, when the row is not `approved`. Only the click that wins the approve's guarded UPDATE ever reaches it, so an approval runs once. Not committed yet.

## What I would tell a team doing this for real
(draft) A "run once" rule has to read the state of record right before the action, never a copy the caller hands you.

## Evidence
- Error text or screenshot: above, from `go test -race -run MFA ./internal/tools/`
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
