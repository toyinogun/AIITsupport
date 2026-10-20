# 0039. One Pi runner's builder cleanup killed the other runner's image build

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #4 Coding standards & tooling
**Time lost:** ~15 min (estimate) · **Story worthy:** post

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
Each buildx job (`images` and `samba-image`) now sets `BUILDX_CONFIG=$RUNNER_TEMP/buildx` before `setup-buildx-action`, so every job keeps its builder state in its own temp folder. Commit `dd0e2e6` on `fix/ci-buildx-config`. The failed jobs were also re-run.

## What I would tell a team doing this for real
(draft) Two runners on one host under one user share more than disk: give each job its own tool config dirs, or run each runner as its own user.

## Evidence
- Error text or screenshot: above, from main run 39439615005, job `images (agent)`
- Commit or PR: `dd0e2e6`, [PR 17](https://github.com/toyinogun/AIITsupport/pull/17)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
