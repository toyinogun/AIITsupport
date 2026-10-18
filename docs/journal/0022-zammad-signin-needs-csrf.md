# 0022. Wrong Zammad passwords seemed not to count toward the AD lockout

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
Measuring how many AD binds one wrong Zammad password costs (an open probe in spec 0006):

```
one wrong via web signin:
401
badPwdCount: 0
one wrong via basic auth:
401
badPwdCount: 1
```

The web sign in said 401 but AD saw nothing.

## What I thought it was
That the web sign in path skipped LDAP, which would have broken AC-5.

## What it actually was
`POST /api/v1/signin` without a CSRF token is refused before any password check. With the token from `GET /api/v1/signshow` (and its cookie), one wrong password is exactly one AD bind: four more wrong ones locked `eva.peters`, and Zammad then refused her right password.

## How I fixed it
Nothing to fix; the probe now fetches the CSRF token first. AC-5's "five wrong passwords" holds. Not committed yet.

## What I would tell a team doing this for real
(draft) When a probe says "nothing happened", check the request reached the code you meant to test.

## Evidence
- Error text or screenshot: the output above; `make directory-login NAME=eva.peters` then showed "the account is locked"
- Commit or PR: not committed yet
