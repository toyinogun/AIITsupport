# 0027. tidy-check failed on tools.mod after adding app dependencies

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~2 min (estimate) · **Story worthy:** no

## What I saw
After adding `slack-go` and `goldmark` and running `go mod tidy`, `make check` stopped at the tidy check:

```
diff current/go.sum tidy/go.sum
+github.com/slack-go/slack v0.30.1 h1:...
make: *** [tidy-check] Error 1
```

## What I thought it was
That `go mod tidy` on `go.mod` was enough.

## What it actually was
`tools.mod` describes the same module, so it tidies over the same packages and needs the new dependencies in `tools.mod` and `tools.sum` too.

## How I fixed it
`go mod tidy -modfile=tools.mod`, then `make check` passed. Not committed yet.

## What I would tell a team doing this for real
(draft) With a second modfile for tools, every new dependency means two tidies.

## Evidence
- Error text or screenshot: the diff above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
