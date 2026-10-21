# 0053. A full Docker disk stalled the Zammad reset at the Elasticsearch watermark

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #15 Access requests via group owner
**Time lost:** ~12 min (estimate) · **Story worthy:** post

## What I saw
`make noord-reset` before the live runs sat for about 9 minutes on `docker compose --profile helpdesk wait zammad-init`. The init container's log stopped at:

```
Dropping indexes... done.
Deleting pipeline... done.
```

and the container sat at 0% CPU. Elasticsearch reported:

```
"status":"red", ... "unassigned_primary_shards":11
disk_threshold : the node is above the high watermark cluster setting
[cluster.routing.allocation.disk.watermark.high=90%], having less than the minimum
required [5.8gb] free space, actual free: [5.7gb], actual used: [90%]
```

## What I thought it was
A slow Zammad start (a fresh Elasticsearch index rebuild usually takes a while), so the first guess was "just wait longer".

## What it actually was
Docker's disk was 90% full (5.7 GB free of 58.3 GB; the Mac itself had 6.7 GB free). Elasticsearch refuses to place shards above its high disk watermark, so the freshly created indexes stayed unassigned and Zammad's init waited on them forever, with no error.

## How I fixed it
Worked around, not fixed: raised the watermarks on the dev Elasticsearch container (`low` 94%, `high` 96%, `flood_stage` 98%) through `_cluster/settings`, then a `_cluster/reroute`. The cluster went yellow (normal for one node), init finished, and the helpdesk seed completed. Every `make helpdesk-reset` creates a fresh Elasticsearch container, so this comes back until Docker disk is freed (`docker system df` showed about 9 GB of unused images and 4.5 GB of build cache). No code change; not committed.

## What I would tell a team doing this for real
(draft) A stalled health wait with no error is often a disk watermark; check free disk and the search cluster's health before blaming the app.

## Evidence
- Error text or screenshot: above
- Commit or PR: none (workaround only)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
