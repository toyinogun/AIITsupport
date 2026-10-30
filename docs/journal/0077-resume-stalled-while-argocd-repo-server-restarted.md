# 0077. make resume left the agent down: ArgoCD's repo-server kept restarting

**Date:** 2026-10-23 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~8 min (estimate) · **Story worthy:** post

## What I saw
`make pause` then `make resume` on k3sprox both succeeded, but two and a half minutes after resume there was still no agent pod. ArgoCD showed:

```
noord-agent   OutOfSync   Unknown
ComparisonError: Failed to load target state: failed to generate manifest ...
dial tcp 10.43.103.45:8081: connect: no route to host
```

## What I thought it was
That the new `paused` switch rendered wrongly, so the agent Deployment never came back.

## What it actually was
Not the chart. ArgoCD's `argocd-repo-server` had restarted (13 restarts in 12 days, exit code 143, no resource limits) and was unreachable for a couple of minutes. ArgoCD could not render any Application until it was back. I did not confirm why it restarts; our heavier chart may be a factor.

## How I fixed it
Nothing changed. I asked ArgoCD to refresh the Applications; once the repo-server was up the agent returned and `make smoke` passed. TODO (you): decide whether to give the repo-server resource limits and look at why its liveness probe kills it.

## What I would tell a team doing this for real
(draft) A pause or resume is only as fast as your GitOps controller's renderer; if it flaps, a healthy change looks broken, so check the controller before the chart.

## Evidence
- Error text or screenshot: the ComparisonError above
- Commit or PR: not committed yet; follows toyinogun/AIITsupport#39
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
