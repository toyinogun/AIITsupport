# 0018. The tool server app can read the tenant admin's phone methods

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~0 min (estimate; a finding from the AC-11 probe, not a breakage) · **Story worthy:** post

## What I saw
The AC-11 admin probe in `TestLiveSync`, run with the tool server app (`GRAPH_*`) on the real tenant:

```
live_test.go:248: admin probe: reading your admin's phone methods with the tool server app gives status 0 (0 means allowed): <nil>
--- PASS: TestLiveSync/fence_and_least_privilege (0.71s)
```

Graph answered 200: the app may read the phone methods of a user the sync does not manage, your own admin included. In the same run, `POST /users` and `POST /users/{id}/authentication/temporaryAccessPassMethods` with the same app both got 403.

## What I thought it was
TODO (you): did you expect Graph to refuse this, or did you expect it to be allowed?

## What it actually was
Expected behavior. Application permissions such as `UserAuthenticationMethod.Read.All` and `UserAuthMethod-Phone.ReadWrite.All` apply tenant wide; nothing in Entra limits them to Noord's staff. The only wall today is our own fence in `internal/graph`: it resolves a user by `mailNickname` and refuses anyone without `employeeType` = `noord-sync` (`ErrNotManaged`) before any auth method call.

## How I fixed it
Nothing to fix in code. The fence already holds: `make entra-mfa NAME=admin` fails with `user "admin": not managed by the Noord sync`, and `make entra-mfa NAME=made.up` fails with `not found in Entra`. The result is recorded in `docs/runbooks/entra-tenant.md` (section 8, "Admin probe result"). The Entra side wall (Authentication Administrator scoped to a "Noord Staff" administrative unit) stays a spec 0005 follow up, after #14. Not committed yet.

## What I would tell a team doing this for real
(draft) Graph app permissions are tenant wide, so your own code is the only fence until you scope the app with an administrative unit; test that fence against the real admin account.

## Evidence
- Error text or screenshot: the probe log line above; `/check verify entra tenant & sync` run on 2026-10-18
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
