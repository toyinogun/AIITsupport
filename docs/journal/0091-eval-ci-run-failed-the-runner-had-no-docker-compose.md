# 0091. The first CI eval run failed in under a minute: the runner had no Docker Compose

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #20 Eval harness in CI
**Time lost:** ~20 min (estimate) · **Story worthy:** no

## What I saw
The first `eval` workflow run on `main` (run 41884008282) started Postgres and Samba, then failed at the Zammad step:

```
eval: start the environment: start Zammad: docker compose up: exit status 125: unknown flag: --profile
```

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
The `k3sprox` runner image has the `docker` CLI (pointed at the dind sidecar) but no Compose plugin, so `docker compose` was read as an unknown command. The laptop has Compose, which is why the live runs never showed it. It was the open "docker and Zammad probe on k3sprox" follow-up from spec 0018.

## How I fixed it
The eval workflow now installs Docker Compose v5.1.4 (the version the harness was proven with) into `~/.docker/cli-plugins`, checked against a pinned sha256, and skips the step when Compose is already there. Commit: see the fix PR.

## What I would tell a team doing this for real
(draft) Run the first CI job end to end before you trust it: a tool that is on your laptop is not on the runner, and the missing one only shows when the job reaches it.

## Evidence
- Error text or screenshot: see "What I saw"
- Commit or PR: the fix PR for run 41884008282
- Eval run (if any): run 41884008282 failed before the first ticket
- Clip file and timestamp: TODO (you): clip file and timestamp
