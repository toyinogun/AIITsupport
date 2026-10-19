# 0038. Samba kept accepting the old password for an hour after a reset

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #13 Password reset links
**Time lost:** ~15 min (estimate) · **Story worthy:** both

## What I saw
The first live reset from Slack worked end to end: the link, the page, the audit pairs, the closed ticket, and a fresh `pwdLastSet` in AD. But `make directory-login NAME=jan.devries`, which binds with the seeded dev password, still succeeded afterwards, though the new password was a random one.

## What I thought it was
That the new password had been set to the seeded one by chance. Once that was ruled out, Samba's `old password allowed period`, which keeps a replaced password valid for 60 minutes by default.

## What it actually was
That setting, and on Samba it covers LDAP simple binds too, not just NTLM. A probe in the live test confirmed the old password still bound right after a delegated reset. Adding `--option="old password allowed period = 0"` to `samba-tool domain provision` changed nothing: provision silently dropped that option from `smb.conf` (`testparm` still reported `60`).

## How I fixed it
The Samba entrypoint now writes `old password allowed period = 0` into `[global]` itself right after provisioning, and fails if the line is missing. The live test now asserts the old password is refused (invalid credentials) right after a reset. The dev Samba was rebuilt with `make directory-reset`. Not committed yet.

## What I would tell a team doing this for real
(draft) After a password reset, test that the old password fails, not only that the new one works; and check a provision flag landed in the config instead of trusting the command line.

## Evidence
- Error text or screenshot: `password_live_test.go:78: the old password still binds right after the reset`; `testparm` showed `60` with the option passed to provision
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
