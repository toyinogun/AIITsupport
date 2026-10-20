# 0044. The pre push hook checked the working tree, not the commit being pushed

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #4 Coding standards & tooling
**Time lost:** ~5 min (estimate) · **Story worthy:** post

## What I saw
A commit that only touched CI files (moving CI onto a self hosted Raspberry Pi runner) would not push. The pre push hook ran `go test -race` (passed) and then `gen-check`, which failed with a diff of sqlc output nobody had touched in that commit:

```
+type ApprovalCard struct {
+	ApprovalID     uuid.UUID
...
make: *** [gen-check] Error 1
error: failed to push some refs to 'https://github.com/toyinogun/AIITsupport.git'
```

## What I thought it was
That the CI commit had somehow left the generated code out of date.

## What it actually was
`make gen-check` runs `make gen` and then `git diff --exit-code -- internal/db/queries`, which compares the **working tree** with the index. The working tree still held the uncommitted spec 0012 work, sqlc output included, so the check failed even though the commit being pushed was clean. Pushing with `--no-verify` was not an option: a Claude Code PreToolUse hook blocks it.

## How I fixed it
I checked out `HEAD` in a throwaway worktree and ran `gen-check` and `go test -race ./...` there; both passed. Then `git stash push -u`, `git push` (both pre push checks green on the clean tree), and `git stash pop`, which put all 85 uncommitted files back. Commit `b3448a5`. The hook itself is unchanged: a later fix could make the pre push checks run on the pushed commit, not the working tree.

It bit once more on the pre commit side. Staging one line of `internal/audit/audit_test.go` (a file that also held uncommitted work) made lint fail: lefthook hides the unstaged part of a partly staged file, so lint type-checked the committed test file against the uncommitted versions of the rest of the package (`*fakeStore does not implement Store (missing method AbandonStuckApprovals)`). The order that works: stash everything first, apply the one change to the clean tree, commit, push, then pop the stash (commit `dadb425`).

## What I would tell a team doing this for real
(draft) A pre push hook that reads the working tree punishes partial commits; check the commit you push, or expect people to reach for `--no-verify`.

## Evidence
- Error text or screenshot: the gen-check diff above
- Commit or PR: `b3448a5`, PR #16
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
