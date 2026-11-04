# 0088. `kubectl scale` could not hold a Collector outage under ArgoCD self heal

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #18 Observability & ops dashboard
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
To test "the assistant still works with the Collector down" on k3sprox, I ran `kubectl -n noord-agent scale deploy/otel-collector --replicas=0`. A minute later the Collector was back (`replicas=1`, a new pod 94 seconds old), before the test message was sent.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
Every Application has automated sync with self heal, so ArgoCD put the replica count back to what git says.

## How I fixed it
I blocked traffic with a temporary `CiliumNetworkPolicy` `ingressDeny` on the Collector instead (ArgoCD does not track an object it did not create), ran the test, and deleted the policy. The same trick worked for Tempo. Nothing in the repo changed.

## What I would tell a team doing this for real
(draft) Under GitOps you cannot fake an outage by editing live state; block the network with an object git does not own, and delete it afterwards.

## Evidence
- Error text: none, `replicas=1` right after `scale --replicas=0`
- Commit or PR: see the PR that ticks the 0016 verify steps
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
