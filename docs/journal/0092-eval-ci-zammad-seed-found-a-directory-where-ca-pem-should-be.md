# 0092. The CI eval's Zammad seed found a directory where `ca.pem` should be

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #20 Eval harness in CI
**Time lost:** ~20 min (estimate) · **Story worthy:** both

## What I saw
With Compose installed, the manual run (41887295263) got Zammad up, then the helpdesk seed failed after about 3 minutes:

```
eval: start the environment: seed the helpdesk: rails step bootstrap failed: docker compose exec: exit status 1
ca: Errno::EISDIR: Is a directory @ io_fread - /opt/noord-seed/ca.pem
```

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
The harness writes `ca.pem` and the Rails scripts into a temp dir made with `os.MkdirTemp("")` (`/tmp`) and bind mounts them into the Zammad containers. On the k3sprox runner the Docker daemon is the dind sidecar, which has its own filesystem and shares only `/home/runner/_work` with the runner container. The source path did not exist there, so Docker created an empty directory at it, and the seed read a directory instead of a file. It worked on the laptop because the daemon and the harness share one filesystem.

## How I fixed it
The eval step in the workflow sets `TMPDIR` to `$RUNNER_TEMP` (under `/home/runner/_work`), so the harness's temp dir is visible to both containers. Found by reading the runner's volume mounts in the k3sprox-gitops scale set config. Commit: see the fix PR.

## What I would tell a team doing this for real
(draft) With a Docker daemon in a sidecar, a bind mount only works for paths both containers can see, and a path that is missing on the daemon side turns into an empty directory instead of an error.

## Evidence
- Error text or screenshot: see "What I saw"
- Commit or PR: the fix PR for run 41887295263
- Eval run (if any): run 41887295263 failed in the seed step, before the first ticket
- Clip file and timestamp: TODO (you): clip file and timestamp
