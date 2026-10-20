# 0039. The Pi runners shared state and ran out of time, so CI moved to the cluster

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #4 Coding standards & tooling
**Time lost:** ~2 hours (estimate) · **Story worthy:** both

## What I saw
CI on main failed after merging spec 0012. Every job passed except `images (agent)`, so `ci-ok` failed too. The build was compiling normally, then:

```
ERROR: failed to build: failed to receive status: rpc error: code = Unavailable desc = closing transport due to: connection error: desc = "error reading from server: EOF", received prior goaway: code: NO_ERROR, debug data: "graceful_stop"
```

`images (toolserver)`, the same Dockerfile with a different build arg, passed.

## What I thought it was
A flaky Pi: out of memory with two cross compiles running at once. But an OOM kill does not say `graceful_stop`; that goaway means BuildKit was asked to shut down.

## What it actually was
Both runners (`rpi01-1`, `rpi01-2`) live on the same Pi and run as the same login user, so they share one `~/.docker/buildx` directory. The agent build lost its BuildKit at 14:49:52, ten seconds into the toolserver job's `docker buildx rm` of its own builder (14:49:42 to 14:49:58). The timing points at the shared buildx state; not proven from the Pi's Docker logs.

## How I fixed it
First try: each buildx job set `BUILDX_CONFIG=$RUNNER_TEMP/buildx` (commit `dd0e2e6`). The image jobs passed on the next run, but `test` and `secrets` were cancelled by their 20 and 15 minute timeouts. Jobs that took 5 minutes in the morning took 15 to 17 minutes, and `actions/cache` failed to restore with `Cannot open: File exists`, because both runners share one `~/go/pkg/mod` too.

So CI moved off the Pi to ephemeral ARC runners in the k3sprox homelab cluster: one fresh pod per job, with its own privileged dockerd sidecar and its own Docker data dir, so nothing is shared between jobs. Auth is a GitHub App (the fine grained token page kept failing with "an unknown error has occurred"). See [docs/runbooks/ci-runner.md](../runbooks/ci-runner.md).

## What I would tell a team doing this for real
(draft) Two runners on one host under one user share more than disk. Ephemeral runners, one clean pod per job, make the whole class of shared state bugs go away.

## Evidence
- Error text or screenshot: above, from main run 39439615005, job `images (agent)`
- Commit or PR: `dd0e2e6`, [PR 17](https://github.com/toyinogun/AIITsupport/pull/17), and the ARC setup PR in the private k3sprox-gitops repo
- Timed out run: 39445749783 (`test` and `secrets` cancelled)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
