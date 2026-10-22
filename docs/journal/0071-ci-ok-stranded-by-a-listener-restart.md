# 0071. Lost runners and a listener restart stranded ci-ok on main

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~3 hours of wall clock, ~20 min of work (estimate) · **Story worthy:** post

## What I saw
The `main` run for the [0070](0070-buildkit-session-dies-before-the-push.md) fix (run 39718927973, commit `d54f4d0`) pushed all three app images, but:

- `test` and `generated` were marked failed at about 04:03 UTC, in the middle of a step, with no logs (the log download returned 404). The `test` runner still ran its cleanup until 04:18.
- `images (agent, app)` spent 27 minutes in its Build step (03:51 to 04:18). The push from cache then took 13s.
- `ci-ok` sat in "Queued" from 04:18 on. Two hours later, `arc-runners` had no pods, and the listener logged `"assigned job"=0 ... currentRunnerCount=0`.
- `gh run cancel` did not stop it: the run still showed "Queued".

## What I thought it was
Runner pods not starting because the workers were full (`Pending` pods), or the listener being down.

## What it actually was
Both ARC pods were `Running`, but the listener was 135 minutes old at 06:34, so it had restarted at about 04:19. `ci-ok` was queued at 04:18:46. The job's assignment message most likely went to the old listener session, and the new one never got it, so no runner was ever asked for. A normal cancel needs a runner to act on it, so it did nothing.

That the lost runners at 04:03 and the listener restart share the cause of 0069 (the node `k3sprox-wkr-pve1-0` starved by CI builds) is likely but not proven. TODO (you): `kubectl get events -n arc-systems` or the listener's previous logs, if they still show why it restarted.

## How I fixed it
`gh api -X POST repos/toyinogun/AIITsupport/actions/runs/39718927973/force-cancel`, then `gh run rerun 39718927973 --failed`. Attempt 2 got runners right away; `test`, `generated` and `ci-ok` passed, and `main` is green. No code change.

The cause, too much CI load on the node that shares etcd's disk, is still open (0069).

## What I would tell a team doing this for real
(draft) A job queued while the ARC listener restarts can wait forever; if `ci-ok` sits in "Queued" with no runner pods, force-cancel and re-run instead of waiting.

## Evidence
- Error text or screenshot: listener log `"assigned job"=0 decision=0 min=0 max=6 currentRunnerCount=0`; `kubectl get pods -n arc-runners` → `No resources found`
- Commit or PR: none (no code change)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
