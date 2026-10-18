# 0011. Live Samba tests hang forever on Docker's credential helper

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~20 min (estimate) · **Story worthy:** post

## What I saw
`TEST_SAMBA=1 go test -run TestLiveReadScope ./internal/seed/entra/` printed one line and then sat there until Go's 10 minute test timeout killed it:

```
🐳 Building image noord-samba:test
...
panic: test timed out after 10m0s
```

A plain `docker build seed/directory/samba` of the same image finished in 0.6 seconds (all layers cached), so Docker itself was fine.

## What I thought it was
A slow or stuck image build (an `apt` mirror, BuildKit), since the last log line was "Building image".

## What it actually was
The goroutine dump showed testcontainers stuck in `credentialsCache.get` (`docker_auth.go:158`), waiting on a subprocess. Before a build, testcontainers looks up registry credentials through the credential helper named in `~/.docker/config.json` (`credsStore: osxkeychain`), and that helper never returned. Why the helper hangs (a keychain prompt nobody can see, most likely) is not confirmed.

## How I fixed it
Not a code fix. Running the tests with an empty auth config makes testcontainers skip the credential helper:

```bash
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 go test -race ./internal/seed/...
```

With that, the container started and the live scope test ran in 13 seconds. The runbook's live test command now includes it. Not committed yet.

## What I would tell a team doing this for real
(draft) When a test "hangs on build", read the goroutine dump before blaming the build; the hang was a credentials lookup the build log never mentioned.

## Evidence
- Error text or screenshot: the `credentialsCache.get` frame above, from the timed out run's goroutine dump
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
