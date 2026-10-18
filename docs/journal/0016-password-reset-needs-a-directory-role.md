# 0016. Resetting a password needs a directory role, not just a Graph permission

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~10 min (estimate) · **Story worthy:** post

## What I saw
`make entra-demo-password NAME=eva.peters` failed:

```
seed: set the password of eva.peters: graph PATCH /users/[redacted]: 403 Forbidden
(Authorization_RequestDenied): Insufficient privileges to complete the operation.
```

The same seed app had just created 79 users, each with a password, without complaint.

## What I thought it was
A missing permission: `User-PasswordProfile.ReadWrite.All` on the seed app.

## What it actually was
Half right. Graph's Update user docs say `User.ReadWrite.All` covers every user property except `passwordProfile`. Changing an existing user's password in app only mode needs `User-PasswordProfile.ReadWrite.All` and at least the User Administrator directory role. Setting a password at create needs neither.

## How I fixed it
Dropped the `seed entra demo-password` command instead of widening the seed app (spec 0005, rationale fifth update). Demo users still get the demo password at create; an existing demo user is reset by hand in the portal. Also: a demo password made of shell special characters broke `.env` loading, and the staff name rule caught short tokens such as "van"; the runbook now says to use only letters, digits, `-`, `_`, and `!`, and never 3 letters in a row. Not committed yet.

## What I would tell a team doing this for real
(draft) "Can write users" and "can reset passwords" are different rights in Entra; decide on purpose whether your automation should have the second.

## Evidence
- Error text or screenshot: the 403 above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
