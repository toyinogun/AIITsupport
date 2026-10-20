# 0041. Graph will not delete a user's default MFA method while others remain

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #14 MFA re-registration with approval
**Time lost:** ~15 min (estimate) · **Story worthy:** both

## What I saw
The first live approve from Slack ended with the card saying `Approved by @Toyin Ogunseinde, but the clear did not finish (method delete failed). Escalated, check the account by hand.` The audit row read `error / method delete failed` with `cleared_authenticator 0, cleared_phone 0`. The live probe then showed why:

```
signInPreferences: error status 400
delete Authenticator first: status 400, code "badRequest", message "Cannot delete default method with other methods configured. Please change default method before deletion."
delete mobile phone: ok
delete Authenticator again, after the phones: ok
```

## What I thought it was
Either the default method rule the spec had guessed at, or a permission that had not propagated yet after granting `UserAuthMethod-MicrosoftAuthApp.ReadWrite.All`.

## What it actually was
Graph refuses to delete a user's default sign in method while she has other methods. Once Microsoft Authenticator is registered, it is the default. The spec planned to read the default from `signInPreferences`, but that answers 400 to the tool server app, so the clear fell back to the spec's fixed order, which tried the Authenticator first.

## How I fixed it
The fixed order is now the phones first (`alternateMobile`, `office`, `mobile`) and the Authenticator last. A new test covers it with the fake Graph's default rule. The safety net worked as designed before the fix: the clear stopped at the first refusal, changed nothing, and escalated. Spec 0012 AC-11 is owed an amendment to this order (`/architect`). Not committed yet.

## What I would tell a team doing this for real
(draft) Probe the real API's ordering rules before you trust a documented fallback, and make the first failure stop cleanly so a wrong guess costs a ticket, not an account.

## Evidence
- Error text or screenshot: above, from `TEST_ENTRA=1 TEST_ENTRA_DELETE=1 go test -run LiveAuthenticatorProbe -v ./internal/graph/`
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp (the Slack card showing "method delete failed" is a good shot)
