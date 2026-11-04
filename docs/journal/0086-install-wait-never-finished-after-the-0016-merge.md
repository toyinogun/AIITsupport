# 0086. The install wait never finished after the spec 0016 merge

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #18 Observability & ops dashboard
**Time lost:** ~1 hour (estimate) · **Story worthy:** both

## What I saw
After merging PR #52 and running `make install CLUSTER=k3sprox`, the run sat in its `wait` step. ArgoCD showed:
`noord-agent  Synced  Progressing`, `noord-k3sprox  OutOfSync  Progressing`, no `noord-grafana` Application, and
`waiting for healthy state of argoproj.io/Application/noord-agent`. The last `noord-migrate` Job was 3 days old and the database was at migration 6, not 8. The Grafana Ingress reported `Service "grafana" not found`.

## What I thought it was
TODO (you): what did you first think it was? (the session shows only "it is still rolling out")

## What it actually was
Three separate things stacked up:
1. `values.yaml` pins the image tag (the old `560b0d1`), so `make install` alone never rolls the new agent or migration 00008. `make release CLUSTER=<name> SHA=<sha>` is a separate required step, and the install output does not say so until you run release.
2. A sync wave deadlock. The Grafana Tailscale Ingress lives in the `noord-agent` Application (wave 2) and points at Service `grafana`. That Service only exists once the child Application `noord-grafana` syncs, but the parent `noord-k3sprox` holds `noord-grafana` until `noord-agent` is Healthy. ArgoCD keeps an Ingress Progressing until it has an address, and Tailscale gives none without a valid backend. So `noord-agent` stays Progressing for good.
3. Install preflight refuses any untracked file in the tree, so two unrelated untracked files had to be moved out for the run.

## How I fixed it
Rolled the images with `make release CLUSTER=k3sprox SHA=5df47a6...` (commit `b61ed1f`), which applied migration 00008. For the deadlock I moved the Grafana Ingress out of the agent part into its own template that renders only in the grafana part, next to its Service (branch `fix/grafana-ingress-deadlock`, test `TestGrafanaIngressShipsWithItsService` written first and seen failing). The chart tests and the install tests only rendered each part alone, so none saw the cross-Application wait.

## What I would tell a team doing this for real
(draft) A sync wave only orders objects inside one Application; an object that points at a Service created by a different Application can deadlock the whole tree, so keep an Ingress in the same Application as its Service, and test a fresh install, not only an update.

## Evidence
- Error text: `waiting for healthy state of argoproj.io/Application/noord-agent`; Ingress event `Service "grafana" not found`
- Commit or PR: PR 52 (merge `5df47a6`), seal commit `1d06393`, release commit `b61ed1f`
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
