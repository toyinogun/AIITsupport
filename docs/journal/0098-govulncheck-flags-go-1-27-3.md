# 0098. govulncheck flagged the Go 1.27.3 standard library while landing a docs only PR

**Date:** 2026-11-22 · **Week:** after week 4 (past the plan's timeline) · **Feature:** #4 Coding standards & tooling (found while landing the feature 17 task 4 docs, PR #73)
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
`make check` on the rebased `chore/sync-gitops-task4` branch stopped at the `vuln` step:

```
Vulnerability #1: GO-2026-6859
    HTTP/1 server crash due to MaxBytesReader header race in net/http
  Standard library
    Found in: net/http@go1.27.3
    Fixed in: net/http@go1.27.4
    Example traces found:
      #2: internal/readiness/readiness.go:144:24: readiness.Fetch calls http.Client.Do
make: *** [vuln] Error 3
```

## What I thought it was
Not the branch: it changed only Markdown, so it could not have changed what govulncheck sees. `go.mod` on main pinned `toolchain go1.27.3`, so main had the same failure.

## What it actually was
A standard library advisory published after the toolchain was pinned. Same shape as journals [0005](0005-govulncheck-flags-go-toolchain.md) and [0052](0052-govulncheck-flags-go-1-27-2.md): the code did not change, the scanner's database did.

## How I fixed it
Bumped the toolchain to `go1.27.4` in the same five places as journal 0052: the `toolchain` line in `go.mod` and `tools.mod` (the `go 1.27.0` line stays), `golang:1.27.4-bookworm` as the Dockerfile builder, the README prerequisites, and the stack line in `AGENTS.md`. CI's setup action reads the `toolchain` line, so it follows. Branch `chore/go-1-27-4`, not committed yet. PR #73 kept its docs only scope; its test plan names this failure as unrelated.

## What I would tell a team doing this for real
(draft) This is the third time a pinned toolchain went stale and surfaced in an unrelated change; a scheduled vulnerability scan on main would catch it before it blocks someone's PR.

## Evidence
- Error text or screenshot: above
- Commit or PR: branch `chore/go-1-27-4`; found on PR #73
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
