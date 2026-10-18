# 0025. Zammad's user search found nobody right after a reset

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
The assistant rights live test, run seconds after `make helpdesk-reset`, failed:

```
live_test.go:229: search eva.peters: [], <nil>
```

A minute later the same search by hand returned `['eva.peters']`.

## What I thought it was
That the search query form was wrong (a login with a dot). The session tried `eva.peters`, `eva`, `login:eva.peters`: all worked by then.

## What it actually was
Zammad's search index lags the LDAP import by a few seconds. The spec already warned about it, which is why the seed takes user ids from bootstrap's map and never from search.

## How I fixed it
The live test now retries the search for up to a minute. The seed itself was never affected. Not committed yet.

## What I would tell a team doing this for real
(draft) Anything backed by a search index is eventually consistent; never use it to check a write you just made.

## Evidence
- Error text or screenshot: the line above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
