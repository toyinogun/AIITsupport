# 0079. The first fresh helpdesk seed under default deny failed: its policy never opened Elasticsearch

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #17 One command GitOps install
**Time lost:** ~45 min (estimate) · **Story worthy:** both

## What I saw
On the fresh proof cluster the install sat in `wait` with `noord-client` Degraded, and the helpdesk seed Job failed with no retry:

```
seed: seed helpdesk: rails step backdate failed: rails runner: exit status 1
search index: RuntimeError: Unable to send Ticket.find(12).search_index_update_backend backend: ... Elasticsearch is not reachable ...
Connection refused - connect(2)
```

At the same moment `main`'s CI had gone red on four commits in a row, all after the k3sprox pause.

## What I thought it was
A startup race: Elasticsearch's readiness probe had failed at first start and the Zammad Rails pods were crash looping, so I assumed the seed ran too early. I stopped the installer, wiped Zammad's three volumes, waited until Elasticsearch was green, and reseeded. It failed the same way, so it was never a race.

## What it actually was
Two separate things:
1. The `seed-helpdesk` NetworkPolicy opened ports 8080, 5432, 6379 and 11211 to the Zammad pods but not 9200. The seed runs Rails in process, and its backdate step indexes every ticket in Elasticsearch, so the connection was refused. The spec's allow list said "Postgres and Redis" and missed it. On the homelab cluster the first seed ran before the policies existed, and every later run takes the "already seeded" path that writes no tickets, so a fresh seed under default deny had never run.
2. The new CI `deploy` job runs the helm based install tests against the live cluster's `values.yaml`. After `make pause` set `paused: true` there, the chart rendered no seed Jobs and two tests failed. My own pause turned `main` red.

## How I fixed it
Opened port 9200 on the seed policy, and added a test that fails without it (`TestSeedHelpdeskReachesElasticsearch`). The chart tests now render with `paused=false` unless a test asks for it. Not committed yet at the time of writing; see the PR for the commit.

## What I would tell a team doing this for real
(draft) Test the fresh path under the real network rules, not just the rerun. And keep tests off the live state of a cluster: a command that changes a values file must not be able to break your CI.

## Evidence
- Error text or screenshot: the two lines above; the policy's egress ports before the fix
- Commit or PR: TODO (you): the PR link once merged
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp

## Follow up
- Spec 0015 AC-14's allow list should name Elasticsearch for the seed Jobs (`/architect gitops install: fix the AC-14 wording`, like journal 0072).
- The first guess cost one wipe and reseed of the throwaway cluster; the log line "Connection refused" (not a timeout) was the clue I read too late.
