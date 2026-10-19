# 0036. Samba ignores the password history hint on a delegated reset

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #13 Password reset links
**Time lost:** ~10 min (estimate) · **Story worthy:** post

## What I saw
The build step 1 live probe (`TestLiveResetPasswordAsTheResetAccount`, `TEST_SAMBA=1`) passed every check but one. `svc-pwreset` reset a locked staff password, the lock cleared, `badPwdCount` read 0, the new password bound, and AD refused it everything else (an admin account, another attribute, a group change, a create). But resetting to the password the account already had was accepted:

```
password_live_test.go:78: reusing the current password = <nil>, want a policy refusal (does Samba ignore the hints control?)
```

## What I thought it was
That Samba ignores the AD password policy hints control (OID `1.2.840.113556.1.4.2239`), which Windows AD uses to hold an admin reset to the history rule. Spec 0011 named this risk up front.

## What it actually was
Samba accepts the control (not critical) but does not apply the history rule to a reset. The length rule still applies: a too short password is refused.

## How I fixed it
The spec's own fallback: the reset page no longer shows the history line in its rules, and the live test now pins Samba's behavior (a reuse is accepted), so it fails the day Samba starts honoring the control and the line can come back. Not committed yet.

## What I would tell a team doing this for real
(draft) Probe the one directory behavior your design leans on before building on it, and write the fallback into the spec so a surprise costs ten minutes, not a redesign.

## Evidence
- Error text or screenshot: the test line above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
