# 0005. govulncheck fails the first run on the Go toolchain itself

**Date:** 2026-10-17 · **Week:** 0 (before week 1) · **Feature:** #4 Coding standards & tooling
**Time lost:** about 5 minutes (estimate) · **Story worthy:** post

## What I saw
The new `make check` stopped at `make vuln`:

```
Your code is affected by 18 vulnerabilities from the Go standard library.
...
    Found in: net/http@go1.26.5
    Fixed in: net/http@go1.26.9
make: *** [vuln] Error 3
```

The fixes spread across `crypto/tls`, `encoding/asn1`, `encoding/xml`, `html/template`, `mime/multipart`, `net/http`, and `net/url`, the newest needing go1.26.9.

## What I thought it was
Nothing in the app's own dependencies: every finding was in the standard library, so the cause was the pinned toolchain (go1.26.5), not the code.

## What it actually was
The scaffold pinned `toolchain go1.26.5` in spec 0001, and four patch releases had shipped since. Spec 0002 named that same toolchain and also required `vuln` to pass, so the two rules conflicted the moment the scanner ran.

## How I fixed it
You chose to bump the toolchain: `toolchain go1.26.9` in `go.mod` and `tools.mod`, and `golang:1.26.9-bookworm` as the Dockerfile builder. The `go 1.26.0` line stayed, so the tool version rule did not move. `make vuln` then reported no vulnerabilities and the agent image still built. `AGENTS.md` still says 1.26.5 until `/sync`. Not committed yet.

## What I would tell a team doing this for real
(draft) A pinned toolchain is a security dependency too; put the vuln scan in CI from day one so a stale pin fails loudly instead of quietly.

## Evidence
- Error text or screenshot: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
