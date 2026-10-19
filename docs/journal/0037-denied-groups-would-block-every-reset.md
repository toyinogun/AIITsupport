# 0037. The denied groups list would have refused a reset link to almost everyone

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #13 Password reset links
**Time lost:** ~10 min (estimate) · **Story worthy:** both

## What I saw
Building the `password_reset_link` refusals, spec 0011 AC-3 (and AC-10 on the reset page) says to refuse an account that is a member, at any depth, of a code floor group or a `TOOL_DENIED_GROUPS` group. The default `TOOL_DENIED_GROUPS` is `SG-IT-Admins,APP-Helpdesk-Agents,SYNC-Entra-Users`, and in `seed/directory/noord.yaml` every user is in `SYNC-Entra-Users` unless marked `synced: false`. Built as written, nearly every Noord employee would get `protected`, and the AC-20 happy path could never pass.

## What I thought it was
TODO (you): what did you first think it was?

## What it actually was
A spec gap: `TOOL_DENIED_GROUPS` was designed in spec 0007 as groups no tool may *change*, and spec 0011 reused it as "users whose membership protects them". The two meanings differ for the Entra sync group.

## How I fixed it
Spec 0011 now gives resets their own list: the code floor plus `RESET_DENIED_GROUPS` (default `SG-IT-Admins,APP-Helpdesk-Agents`), checked at issue and again on the reset page. `TOOL_DENIED_GROUPS` keeps its job of fencing group changes, `SYNC-Entra-Users` included. Not committed yet.

## What I would tell a team doing this for real
(draft) When a new feature reuses an existing setting, check what the setting means against real data, not just its name.

## Evidence
- Error text or screenshot: none; found by reading the seed against the spec
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
