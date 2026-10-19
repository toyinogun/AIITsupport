# 0031. Ctrl+C can't leave an interrupted turn for the restart check

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #10 Locked out user, end to end
**Time lost:** ~2 min (estimate) · **Story worthy:** no

## What I saw
The spec 0008 verify step for AC-14 says to stop the agent mid turn with Ctrl+C. But Ctrl+C is a graceful shutdown: running turns get `AGENT_SHUTDOWN_GRACE` (25 s) to finish, and a lockout turn takes about 10 s. So the turn would end normally, and the restart sweep would have nothing to pick up.

## What I thought it was
Caught before running it, from reading AC-13: the graceful shutdown works as designed, and the verify step's wording was wrong.

## What it actually was
The verify step described a crash, but named the graceful signal. Only a hard stop leaves an `open` conversation whose last stored message is the user's.

## How I fixed it
A watcher polled Postgres and ran `kill -9` on the agent once the new user message was stored. After a restart, the sweep escalated the conversation, opened a ticket with the restart note, and posted the restart text about 95 s later (the stale window). The verify.md step now records the `kill -9` method. Not committed yet.

## What I would tell a team doing this for real
(draft) To test crash recovery, cause a real crash; a graceful stop tests the graceful path.

## Evidence
- Error text or screenshot: watcher log `killed at 12:47:05 msgs=1`; agent log `agent_interrupted` at 12:49:03, then `agent_escalated` reason `restart`
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
