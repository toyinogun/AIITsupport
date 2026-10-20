# 0051. Parallel test packages raced to build one Samba image tag, so a docs only PR failed CI

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #6 Noord directory & seeded mess (the `sambatest` helper, spec 0004)
**Time lost:** ~20 min (estimate) · **Story worthy:** post

## What I saw
PR 19 changed only Markdown, yet the `test` job (and so `ci-ok`) failed:

```
--- FAIL: TestLiveReadScope (46.97s)
    live_test.go:265: start Samba: run Samba container: generic container: create container: build image:
    failed to create an image docker.io/library/noord-samba:test with target sha256:0dd63b2d...
    after deleting the existing one: AlreadyExists: image "docker.io/library/noord-samba:test": already exists
FAIL	github.com/toyinogun/AIITsupport/internal/seed/entra	50.778s
```

## What I thought it was
A flake unrelated to the PR, since nothing in it touches Go. Main's runs before it were green, so it looked like bad luck on the runner rather than a bug.

## What it actually was
A real race. `go test ./...` runs packages as parallel processes, and five of them (`cmd/seed`, `internal/ldap`, `internal/mcpserver`, `internal/seed/directory`, `internal/seed/entra`) each built the same `noord-samba:test` tag through testcontainers' `WithDockerfile`. On a fresh runner dockerd with no cached image, two builds finished close together, and the second one lost tagging the image in the containerd store. It only shows when builds overlap, which is why main was usually green.

## How I fixed it
`sambatest.buildImage` now builds the image under an exclusive `flock` on `$TMPDIR/noord-samba-build.lock` (testcontainers' `DockerProvider.BuildImage`). Every container then starts from the built tag; testcontainers finds it locally and never pulls. Builds are serialized, and container starts still run in parallel. Checked locally by deleting the image and running the five packages in parallel with `TEST_SAMBA=1`: all `ok`. `syscall.Flock` is unix only, which fits macOS dev and Linux CI. Commit: 55e8015.

## What I would tell a team doing this for real
(draft) A shared, named test image is shared state across test processes: build it once, under a lock or in its own CI step, and never inside every test package.

## Evidence
- Error text or screenshot: above, from CI run 39486895890, job `test`
- Commit or PR: 55e8015, https://github.com/toyinogun/AIITsupport/pull/19
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
