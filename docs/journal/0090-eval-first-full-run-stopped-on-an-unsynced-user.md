# 0090. The first full eval run stopped at ticket 26 because a user was not in Entra on purpose

**Date:** 2026-11-04 · **Week:** 3 · **Feature:** #20 Eval harness in CI
**Time lost:** ~25 min (estimate) · **Story worthy:** no

## What I saw
The first full run (`dry-1`) played 25 tickets, then stopped:

```
eval: the run failed: ticket ev-026: ticket ev-026: setup mfa_methods: piotr.nowak has no account in the fake Entra
stopped early: error
```

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
`piotr.nowak` is `synced: false` in the directory seed, so he has no Entra account on purpose. Ticket `ev-026` lists the `mfa_methods` setup and expects the `not_in_entra` refusal. My setup step treated "no account" as an error instead of "nothing to register".

## How I fixed it
`registerAuthenticator` in `eval/runner/setup.go` now returns nil for a user with no Entra account, so the tool server gives the refusal the ticket expects. A second full run (`dry-2`) finished all 50 tickets. Commit: not committed yet. There is no unit test for this case yet.

## What I would tell a team doing this for real
(draft) A setup step that fails on a deliberate gap in the seed data stops the whole run, so let setup be a no op when the ticket's point is that the thing is missing.

## Evidence
- Error text or screenshot: see "What I saw"
- Commit or PR: not committed yet
- Eval run (if any): `dry-1` stopped after 25 tickets; `dry-2` finished 50 tickets in 1447 s (about 9 minutes of that is startup), 30 passed, unsafe 2, wrong 9, total cost about $0.55. Runner memory was not measured. Side note: `go run` turns the harness's exit code 3 into 1, so use the built binary when you need the exit code.
- Clip file and timestamp: TODO (you): clip file and timestamp
