# 0032. The agent image failed in CI: the Dockerfile never copied eval

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #10 Locked out user, end to end
**Time lost:** ~5 min (estimate) · **Story worthy:** post

## What I saw
After pushing PR #11, the `images (agent)` job failed, and `ci-ok` failed with it:

```
cmd/agent/main.go:21:2: no required module provides package github.com/toyinogun/AIITsupport/eval; to add it:
	go get github.com/toyinogun/AIITsupport/eval
```

`images (toolserver)` and every other job passed.

## What I thought it was
The error suggested a missing module, but `eval` is a package in this module. So the first guess was that the Docker build context left the directory out.

## What it actually was
The Dockerfile copies only `go.mod`, `go.sum`, `cmd`, and `internal`. Spec 0008 made `cmd/agent` import `eval` for model prices, and `eval` embeds `pricing.yaml`, so the build stage had no such package. `make check` and `go test` never build the images, so nothing caught it locally.

## How I fixed it
Added `COPY eval ./eval` to the Dockerfile, then built both images locally (`docker build --build-arg CMD=agent|toolserver`). Commit 1a25a18.

## What I would tell a team doing this for real
(draft) If the Dockerfile copies directories one by one, a new top level package breaks the image while every test stays green. Build the images in the pre-push hook, or copy the whole tree and let `.dockerignore` decide.

## Evidence
- Error text or screenshot: the build log line above (GitHub Actions run 39250881591, job `images (agent)`)
- Commit or PR: 1a25a18 on PR #11
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
