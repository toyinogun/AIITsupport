# 0068. StatefulSet claim templates kept noord-client OutOfSync

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
After the helpdesk seeded on k3sprox (85 users, 20 tickets), `noord-client` stayed `OutOfSync` while `Healthy`, so the installer's wait for "Synced and Healthy" could never pass:

```
StatefulSet elasticsearch OutOfSync
StatefulSet noord-client-postgres OutOfSync
```

## What I thought it was
Fields Kubernetes defaults on a StatefulSet, the usual ArgoCD noise.

## What it actually was
Kubernetes writes `apiVersion: v1` and `kind: PersistentVolumeClaim` into each `volumeClaimTemplates` entry. Our Elasticsearch template and the Zammad chart's Postgres subchart leave them out, so ArgoCD saw a diff forever. The Redis subchart sets both and was Synced, which gave it away.

## How I fixed it
Set `apiVersion` and `kind` on our Elasticsearch claim template, and added `ignoreDifferences` for those two paths on StatefulSets in the `noord-client` Application, for the subchart's template we cannot edit. Not merged yet.

## What I would tell a team doing this for real
(draft) When one StatefulSet is Synced and its sibling is not, diff their templates before reaching for ignoreDifferences.

## Evidence
- Error text or screenshot: above
- Commit or PR: not merged yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
