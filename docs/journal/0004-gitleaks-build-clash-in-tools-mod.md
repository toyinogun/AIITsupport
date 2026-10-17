# 0004. gitleaks won't build next to golangci-lint in one tools.mod

**Date:** 2026-10-17 · **Week:** 0 (before week 1) · **Feature:** #4 Coding standards & tooling
**Time lost:** about 5 minutes (estimate) · **Story worthy:** post

## What I saw
After pinning golangci-lint, lefthook, gitleaks, and govulncheck in `tools.mod`, three of the four tools built, but `go tool -modfile=tools.mod gitleaks` failed:

```
# github.com/charmbracelet/x/cellbuf
../../go/pkg/mod/github.com/charmbracelet/x/cellbuf@v0.0.13-0.20250311204145-2c3ea96c31dd/cell.go:195:8: not enough arguments in call to b.Italic
	have ()
	want (bool)
.../cell.go:218:10: b.DoubleUnderline undefined (type ansi.Style has no field or method DoubleUnderline)
```

## What I thought it was
A dependency version clash between the tools, since every tool in one `tools.mod` shares one module graph. `go mod why` confirmed it.

## What it actually was
All tools in one modfile get a single set of versions. golangci-lint v2.15.0 pulls `charmbracelet/x/ansi` v0.11.8. gitleaks v8.30.1 pulls lipgloss v1, which depends on an old `charmbracelet/x/cellbuf` pseudo version that no longer compiles against that newer `ansi`.

## How I fixed it
`go get -modfile=tools.mod github.com/charmbracelet/x/cellbuf@v0.0.15`, then `go mod tidy -modfile=tools.mod`. All four tools build, and the `go 1.26.0` line stayed put. Not committed yet.

A related gotcha from the same step: the spec's version rule (a tool's own `go` line must be at most the app's `go 1.26.0`) put lefthook on v2.1.8, not "the latest 2.1.x". Every lefthook release from v2.1.9 on asks for Go 1.26.3 or newer.

## What I would tell a team doing this for real
(draft) One shared tools modfile is cheap until two tools disagree on a transitive dependency; build every tool once right after pinning, not just the first one.

## Evidence
- Error text or screenshot: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
