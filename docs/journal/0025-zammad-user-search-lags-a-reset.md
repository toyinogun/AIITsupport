# 0025. Zammad's user search found nobody right after a reset

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
The assistant rights live test, run seconds after `make helpdesk-reset`, failed:

```
live_test.go:229: search eva.peters: [], <nil>
```

A minute later the same search by hand returned `['eva.peters']`.

It came back later the same day (while building AC-17): the test, already retrying for a minute, ran about 80 seconds after a fresh `make helpdesk-reset` and still failed with `search eva.peters: [], <nil>`. A rerun about 2 minutes later passed in 2 s.

## What I thought it was
That the search query form was wrong (a login with a dot). The session tried `eva.peters`, `eva`, `login:eva.peters`: all worked by then.

## What it actually was
Zammad's search index (Elasticsearch, fed by background jobs) lags the LDAP import. Usually by seconds, but on a fresh reset, with Elasticsearch just started, by more than a minute. The spec already warned about it, which is why the seed takes user ids from bootstrap's map and never from search.

## How I fixed it
The live test retried the search for up to a minute (commit 9348f6d). That proved too short, so it now waits up to 3 minutes (90 tries, 2 s apart) and stops as soon as the user shows up. The seed itself was never affected. The 3 minute wait is not committed yet.

## What I would tell a team doing this for real
(draft) Anything backed by a search index is eventually consistent; never use it to check a write you just made.

## Evidence
- Error text or screenshot: the line above
- Commit or PR: 9348f6d (the 1 minute retry); the 3 minute wait not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
