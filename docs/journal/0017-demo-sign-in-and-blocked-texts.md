# 0017. The demo sign in skipped MFA, then Microsoft refused to send the text

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~40 min (estimate) · **Story worthy:** both

## What I saw
1. Signing in as the demo user `eva.peters` went straight to "Welcome back, Eva Peters": no second step at all.
2. With per user MFA turned on, the sign in reached "Verify your identity" offering a text to the seeded number, then: "Sorry, we're having trouble verifying your account. Please try again." View details: `Error Code: 399287`.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
1. Security defaults ask ordinary users for MFA only when a sign in looks risky; a clean sign in from a laptop is not.
2. `399287` is not in Microsoft's documented error list. Microsoft staff on Q&A tie it to telephony fraud protection: a "bad reputation" block on texts for the user, the number, or the tenant, which no admin setting lifts. A brand new free tenant sending its first texts fits. Not confirmed by Microsoft for this tenant.

## How I fixed it
Security defaults off, per user MFA on the demo user, SMS enabled in the authentication methods policy (not for sign in; scoping it to a group was dropped once the portal announced Microsoft retires its SMS and voice delivery on Feb 1, 2027). For the second factor, cleared the user's phone with `make entra-mfa CLEAR=1`, signed in, registered a code app at the "More information required" prompt, then `make entra-sync` put the seeded mobile back. The demo sign in now always asks for an app code. Spec 0005 updated (AC-1, AC-8, Security model with the accepted risk, rationale sixth update). Not committed yet.

## What I would tell a team doing this for real
(draft) "MFA is on" and "this sign in asks for MFA" are different things; and do not build a demo on SMS from a new tenant.

## Evidence
- Error text or screenshot: the two screens above, `Error Code: 399287` (request and correlation ids left out)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
