# 0003. go.sum entries missing after adding goose as a tool

**Date:** 2026-10-17 · **Week:** 0 (before week 1) · **Feature:** #3 Stack & architecture
**Time lost:** about 2 minutes (estimate) · **Story worthy:** no

## What I saw
After `go get -tool` for sqlc, then for goose, running `go tool sqlc version` failed:

```
missing go.sum entry for module providing package go.uber.org/zap (imported by github.com/pingcap/tidb/pkg/parser/charset)
```

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
Adding goose as a tool bumped modules that sqlc's dependencies share, and `go get` did not record every checksum sqlc then needed.

## How I fixed it
Ran `go mod tidy` once the first code existed. After that, `go tool sqlc` and `go tool goose` both ran. Not committed yet.

## What I would tell a team doing this for real
(draft) After adding several `tool` dependencies, run `go mod tidy` before trusting `go tool`.

## Evidence
- Error text or screenshot: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
