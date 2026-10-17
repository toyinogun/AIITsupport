# 0006. CI installs the go line's Go, not the toolchain line's

**Date:** 2026-10-17 · **Week:** 0 (before week 1) · **Feature:** #4 Coding standards & tooling
**Time lost:** about 5 minutes (estimate) · **Story worthy:** no

## What I saw
The first CI run on PR #1 failed six jobs (`lint`, `test`, `generated`, `vuln`, `secrets`, and so `ci-ok`). Only the two image builds passed. Each Go job stopped at the toolchain check:

```
##[error]Go is go1.26.0, but go.mod's toolchain line says go1.26.9
```

## What I thought it was
The log said it directly: `actions/setup-go` had installed the wrong Go version.

## What it actually was
`actions/setup-go` v7 with `go-version-file: go.mod` installed the version on the `go` line (1.26.0), not the `toolchain` line (go1.26.9). With `GOTOOLCHAIN=local`, Go never switched to a newer toolchain on its own. The toolchain check from spec 0002 caught it, exactly as designed.

## How I fixed it
The shared `.github/actions/setup-go` step now reads the `toolchain` line from `go.mod` and passes that version to `setup-go` as `go-version`. Commit 86bf5b4 on `feat/tooling`.

## What I would tell a team doing this for real
(draft) Don't trust a CI action to read the version you meant; add a one line check that fails when it picks the wrong one.

## Evidence
- Error text or screenshot: above
- Commit or PR: 86bf5b4, toyinogun/AIITsupport PR #1
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
