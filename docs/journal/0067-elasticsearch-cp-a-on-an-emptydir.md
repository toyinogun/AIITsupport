# 0067. Elasticsearch's config copy failed on the emptyDir

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
On the first k3sprox install of the helpdesk (spec 0015 task 3), `elasticsearch-0` sat in `Init:Error` with 5 restarts while Zammad's Postgres, Redis and Memcached were already running:

```
cp: preserving times for '/config/.': Operation not permitted
```

## What I thought it was
A permissions problem writing into the emptyDir. My first local repro failed with "Permission denied" on every file, but only because I left out the fsGroup's extra group, which is what makes the emptyDir writable in the pod.

## What it actually was
To keep the read only root filesystem, an init container copies the image's config dir into an emptyDir with `cp -a`. `-a` also sets the source's times on the target directory itself, and the non root user (uid 1000) may not change the times of an emptyDir root it does not own. Every file copies; only that last step fails, and it fails the container.

## How I fixed it
Changed the copy to `cp -r`, which preserves nothing. Checked locally as `1000:0` with `--group-add 1000` on a read only root: `-a` fails the same way, `-r` copies all 10 entries. PR #27, not merged yet.

## What I would tell a team doing this for real
(draft) Reproduce a pod's identity exactly (user, group, and fsGroup's supplemental group) before you trust a local repro of a permission error.

## Evidence
- Error text or screenshot: above
- Commit or PR: https://github.com/toyinogun/AIITsupport/pull/27
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
