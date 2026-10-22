# 0072. Cilium ignores an ipBlock that points at a node, so the database's kube API allow blocked the API

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~10 min (estimate), caught before anything shipped · **Story worthy:** post

## What I saw
Spec 0015 (AC-14) allows the CNPG instance to reach the kube API with a plain NetworkPolicy, `ipBlock` `cluster.apiServerCIDR` on 6443. On k3sprox, a throwaway pod with default deny plus exactly that allow got:

```
blocked 10.43.0.1 443
blocked 172.16.70.20 6443
```

Both the Service address and the control plane's own address were refused.

## What I thought it was
A known Cilium rule rather than a typo: its CIDR selectors do not match addresses that belong to cluster nodes (`policy-cidr-match-mode` is empty on k3sprox), so a node IP is the `kube-apiserver` or `remote-node` entity, never a CIDR.

## What it actually was
Confirmed. The spec's allow is right for k3s's own policy controller (the proof VM), but under Cilium an `ipBlock` to a node address never matches, so the database's instance manager would lose the API the moment the default deny landed.

## How I fixed it
A `CiliumNetworkPolicy` with `toEntities: [kube-apiserver]` on 6443 for the `noord-pg` pods, rendered when the new value `cilium.enabled` is on (k3sprox sets it). The plain `ipBlock` rule stays for clusters without Cilium. The same test pod with the entity rule reached the API and still could not reach a node's kubelet port (`blocked 172.16.70.21 10250`). Spec wording for `/architect` to fold into AC-14. Not committed yet; the live check of the full policy set waits for the merge, because applying them by hand outside ArgoCD was refused by the session's permission rules.

## What I would tell a team doing this for real
(draft) Test every network allow on the real CNI before you write it into a spec: "the kube API by CIDR" is true on one CNI and silently false on another.

## Evidence
- Error text or screenshot: the `nc` output above, from a pod in a scratch namespace on k3sprox (since deleted)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
