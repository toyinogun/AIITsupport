# 0094. The repeat 3 eval run failed before ticket 1: the Zammad seed timed out on an overloaded Docker VM

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #20 Eval harness in CI
**Time lost:** ~12 min (estimate) · **Story worthy:** no

## What I saw
`make eval ARGS="--label verify-r3 --repeat 3"` stopped in setup with no ticket played:
`eval: start the environment: seed the helpdesk: rails step backdate failed: stopped after SEED_ZAMMAD_WAIT_TIMEOUT; run make helpdesk-reset`

## What I thought it was
A full Docker disk, as in journal 0089.

## What it actually was
Not confirmed. The disk was fine (60% used, 21.9 GB free). The Docker VM (8 GB) had a load average of about 100 to 120 with 25 containers running: the development Zammad stack (with its own Elasticsearch), a `k3d` cluster using about 1.5 GB, and other projects' containers, next to the eval's own ten container Zammad stack. The seed was probably just slower than the 10 minute `SEED_ZAMMAD_WAIT_TIMEOUT`. Earlier eval runs the same day passed the same step.

## How I fixed it
Not fixed yet. Options: stop the development Zammad stack before an eval run, retry, or raise `SEED_ZAMMAD_WAIT_TIMEOUT`. No commit.

## What I would tell a team doing this for real (draft)
An eval that starts its own full stack needs a machine to itself. Check the container load before a long run, not after a 10 minute timeout.

## Evidence
- Error text or screenshot: the `eval:` line above
- Commit or PR: none
- Eval run (if any): `verify-r3` (never started)
- Clip file and timestamp: TODO (you): clip file and timestamp
