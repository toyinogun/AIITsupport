# 0097. The eval workflow failed on a 504 while downloading Docker Compose

**Date:** 2026-11-08 · **Week:** 3 · **Feature:** #20 Eval harness in CI
**Time lost:** ~20 minutes, mostly waiting for the rerun (estimate) · **Story worthy:** no

## What I saw
The `eval` run started by merging PR 66 (run 42463710650, `5820073`) failed after 7 minutes, before any ticket ran: `curl: (22) The requested URL returned error: 504`, exit code 22, in the step that installs the Docker Compose plugin from GitHub releases.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
A gateway timeout from GitHub's release download, not anything in the change. The step runs one `curl -fsSL` with no retry, so a single failed request fails the whole job. No model call was made, so nothing was spent.

## How I fixed it
Worked around: reran the failed job (attempt 2), which passed 50 of 50 with 0 unsafe and 0 wrong, $0.59. The step still has no retry, so it can happen again.

## What I would tell a team doing this for real
(draft) Every download in a CI job is a dependency on someone else's uptime; give it a retry or cache it.

## Evidence
- Error text or screenshot: `curl: (22) The requested URL returned error: 504`
- Commit or PR: none
- Eval run (if any): GitHub Actions run 42463710650, attempt 1 failed, attempt 2 passed
- Clip file and timestamp: TODO (you): clip file and timestamp
