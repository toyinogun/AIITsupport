# 0030. Both binaries refused to start: the local tool server token was too short

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #10 Locked out user, end to end
**Time lost:** ~3 min (estimate) · **Story worthy:** no

## What I saw
Starting the tool server and the agent for `/check verify` of spec 0008, both exited at once:

```
toolserver: toolserver config: TOOLSERVER_TOKEN must be at least 32 characters
agent: agent config: TOOLSERVER_TOKEN must be at least 32 characters
```

## What I thought it was
That the 32 character rule had just landed in the spec 0008 commit and the local `.env` had not caught up.

## What it actually was
The rule has been in `internal/config` since spec 0007 (`minToolServerTokenLen = 32`). The local `.env` holds a 25 character value, so the earlier live runs must have overridden it at launch. `.env.example` has a valid placeholder; only the local copy was short.

## How I fixed it
Not changed in the repo. Exported a random 48 character `TOOLSERVER_TOKEN` in the launch shell after loading `.env`, shared by both processes. `.env` was not edited. TODO (you): put a 32+ character token in your local `.env` so `make dev` starts as is.

**Recurred 2026-10-19 (#11 live test):** `make dev` stopped again with `toolserver: toolserver config: TOOLSERVER_TOKEN must be at least 32 characters` (and the same for the agent), because the workaround above never reached `.env` (25 characters there). Fixed for good this time: replaced `TOOLSERVER_TOKEN` in the local `.env` with a random 48 character value (`openssl rand -hex 24`), shared by both binaries. Cost ~2 min. The TODO above is done.

## What I would tell a team doing this for real
(draft) When config refuses a weak secret, make the dev setup produce a valid one too, or the first person to rerun the demo trips on it.

## Evidence
- Error text or screenshot: the two lines above
- Commit or PR: not committed yet (no code change)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
