# 0028. The helpdesk seed raced Zammad's init, so no user was ever searchable

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #9 Tool server & risk tiers
**Time lost:** ~15 min (estimate), plus a 5 minute test timeout · **Story worthy:** post

## What I saw
The tool layer's live Zammad test, run right after `make helpdesk-reset`, waited 5 minutes for `eva.peters` to show up in Zammad's user search and gave up:

```
live_zammad_test.go:48: eva.peters never appeared in Zammad's user search
```

By hand, `eva.peters`, `eva`, `Peters`, `login:"eva.peters"`, and the email all returned `[]`, while `*` returned users. Elasticsearch had indexes for tickets, KB answers, and organizations, but none for users, and the scheduler logged every minute:

```
no such index [zammad_production_user]
```

The init container's last line: `Elasticsearch index exists, no automatic reindexing is needed.`

## What I thought it was
The search lag from journal 0025, or a `login:"..."` query form Zammad does not support (spec 0007 named a fallback for that).

## What it actually was
A race in `make helpdesk-reset`. `docker compose up --wait` returns once the services are healthy, but `zammad-init` is a one shot container that configures Elasticsearch last (here at 21:36:14). The seed started at 21:34:44, so the LDAP users it imported were never indexed. Tickets filled after that point were, which created an index, so init then decided no reindex was needed. Journal 0025's "lag" was very likely the same race, won some runs and lost others.

## How I fixed it
`helpdesk-up` now runs `docker compose --profile helpdesk wait zammad-init` after `up --wait`, so the seed starts only once init has exited. After the fix the user index held 87 documents, and the search found `eva.peters` 0 s after the reset. Not committed yet.

## What I would tell a team doing this for real
(draft) "Healthy" is not "initialised": wait for the one shot init job to exit before anything writes data.

## Evidence
- Error text or screenshot: the lines above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
