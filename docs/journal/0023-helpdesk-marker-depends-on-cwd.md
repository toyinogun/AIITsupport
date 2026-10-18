# 0023. The helpdesk marker changed with the working directory

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~5 min (estimate) · **Story worthy:** post

## What I saw
Right after a clean `make helpdesk-reset`, the first live test said Zammad held a different seed:

```
live_test.go:128: marker = "seed:sha256:1849442d...", want "seed:sha256:c668a106..." (run make helpdesk-reset first)
```

## What I thought it was
TODO (you): what did you first think it was? (The session went straight to the paths.)

## What it actually was
The marker hashes each input file's path together with its bytes. `make` runs from the repo root (`seed/helpdesk/kb.yaml`), the test runs from `internal/seed/helpdesk` (`../../../seed/helpdesk/kb.yaml`), so the same files gave two hashes. The spec said "relative to the repo root"; the code hashed paths as given.

## How I fixed it
`helpdesk.Marker` now takes a root and hashes every path relative to it, refusing a file outside the root. A unit test checks the marker is the same from another working directory. Not committed yet.

## What I would tell a team doing this for real
(draft) If a fingerprint includes file names, normalise them first, or the same content looks different from every directory.

## Evidence
- Error text or screenshot: the line above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
