# 0052. govulncheck flagged the Go 1.27.2 standard library partway through a feature build

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #4 Coding standards & tooling (found while building #15 Access requests via group owner)
**Time lost:** ~15 min (estimate) · **Story worthy:** no

## What I saw
`make check` for feature 15 stopped at the `vuln` step:

```
Vulnerability #1: GO-2026-6809
    Distrust compromised intermediate certificates in crypto/x509
  Standard library
    Found in: crypto/x509@go1.27.2
    Fixed in: crypto/x509@go1.27.3
    Example traces found:
      #1: internal/server/server.go:66:24: server.Run calls http.Server.Serve, which eventually calls x509.Certificate.Verify
make: *** [vuln] Error 3
```

## What I thought it was
That the access request work had pulled in something new. Running `make vuln` on the last commit (with the feature changes stashed) failed too, so it predated the feature: a new Go advisory against the pinned toolchain.

## What it actually was
A standard library advisory published after the toolchain was pinned. The code is unchanged; the scanner's database moved, and `go1.27.2` became a known vulnerable version. Same shape as journal [0005](0005-govulncheck-flags-go-toolchain.md).

## How I fixed it
Bumped the toolchain to `go1.27.3`: the `toolchain` line in `go.mod` and `tools.mod` (the `go 1.27.0` line stays, so the tool version rule did not move), `golang:1.27.3-bookworm` as the Dockerfile builder, the README prerequisites, and the stack line in `AGENTS.md`. CI's setup action reads the `toolchain` line, so it follows. `make vuln` then reported no vulnerabilities, and lint, tidy, the full test suite, and an agent image build passed. Spec 0002 still says `go1.27.2` "today" in its text; left for `/architect` or `/sync`. Committed on branch `chore/go-1-27-3`.

## What I would tell a team doing this for real
(draft) A pinned toolchain goes stale on its own; run the vulnerability scan on a schedule, not only when someone pushes.

## Evidence
- Error text or screenshot: above
- Commit or PR: branch `chore/go-1-27-3`
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
