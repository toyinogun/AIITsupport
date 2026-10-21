# 0069. CI image builds starved k3sprox's etcd

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~20 min (estimate) · **Story worthy:** post

## What I saw
`make install CLUSTER=k3sprox` stopped at preflight, then every `kubectl` call failed:

```
Error from server (ServiceUnavailable): apiserver not ready
[-]etcd failed: reason withheld
[-]etcd-readiness failed: reason withheld
```

etcd's log on `k3sprox-cp-0` showed `slow fdatasync` taking 8.2s (expected under 1s) and reads taking up to 10s. IO pressure on `cp-0` was about 89% (some) and 38% (full), with memory fine.

## What I thought it was
Our own change. The helpdesk had just added Zammad, Elasticsearch, and three Longhorn volumes, each with a replica on `k3sprox-wkr-pve1-0`. TODO (you): did you suspect anything else first?

## What it actually was
The user's guess: the CI run on `main`. Its image builds run on ARC runner pods, all three on `k3sprox-wkr-pve1-0` (load average 126, IO pressure 96%). `cp-0` runs almost nothing, so it most likely shares a physical disk with that worker on the same Proxmox host (TODO (you): confirm). etcd slow request warnings per 10 minutes were 2 to 113 all evening, including Zammad's first start, then 977 and 1627 in the two buckets after the `main` CI run was queued at 22:23 UTC.

The same starvation failed that CI run's `images (agent, app)` job: BuildKit lost its session (`error: no active session …: context deadline exceeded`, repeated for a minute), while every other job passed. Why the session died, and the workflow fix: [0070](0070-buildkit-session-dies-before-the-push.md).

## How I fixed it
Waited for the CI run to finish and the API to report ready, reran the install (which then sealed Slack and passed), and reran only the failed CI job afterwards. No code change. A real fix is a placement or storage change (keep ARC runners off the worker that shares `cp-0`'s disk, or give etcd its own disk); not done yet.

## What I would tell a team doing this for real
(draft) Never let build runners share a disk with a single etcd member; a busy docker build is enough to take the control plane down.

## Evidence
- Error text or screenshot: above
- Commit or PR: none (no code change)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
