# 0062. `go run` would hide the installer's exit code 2

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
Spec 0015 gives `make install` meaningful exit codes: 2 when preflight found a problem and nothing changed, 3 when a pull request was opened. The first Makefile target used `go run ./cmd/seed install`, the pattern every other seed target uses.

## What I thought it was
TODO (you): what did you first think it was? (In the session this was caught while writing the target, before a run.)

## What it actually was
`go run` prints `exit status 2` for a child that exits non zero and then exits 1 itself, so any script or CI step checking for 2 or 3 would only ever see 1.

## How I fixed it
The `install` target builds `bin/seed` and runs it. A real preflight run on k3sprox then ended with `make: *** [install] Error 2` and exit 2. Not committed yet.

## What I would tell a team doing this for real
(draft) If your exit codes are part of the contract, never wrap the binary in `go run`.

## Evidence
- Error text: `make: *** [install] Error 2` (after the fix)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
