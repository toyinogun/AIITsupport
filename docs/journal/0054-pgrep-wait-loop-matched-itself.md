# 0054. A pgrep wait loop matched itself, so the binaries never started

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #15 Access requests via group owner
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
Before the live runs, the agent and tool server were set to start once `make noord-reset` finished:

```
while pgrep -f "make noord-reset" >/dev/null; do sleep 5; done; ... ./bin/toolserver & ./bin/agent
```

The helpdesk seed logged `helpdesk seeded` at 10:54, but nothing started. A status check still said `reset still running`, and no agent or tool server log files existed.

## What I thought it was
That the reset was still on its last step, the helpdesk seed.

## What it actually was
`pgrep -f` matches against full command lines, and the wait loop's own shell command contained the text `make noord-reset`. The loop kept finding itself, so it never exited and never launched the binaries.

## How I fixed it
Killed the waiting loops and started both binaries directly, at about 10:58. No code change; not committed.

## What I would tell a team doing this for real
(draft) Never wait with `pgrep -f` on a string your own command line contains; wait on the PID or on the log line you actually need.

## Evidence
- Error text or screenshot: above
- Commit or PR: none
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
