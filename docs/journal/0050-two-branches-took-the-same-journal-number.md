# 0050. Two branches took the same journal number

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #14 MFA re-registration with approval
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
Before opening the feature 14 PR, `git rev-list --count HEAD..origin/main` said the branch was 5 commits behind. Merging `origin/main` stopped with:

```
CONFLICT (content): Merge conflict in .github/workflows/ci.yml
CONFLICT (content): Merge conflict in docs/journal/README.md
CONFLICT (content): Merge conflict in docs/runbooks/ci-runner.md
```

Main had merged its own `0039-pi-runners-share-a-buildx-config.md` while this branch held `0039` to `0048` uncommitted. Main had also moved CI from the Pi (`rpi01`) to ARC runners (`k3sprox`), with the same fail open fork check this branch had just fixed (journal 0048).

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
Journal numbers are handed out per working copy, so two branches that log incidents at the same time pick the same next number. The CI conflict was two fixes to the same `runs-on` lines.

## How I fixed it
Renumbered main's single entry to `0049` (file, title, index row, and the runbook link), because code comments and spec 0012 cite this branch's `0039`, `0041`, and `0043`. In CI, kept main's `k3sprox` runners and put the branch's allow list on top: `(github.event_name == 'push' || github.event.pull_request.head.repo.full_name == github.repository) && 'k3sprox' || 'ubuntu-latest'`. Merge commit `d78e95c`, in PR #18.

## What I would tell a team doing this for real
(draft) Give journal entries a branch or date prefix, or number them at merge time; sequential numbers only work with one writer.

## Evidence
- Error text or screenshot: the three `CONFLICT (content)` lines above
- Commit or PR: `d78e95c`, https://github.com/toyinogun/AIITsupport/pull/18
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
