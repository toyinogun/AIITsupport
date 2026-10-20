# 0046. A 1s audit write timeout in the tests timed out on the Pi runner

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #4 Coding standards & tooling
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
The second CI run on the Raspberry Pi runner got past the missing compiler (0045), and every package passed but one:

```
--- FAIL: TestPgThreadHasOK (9.76s)
    audit_pg_test.go:493: audit log unavailable, action not run: insert intent: timeout: context deadline exceeded
FAIL	github.com/toyinogun/AIITsupport/internal/audit	39.478s
```

## What I thought it was
A deadline too tight for a slow, busy runner rather than a real bug: the same test passes locally and on `ubuntu-latest`.

## What it actually was
The audit tests' `testConfig` gave each insert a 1s `WriteTimeout` (production defaults to 5s). On the Pi, Postgres writes to an SD card, and in the same `go test` the live Samba packages (`cmd/seed`, `internal/ldap`, `internal/mcpserver`) ran for 7 to 9 minutes each beside it, so one insert took over a second. Not confirmed beyond the one failure and the timings.

## How I fixed it
`testConfig.WriteTimeout` is now 5s, matching the `AUDIT_WRITE_TIMEOUT` default. No test waits on that deadline, so passing runs are no slower. Commit `dadb425`.

## What I would tell a team doing this for real
(draft) A test timeout tuned on a laptop is a flake waiting for slower hardware; start tests from the production default and only go tighter when the test needs it.

## Evidence
- Error text or screenshot: the go test output above, run 39428797094, job `test`
- Commit or PR: `dadb425`, PR #16
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
