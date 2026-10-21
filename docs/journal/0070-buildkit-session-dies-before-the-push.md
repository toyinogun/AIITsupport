# 0070. BuildKit dropped the session before the agent image's push

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~45 min (estimate) · **Story worthy:** post

## What I saw
`images (agent, app)` failed on `main` (run 39686189025, commit `f96c67e`), on the first try and again on a rerun of only that job. Both times the compile finished, then step #27, `exporting to image`, failed at `pushing layers`:

```
#27 pushing layers
#27 5.193 error: no active session for a9mn2sn6e6ex3b4t4io1jbazh: context deadline exceeded
#27 5.193 retrying in 1s
...
ERROR: failed to build: failed to solve: DeadlineExceeded: failed to push ghcr.io/toyinogun/aiitsupport-agent:f96c67e…: no active session for …: context deadline exceeded
```

`images (toolserver, app)` and `images (seed, seed)`, the same Dockerfile, passed both times. On `e0658f4` an hour earlier the agent's whole build and push step took 3 minutes; here the `go build` alone took 640s (first try) and 332s (rerun).

## What I thought it was
Node load, as in [0069](0069-ci-image-builds-starved-etcd.md): all runner pods sit on `k3sprox-wkr-pve1-0`. But failing twice at the same step of the same job looked like something more specific.

## What it actually was
BuildKit health checks the client's session (buildx in the runner container) every 5s with a 15s timeout, and closes the session after two failures in a row, so about 30s of no answer ends it (`session/grpc.go` in BuildKit v0.33.1). The build itself keeps going without the session, but the push fetches the GHCR credentials through it, and the push is the first thing after the compile to use it. So any build that runs long on a starved node fails exactly at step #27.

The agent job was the one hit both times because of scheduling, not its code: it is first in the matrix, so it got a runner in the first wave next to `test`, `lint` and `generated` (whose `setup-go` took 11 to 12 minutes that run), while `toolserver` and `seed` waited for a free runner until 22:38 and built on a quieter node in 2 minutes. The rerun at 22:46 overlapped the next `main` run's Go jobs.

Reproduced locally with BuildKit v0.33.1, a password protected registry, and a 60s `RUN`: freezing the buildx client for 45s mid build (`kill -STOP`) gives the same `no active session …: context deadline exceeded` at `pushing layers`, and buildkitd logs `healthcheck failed fatally`.

## How I fixed it
The `images` job now builds without an output first, then, on a push to `main` only, runs `docker/build-push-action` again with `push: true`. The second run is all cache hits in the same builder, so it lasts seconds with a fresh session. In the local repro the session died during the first step again (`healthcheck failed fatally`), the build still exited 0, and the push step pushed from cache. Commit: not committed yet.

The load itself is not fixed here: the runners still share a node (and most likely a disk) with etcd, see 0069.

## What I would tell a team doing this for real
(draft) A docker build's push depends on a client session BuildKit will drop after 30 quiet seconds; on shared CI hardware, split the long build from the push.

## Evidence
- Error text or screenshot: above, jobs 119520725443 and 119529729212 of run 39686189025
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
