# 0080. Every smoke check failed on a healthy cluster: a new pod's first second is refused on stock k3s

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #17 One command GitOps install
**Time lost:** ~40 min (estimate) · **Story worthy:** both

## What I saw
The fresh install finished its wait with every ArgoCD Application Synced and Healthy, and then the smoke step failed all four checks:

```
install proofvm:   fail toolserver_readyz network
install proofvm:   fail agent_readyz network
install proofvm:   fail tools_list network
install proofvm:   fail migrations network
```

`make smoke` on its own gave the same four lines every time.

## What I thought it was
A network policy gap, like the one in journal 0079. I checked with a long lived pod carrying the smoke Job's label: it reached the tool server, the agent and the database straight away. Then I thought it was DNS (the Job's URLs end in `.svc`), but those names resolved and connected from the same pod.

## What it actually was
A timing race, not a policy. On a stock k3s cluster the node's network policy controller needs a moment to learn a new pod's address, and a pod's first connections are refused until it does. A pod that loops `nc` once a second showed `t=1s refused`, then `ok` from `t=2s` on. The smoke Job connects in its very first second, so all four checks failed, even though the system was healthy. On the homelab cluster (Cilium) this never showed up, and the helpdesk seed Job did not hit it because it waits for a Zammad login first.

## How I fixed it
`seed smoke` now retries a connection failure (the `network` cause) until `SMOKE_SETTLE` (default 10s) has passed since the run began. A wrong answer is never retried, and a service that stays down still fails as `network` after the settle period. Tests cover the three cases (`TestSmokeSettlesNewPodNetwork`) and the setting's default and range. Needs a new seed image, so it reaches the cluster through a release.

## What I would tell a team doing this for real
(draft) If a check runs in a brand new pod, give it a short grace period for the network to catch up, and prove it with a once a second probe from the first moment. A probe that waits for "Ready" first hides the very window that breaks you.

## Evidence
- Error text or screenshot: the four `fail ... network` lines; the probe output `t=1s refused t=2s ok`
- Commit or PR: TODO (you): the PR link once merged
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp

## Related
- `make resume` bumps `seedRun` but does not commit it when the cluster is not paused (`paused` is already false, so nothing triggers the commit). I committed the bump by hand three times. That is its own fix, still owed, as entry 0081.
