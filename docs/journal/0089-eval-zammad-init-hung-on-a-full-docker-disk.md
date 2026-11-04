# 0089. The eval's Zammad stack hung for 12 minutes on a nearly full Docker disk

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #20 Eval harness in CI
**Time lost:** ~15 min (estimate) · **Story worthy:** no

## What I saw
The first live probe of the harness's own Zammad stack (`TestZammadStartsSeeded`) never finished. `zammad-init` stayed `Up` for 12 minutes, the last lines of its log were `Dropping indexes... done.` and `Deleting pipeline... done.`, and Elasticsearch reported cluster status `red` with `unassigned_shards: 44` and `active_shards: 0`. The Elasticsearch log carried `high disk watermark [90%] exceeded ... free: 5.1gb[8.8%]`.

## What I thought it was
The Zammad seed being slow (the spec estimates about 2 minutes), or two Zammad stacks on one 8 GB Docker VM competing for memory.

## What it actually was
The Docker VM disk was 91% full (`/dev/vda1 59G 51G 5.2G`). Elasticsearch will not allocate new shards above its disk watermarks, so `zammad-init` could not recreate its indexes and waited. The developer's own stack was fine only because its shards were allocated long ago.

## How I fixed it
The Compose override the harness generates now sets `cluster.routing.allocation.disk.threshold_enabled: "false"` on `zammad-elasticsearch`. The index is throwaway and its volume is deleted at the end of the run. I did not prune anything on the machine. Commit: not committed yet.

## What I would tell a team doing this for real
(draft) A test stack that works on a fresh machine can still hang on a full disk, so give throwaway Elasticsearch no disk threshold and check the runner's free disk before blaming the seed.

## Evidence
- Error text or screenshot: cluster health `status: red`, `unassigned_shards: 44`; `high disk watermark [90%] exceeded`
- Commit or PR: not committed yet
- Eval run (if any): none, this was the harness's own probe
- Clip file and timestamp: TODO (you): clip file and timestamp
