# 0081. The fresh install proof: one command, a Slack unlock, a reset link, a restore, and the bill

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #17 One command GitOps install
**Time lost:** about a day of wall clock for the whole run, ~4 h of it on the incidents in 0078 to 0080 (estimate) · **Story worthy:** both

## What I did
Task 12 of spec 0015, on a new one node k3s VM (8 GB, not the 12 GB the spec names: the two hypervisors had no more free memory, so two other VMs were stopped for the run). k3sprox was paused first, because both clusters share one Slack app.

| Step | Result |
|---|---|
| `make cluster-init`, fill `secrets.env`, `make install` | exit 0 on the fourth try; every Application Synced and Healthy; smoke check passed (AC-1) |
| Slack: a locked out demo user DMs the assistant | unlocked in 6.7 s, four tool calls, each with an intent and an outcome audit row |
| Reset link | issued, opened over the tailnet URL, the new password was set and the single use link was consumed |
| Restore drill (AC-29) | recovery cluster in a scratch namespace read the bucket: 24 `audit_log` rows and the same newest timestamp as the live database |
| `make uninstall` | all three namespaces gone, no volumes left (AC-21) |
| Teardown | VM deleted, k3sprox resumed (agent back, smoke passes), folder removed in a commit |

## The four install attempts
1. Preflight stopped with exit 2 (a public URL setting was missing and the cluster folder was not committed). Nothing changed.
2. The helpdesk seed failed on a network rule (journal 0079). I stopped the installer, fixed the chart, reseeded.
3. After the seed worked, the smoke check failed four times on a healthy cluster (journal 0080).
4. Exit 0.

## What it actually was
Three fixes came out of the proof, all in the fresh path that the homelab cluster never exercised: Elasticsearch closed on both sides of its network policy (and the first fix aimed at the wrong pods), the smoke Job racing a new pod's first second on stock k3s, and the CI `deploy` job reading a live cluster's paused flag. A fourth, `make resume` not committing its `seedRun` bump on a cluster that was never paused, I worked around by committing that line by hand three times (fixed afterwards, journal 0082).

## How I fixed it
PRs 44, 45, 46 and 47, plus the runbook and README in PR 42. The `make resume` fix followed in its own PR (journal 0082).

## What I would tell a team doing this for real
(draft) Run the install on a bare cluster before you call it done, and do it twice: the second time is the one that finds the bugs your first fix introduced. Record the run as you go, because the log is the story.

## Evidence
- Error text or screenshot: the install log (`proofvm-install.log`, kept locally, not committed); the audit rows above
- Commit or PR: PRs 42, 44, 45, 46, 47
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp. The run was not screen recorded; if you want a clip, record a rerun.

## Follow up
- Spec 0015 AC-14's allow list should name Elasticsearch for the seed Jobs, and AC-27 should say 8 GB (`/architect gitops install`).
- The agent logs `approval_tick_failed` with cause `network` in its first second after a start (the same new pod race as the smoke Job). It clears itself.
