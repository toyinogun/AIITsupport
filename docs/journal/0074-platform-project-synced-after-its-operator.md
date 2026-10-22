# 0074. The platform sync waited forever: the plugin's Application synced before the project that permits its repo

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~15 min (estimate) · **Story worthy:** post

## What I saw
Right after merging PR #34, ArgoCD on k3sprox showed:

```
noord-k3sprox-barman     Unknown     Unknown
noord-k3sprox-platform   OutOfSync   Healthy
InvalidSpecError: application repo https://cloudnative-pg.github.io/charts is not permitted in project 'noord-platform'
Running waiting for healthy state of argoproj.io/Application/noord-k3sprox-barman
```

## What I thought it was
That the chart never added the plugin's repo to the AppProject. It did: the live AppProject still listed only this repo, because it had not synced yet.

## What it actually was
Sync waves. The new plugin Application sits at wave minus 10, the `noord-platform` AppProject had no wave (so 0). ArgoCD applied the child first, then waited for it to turn healthy before moving to wave 0. A child whose repo its project does not yet permit never turns healthy, so the sync waited forever. The chart tests rendered both objects correctly but checked nothing about their order.

## How I fixed it
The AppProject now carries `argocd.argoproj.io/sync-wave: "-40"`, ahead of every operator Application (the lowest is minus 30), and `TestPlatformProjectSyncsFirst` fails if any operator Application ever comes first. The stuck sync stays pinned to the old commit, so it needs terminating once the fix is on main (deploy/AGENTS.md gotcha). Not committed yet.

## What I would tell a team doing this for real
(draft) In an app of apps, the AppProject is a dependency like any other: give it the earliest wave, or the first new child with a new repo locks the sync.

## Evidence
- Error text or screenshot: the ArgoCD condition and operation message above
- Commit or PR: not committed yet; follows toyinogun/AIITsupport#34
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
