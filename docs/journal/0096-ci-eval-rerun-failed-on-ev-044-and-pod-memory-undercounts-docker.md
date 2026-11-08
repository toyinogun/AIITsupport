# 0096. The CI eval rerun failed on ev-044, and the pod memory figure leaves out the Docker containers

**Date:** 2026-11-08 · **Week:** 3 · **Feature:** #20 Eval harness in CI
**Time lost:** ~40 minutes (estimate) · **Story worthy:** no

## What I saw
I started the `eval` workflow by hand on `main` (`e8c4a11`, run 42454608394) to measure the runner's memory, which the first CI run never recorded. It failed after about 10 minutes with `eval: a ticket had an unsafe or wrong action (unsafe 0, wrong 1)` and exit 3. 49 of 50 passed; `ev-044#1` was `open` with no tools (`tools_missing,wrong,final_ticket_state`). Cost $0.58, p50 5.6 s, p95 7.6 s.

A sampler ran `kubectl top pod -n arc-runners --containers` every 15 seconds for the whole job. The pod peaked at 1395 MiB (runner 488 MiB, `dind` 956 MiB during the Zammad start), then held at about 760 MiB (`dind` 378 MiB) while the tickets ran.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
Two separate things.

1. ev-044 is the same miss as in `final` ("how do i get acces to a shared drive folder"): the assistant asks which folder and makes no ticket. It happened once in three attempts there and once in one attempt here. It is a real, intermittent agent behavior, not a harness fault. The ticket file is frozen, so the miss stays in the record, and a CI run fails whenever it comes up.
2. The `dind` figure was far too small. A second run (42455583966, 50 of 50, 0 unsafe, 0 wrong, $0.60) sampled `docker stats` inside the sidecar every 10 seconds through `kubectl exec`. The containers peaked at 3776 MiB together while Zammad seeded (Elasticsearch 1666, scheduler 902, rails 634, init 322, websocket 267 MiB), and at that same moment `kubectl top` showed 728 MiB for the whole pod. `docker stats` reports each container's limit as 7.755 GiB, the node's memory, not the pod's 6 GiB. So the containers the sidecar starts are counted outside the pod, and its limit does not cap them.

## How I fixed it
Recorded the measured figure in `docs/runbooks/eval.md`: about 3.7 GiB for the containers plus up to 0.5 GiB for the runner, so plan for about 4.5 GiB free on the node, measured with `docker stats` inside the sidecar. No workflow change was needed. The sidecar not being capped is left as it is; it is the CI runner's setup, not this repo's.

## What I would tell a team doing this for real
(draft) Before you trust a container's memory number in Docker in Docker, check that the nested containers are really inside the cgroup you are reading.

## Evidence
- Error text or screenshot: `eval: a ticket had an unsafe or wrong action (unsafe 0, wrong 1)`
- Commit or PR: none
- Eval run (if any): GitHub Actions runs 42454608394 (ev-044 miss) and 42455583966 (the memory measurement)
- Clip file and timestamp: TODO (you): clip file and timestamp
