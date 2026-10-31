# 0079. The first fresh helpdesk seed under default deny failed: Elasticsearch was closed on both sides

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #17 One command GitOps install
**Time lost:** ~2 h 15 min (estimate) · **Story worthy:** both

## What I saw
On the fresh proof cluster the install sat in `wait` with `noord-client` Degraded, and the helpdesk seed Job failed with no retry:

```
seed: seed helpdesk: rails step backdate failed: rails runner: exit status 1
search index: RuntimeError: Unable to send Ticket.find(12).search_index_update_backend backend: ... Elasticsearch is not reachable ...
Connection refused - connect(2)
```

At the same moment `main`'s CI had gone red on four commits in a row, all after the k3sprox pause.

## What I thought it was
A startup race: Elasticsearch's readiness probe had failed at first start and the Zammad Rails pods were crash looping, so I assumed the seed ran too early. I stopped the installer, wiped Zammad's three volumes, waited until Elasticsearch was green, and reseeded. It failed the same way, so it was never a race. Then I found the missing egress port on the seed's policy, fixed it, wiped and reseeded again, and it failed a third time: I had fixed one side of a two sided rule. After opening Elasticsearch's ingress as well, a probe pod was still refused, and a long dive into the node's firewall rules ended with the real third cause below.

## What it actually was
Two separate things:
1. A connection through default deny needs both sides. The `seed-helpdesk` policy opened ports 8080, 5432, 6379 and 11211 to the Zammad pods but not 9200 (egress side), and Elasticsearch's own policy admitted only Zammad's pods (ingress side). The seed runs Rails in process, and its backdate step indexes every ticket in Elasticsearch, so the connection was refused. Postgres, Redis and Memcached worked because Zammad's own policy already admitted the seed; Elasticsearch has a separate policy. The decisive check was a throwaway pod with the seed's label: refused, while a pod with a Zammad label connected. A third cause hid behind the first two: the egress rule I added in PR 44 selected its target as "the Zammad pods" (`app.kubernetes.io/instance: noord-client`), and Elasticsearch's pod does not carry that label. The port was open to the wrong pods. Zammad's own policy gets it right with a separate rule aimed at `name: elasticsearch`. My test checked that port 9200 appeared, not who it was aimed at, so it passed on the wrong rule. The spec's allow list said "Postgres and Redis" and missed it. On the homelab cluster the first seed ran before the policies existed, and every later run takes the "already seeded" path that writes no tickets, so a fresh seed under default deny had never run.
2. The new CI `deploy` job runs the helm based install tests against the live cluster's `values.yaml`. After `make pause` set `paused: true` there, the chart rendered no seed Jobs and two tests failed. My own pause turned `main` red.

## How I fixed it
Gave the seed its own egress rule aimed at the elasticsearch pods by name, took 9200 off the Zammad rule, and opened Elasticsearch's ingress to the seed (PRs 44, 45, and a third). `TestSeedHelpdeskReachesElasticsearch` now checks both sides and the target of the egress rule. The chart tests render with `paused=false` unless a test asks for it. Each failed attempt needed a wipe of Zammad's volumes and a reseed, because a half seeded Zammad cannot be topped up.

## What I would tell a team doing this for real
(draft) Test the fresh path under the real network rules, not just the rerun. With default deny, a connection needs an allow on both ends and each rule must select the right pods, so probe from a pod with the same labels before you change anything, and make your test assert the target, not just the port. Do not edit live objects that GitOps owns to test a fix: the controller puts them back and you test nothing. And keep tests off the live state of a cluster: a command that changes a values file must not be able to break your CI.

## Evidence
- Error text or screenshot: the two lines above; the policy's egress ports before the fix
- Commit or PR: TODO (you): the PR link once merged
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp

## Follow up
- Spec 0015 AC-14's allow list should name Elasticsearch for the seed Jobs (`/architect gitops install: fix the AC-14 wording`, like journal 0072).
- The first guess cost one wipe and reseed of the throwaway cluster; the log line "Connection refused" (not a timeout) was the clue I read too late.
