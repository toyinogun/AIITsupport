# 0082. `make resume` said "resumed" and committed nothing, so the seed steps never ran again

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #17 One command GitOps install
**Time lost:** ~25 min (estimate) · **Story worthy:** post

## What I saw
After wiping the helpdesk's volumes on the proof cluster, I ran `make resume` to rerun the seed steps. It printed `resumed; every seed step runs once more`, but ArgoCD stayed on the old commit, no new seed Job appeared, and a watcher waited for it until it timed out. `git status` showed `deploy/clusters/<name>/values.yaml` modified and nothing committed.

## What I thought it was
ArgoCD being slow to notice the new commit, so I forced hard refreshes. The commit it was waiting for did not exist.

## What it actually was
`Resume` wrote `seedRun` in the file, and then called the shared `change` helper for `paused: false`. That helper commits only when its own value changed. On a cluster that was never paused, `paused` was already false, so nothing was committed or pushed and the `seedRun` bump stayed in the working tree. It had worked the first time only because that first commit also added the `paused` line. The existing test always paused before resuming, so it never exercised the case.

## How I fixed it
`Resume` now writes both values and always commits and pushes once (a `commitFolder` helper shared with `change`). New test `TestResumeOnARunningClusterCommitsTheBump` failed before the fix. Until then I committed the `seedRun` line by hand, three times.

## What I would tell a team doing this for real
(draft) A command that says "done" must only say so after the change reached the place that acts on it. Test the command from the state you actually use it in, not only from the state its sibling command leaves behind.

## Evidence
- Error text or screenshot: `resumed; every seed step runs once more` with an uncommitted `seedRun` line in `git status`
- Commit or PR: TODO (you): the PR link once merged
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
