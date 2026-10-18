# 0013. Graph rejects the fictional phone range the spec picked

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~35 min (estimate) · **Story worthy:** post

## What I saw
The first real sync (`make entra-sync ONLY=eva.peters`) created the user, then failed on the phone:

```
seed: entra sync: set phone eva.peters: graph POST /users/[redacted]/authentication/phoneMethods:
400 Bad Request (invalidPhoneNumber): The provided phone number is formatted properly, but is an invalid phone number.
```

Every unit test had passed, because the fake Graph stores any number it is given.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
Graph checks that a number is in a range allocated to a carrier, the way Google's libphonenumber does. `+31 60` is not an allocated Dutch mobile range. The first fix, Ofcom's drama range `+44 7700900NNN`, failed the same way: ranges reserved for fiction are mostly unallocated, so that kind of check rejects them. The `phonenumbers` library (9.0.41) marks both rejected numbers invalid, which matches Graph.

## How I fixed it
Two spec updates in one morning. The second checked candidates with `phonenumbers` first, then probed Graph live: `+1 2025550117` (the North American 555-0100 to 555-0199 range, reserved for fiction) was accepted on `eva.peters`. The generator now makes `+1 20255501NN` with a 2 digit position (99 staff max). The cross check's rules stayed: an anchored hygiene regex, no demo number equal to a generated one, and a sort over the exact usernames. The fake Graph now refuses both rejected ranges, with a regression test. Not committed yet.

## What I would tell a team doing this for real
(draft) Check a "fictional" value against the real validator before you write it into a spec; a fake that accepts anything hid the problem twice.

## Evidence
- Error text or screenshot: the 400 above, twice; the `phonenumbers` check table in spec 0005's rationale
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
