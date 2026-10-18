# 0010. gitleaks flags a module checksum in tools.sum

**Date:** 2026-10-18 · **Week:** 0 (before week 1) · **Feature:** #6 Noord directory & seeded mess
**Time lost:** ~5 min (estimate) · **Story worthy:** post (suggestion)

## What I saw
During `/check verify noord directory`, `make secrets` passed, but a scan of the working folder with the project's own config still found a leak:

```
RuleID:      generic-api-key
File:        tools.sum
Line:        625
WRN leaks found: 1
```

Line 625 is the `h1:` checksum for `github.com/moby/moby/api v1.56.1`, which arrived with the testcontainers dependency for this feature. A scratch repo holding only the new `tools.sum` and `.gitleaks.toml`, committed and scanned with `gitleaks git` (what `make secrets` runs), also reported `leaks found: 1`.

## What I thought it was
TODO (you): what did you first think it was? (The session went straight to checking whether the hits were in ignored files: the other two were `.dev/certs/*.key`, which git ignores.)

## What it actually was
A false positive. gitleaks' default `generic-api-key` rule sees a high entropy string in a checksum line and calls it a key. The same line sits in `go.sum` and is never flagged, because the default config (v8.30.1) skips files by name only: `go\.(?:mod|sum|work(?:\.sum)?)$`. Our second module file is called `tools.sum`, so it gets no pass. Proof: the same content scanned as `go.sum` gave "no leaks found", and as `tools.sum` gave "leaks found: 1". `make secrets` was only clean because `tools.sum` was not committed yet, so AC-16 would have failed after the commit.

## How I fixed it
Added an allowlist to `.gitleaks.toml` for `(?:^|/)tools\.(?:mod|sum)$`, the same name based pass the default gives `go.mod` and `go.sum`. Checked with a scratch repo holding the whole branch change committed: "no leaks found". A random key planted in any other file, `tools.sum.bak` included, is still caught. Not committed yet.

## What I would tell a team doing this for real
(draft) A scanner's built in allowlist matches file names, so a renamed or second lock file (`tools.sum`) loses the pass; scan the working tree before you commit, not only history.

## Evidence
- Error text or screenshot: the gitleaks output above, from the verify session on 2026-10-18
- Commit or PR: not committed yet (fix in `.gitleaks.toml`)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
