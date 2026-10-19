# 0029. Adding the Anthropic SDK broke go mod tidy with an ambiguous import

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #10 Locked out user, end to end
**Time lost:** ~10 min (estimate) · **Story worthy:** post

## What I saw
After `go get github.com/anthropics/anthropic-sdk-go@latest` (v1.82.0) the build was fine, but `make tidy-check` failed:

```
github.com/toyinogun/AIITsupport/internal/agent imports
	github.com/anthropics/anthropic-sdk-go tested by
	github.com/anthropics/anthropic-sdk-go.test imports
	golang.org/x/oauth2/google imports
	cloud.google.com/go/compute/metadata: ambiguous import: found package cloud.google.com/go/compute/metadata in multiple modules:
	cloud.google.com/go v0.34.0
	cloud.google.com/go/compute/metadata v0.9.0
```

It surfaced late because the first `go mod tidy` ran with its output sent to `/dev/null`, so its failure went unseen until the check.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
`go mod tidy` also resolves the imports of dependencies' tests. The SDK's tests import `golang.org/x/oauth2/google`, and an old `golang.org/x/oauth2` (2020) in the module graph requires `cloud.google.com/go` v0.34.0, which still held `compute/metadata` before Google split it into its own module. Two modules then provide the same package.

## How I fixed it
`go get cloud.google.com/go@latest` (v0.123.0, after the split), then `go mod tidy`. `go.mod` keeps only `cloud.google.com/go/compute/metadata` and `cloud.google.com/go/auth/oauth2adapt` as indirect lines. `tools.mod` needed the SDK pinned too (see 0027, Seen again). Not committed yet.

## What I would tell a team doing this for real
(draft) Never silence `go mod tidy`; after adding a dependency, run the tidy check before you trust the build.

## Evidence
- Error text or screenshot: the error above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
