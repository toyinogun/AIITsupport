# 0063. CNPG managed roles keep the Cluster OutOfSync in ArgoCD

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~15 min (estimate) · **Story worthy:** post

## What I saw
The first live `make install CLUSTER=k3sprox` brought everything up: `noord-pg` healthy, the migration Job applied all 6 migrations, the tool server and agent running. But the install's wait step never finished, because ArgoCD kept the apps OutOfSync:

```
noord-k3sprox            OutOfSync     Healthy
noord-agent              OutOfSync     Healthy
  Cluster noord-pg OutOfSync
```

## What I thought it was
CNPG's webhook filling in defaults (the `initdb` encoding and locales, `storage.resizeInUseVolumes`) that ArgoCD read as drift.

## What it actually was
Close, but only one default mattered. Every other CNPG cluster on k3sprox gets the same `initdb` defaults and stays Synced; ours is the only one with `managed.roles`. The webhook adds `connectionLimit: -1` to each role, and roles are list items, which ArgoCD compares whole, so the list never matches.

## How I fixed it
State `connectionLimit: -1` on each managed role in `deploy/helm/noord/templates/agent/postgres.yaml`, so what git says equals what the API server stores. Commit: see the PR that merged `fix/cnpg-role-drift`.

## What I would tell a team doing this for real
(draft) When a CRD's webhook defaults fields inside a list, write those defaults into your manifest, or GitOps will call it drift forever.

## Evidence
- Error text: above
- Commit or PR: TODO (you): the merged PR for fix/cnpg-role-drift
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
