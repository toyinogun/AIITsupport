# 0076. The helpdesk seed "lost" its token: Zammad's geo IP lookup hung every first request once egress was closed

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~40 min (estimate) · **Story worthy:** both

## What I saw
After the network policies went live on k3sprox, the helpdesk seed Job failed twice in a row:

```
seed: seed helpdesk: ZAMMAD_TOKEN is not the noord-assistant token (it changed since the seed); run make helpdesk-reset
```

The tool server's new `/readyz` said `zammad: unavailable, cause deadline`, and then, a minute later, `ok`.

## What I thought it was
First, that the policies were still settling right after the sync (the tool server recovered). Then a real token mismatch. Neither: the token hashed the same in both Secrets and in `secrets.env`, and Cilium logged no policy drops for the seed's pod.

## What it actually was
Probing from a pod with the seed's label: calls without a token answered in milliseconds; with the assistant token, the first two hung for the whole 12 second timeout and later ones took 30 ms. Rails' log named the cause: `ERROR -- : https://geo.zammad.com/lookup?ip=10.42.1.128: #<RuntimeError: 0/>`. On an authenticated request Zammad looks up the caller's IP, and with no internet egress (AC-14) the lookup blocks until its own 30 second timeout. Each Rails worker pays that once, and the seed makes exactly one tokened call. The seed then reported any error from that call as a changed token, which sent me the wrong way.

## How I fixed it
A new Rails step, `rails/services.rb`, runs right after bootstrap on the fresh and the already seeded path. It sets Zammad's four outbound services to none: `geo_ip_backend`, `image_backend` (avatar lookups that send email addresses out), `geo_calendar_backend`, and `geo_location_backend` (OpenStreetMap). It is left out of the marker, so an existing helpdesk takes it without a reset. `checkToken` now says "the token changed" only on a 401 or another user's login, and any other failure keeps its cause. Not committed yet.

**Update, after the merge (#37):** the step worked (the settings read back empty), but the seed still timed out once. The running Rails workers had cached the old settings and called `geo.zammad.com` again at 21:29:03, after the step had turned it off. Deleting the `zammad-railsserver` pod gave fresh workers, and the seed then passed ("already seeded", 15 s). Open follow up: on a fresh cluster (the proof VM) the step also runs after Rails has started, so the seed's REST fill can hit the same cached settings. It needs the settings in place before Rails serves, or a wait until the workers reload them.

## What I would tell a team doing this for real
(draft) When you close egress, list every outbound call your third party apps make on their own, and make "the dependency did not answer" a different error from "the credential is wrong".

## Evidence
- Error text or screenshot: the seed's line above; Rails' `geo.zammad.com` error; the probe's `token: 000 in 12.0s` against `none: 403 in 0.007s`
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
