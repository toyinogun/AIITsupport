# 0078. Reaching the hypervisor API took two firewalls, and both failed as the same silent timeout

**Date:** 2026-10-30 · **Week:** 2 · **Feature:** #17 One command GitOps install
**Time lost:** ~1 hour (estimate) · **Story worthy:** post

## What I saw
From the Mac, away from home, every connection to the hypervisor API port timed out:

```
nc: connectx to 172.16.70.10 port 22 (tcp) failed: Operation timed out
172.16.70.10:8006 no
```

The router showed up on the tailnet and answered on its own address, and Tailscale listed the homelab subnet as one of its routes. The Mac's route table sent that subnet through the Tailscale interface. Still nothing came back.

## What I thought it was
That the subnet route was not advertised or approved, because the first check showed no routes. That was a lookup mistake: I had matched a stale machine entry with a similar name (the real one had a capital S). After approving the route, I thought the router firewall was blocking it.

## What it actually was
Two independent gates, each failing as a plain timeout:
1. The router's firewall rules for the Tailscale interface were default deny, with a pass rule per listed host. The hypervisors were not listed.
2. The tailnet access policy allowed my user only one `/32` and one port inside that subnet (`172.16.70.40/32:443`). Tailscale dropped everything else before it reached the router.

The tell was the router's rule counters. After I added the pass rules, every rule and even the logging block rule still read `0/0 B`, so my packets never arrived at the router at all.

## How I fixed it
Added a narrow pass rule on the router (my Mac's tailnet address only, to the two hypervisors, port 8006) and a matching accept rule in the tailnet policy. I checked the policy diff showed exactly the two added lines before saving. Both are marked "remove after the proof". Not committed (neither lives in this repo).

## What I would tell a team doing this for real
(draft) With two independent gates, a timeout does not say which one dropped the packet. Check the counter on the deny rule you control: if it is zero, the packet never got there, so look upstream. And preview the access policy for your own user before you touch any firewall.

## Evidence
- Error text or screenshot: `Operation timed out` on ports 22 and 8006; router rule counters `0/0 B` after the pass rules; policy preview for my user listing only `172.16.70.40/32:443` in that subnet
- Commit or PR: not committed (router and tailnet settings)
- Eval run (if any): n/a
- Clip file and timestamp: TODO (you): clip file and timestamp

## Related
- The same session found that neither hypervisor has 12 GB of free RAM for the spec's proof VM (one at about 81% used, the other about 74%). The proof VM is sized at 8 GB on the less loaded node, or a CI runner and one other VM are stopped for the run. TODO (you): which option you chose.
- What I first thought it was and the time lost are estimates from the session; adjust them in your own words.
