# 0064. A stray reset password crashed the tool server on the first install

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
On the first live install the tool server crash looped:

```
toolserver: toolserver config: RESET_HTTP_ADDR is required but not set
RESET_LDAP_BIND_DN is required but not set
RESET_PUBLIC_URL is required but not set
```

## What I thought it was
The reset settings reaching the pod partly, through the chart.

## What it actually was
The hand made k3sprox `secrets.env`. The script that filled `LDAP_BIND_PASSWORD=` used Python's `str.replace`, which also matched the tail of `RESET_LDAP_BIND_PASSWORD=`, so one of the four all or none reset settings was set (spec 0011).

## How I fixed it
Blanked `RESET_LDAP_BIND_PASSWORD` in the local, gitignored `secrets.env` and reran `make install`: it dropped the one sealed value, pushed the reseal, and only the tool server restarted (the agent pod kept running, AC-8). Idea for later: preflight could run each workload's config loader on the values it is about to seal, which would have caught this before anything was applied.

## What I would tell a team doing this for real
(draft) Run the app's own startup checks before you deploy its config, not after.

## Evidence
- Error text: above
- Commit or PR: `fdfd0fe` (the reseal); the fix itself is in a gitignored file
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
