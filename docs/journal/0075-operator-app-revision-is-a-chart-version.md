# 0075. make install hung at platform: an operator Application's revision is its chart version, not a commit

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~20 min (estimate) · **Story worthy:** post

## What I saw
`make install CLUSTER=k3sprox` printed `install k3sprox: platform` and sat there, while ArgoCD showed both platform Applications fine:

```
noord-k3sprox-platform   Synced   Healthy   2581f5e6bc01ad609f79690680b7724a980e4d83
noord-k3sprox-barman     Synced   Healthy   0.8.1
```

## What I thought it was
The installer's wait, and it was. Stopped before the 30 minute timeout; nothing had been sealed or committed, since the platform step runs first.

## What it actually was
The wait required every Application to be at the git commit it pushed or found. The Barman plugin's Application points at a Helm chart repo, so its revision is the chart version (`0.8.1`) and can never match a commit. Task 7's test passed because the fixture answered every Application with the test commit, which no chart sourced Application would ever report.

## How I fixed it
Operator Applications (`operatorApps`) are waited on for Synced and Healthy only; the commit check stays for our own. The fixture now answers the plugin's Application with its chart version, as k3sprox does, and the test runs the platform step alone under a two second deadline, so this failure would show up in two seconds instead of hanging. Not committed yet.

## What I would tell a team doing this for real
(draft) Make your fakes report what the real system reports: a fake that echoes your own assumption back can't catch it.

## Evidence
- Error text or screenshot: the Application revisions above; the test without the fix: `platform: not ready within INSTALL_TIMEOUT (30m0s): Application noord-k3sprox-barman not at 0123456`
- Commit or PR: not committed yet; follows toyinogun/AIITsupport#34 and #35
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
