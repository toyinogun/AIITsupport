# 0042. The Slack test account was a guest, and its email mapped to nobody

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #14 MFA re-registration with approval
**Time lost:** ~15 min (estimate) · **Story worthy:** post

## What I saw
The first live MFA request from Slack got the gate refusal instead of the MFA flow:

```
I can only help Noord staff with a confirmed work email in this workspace. Please contact IT directly.
{"msg":"agent_escalated","reason":"slack_profile","ticket":false,"cause":"none"}
{"msg":"agent_turn_done","outcome":"escalated","model_calls":0,"tool_calls":0}
```

## What I thought it was
That the requester's email did not match a Noord account.

## What it actually was
Two setup problems in the demo workspace, neither in the code. The requester's Slack account was a guest (`is_restricted` true), and the Slack gate refuses guests by design (spec 0008, AC-3); that also explains earlier "I'm locked out" refusals. And the directory overlay gave the demo email to `jan.devries`, but that address belonged to the workspace owner's Slack account, which was meant to be the IT approver; the requester's email mapped to no Noord user.

## How I fixed it
Changed the requester to a regular member in Slack's member admin. Pointed the overlay entry at `eva.peters` with the requester's own email (read from Slack and written straight to the gitignored overlay, never printed) and ran `make directory-reset`. The owner account stays the approver in `SLACK_IT_APPROVERS`. No code change.

## What I would tell a team doing this for real
(draft) Before a live demo, check each test account against the gate fields and the directory mapping with one read only script; a refused gate looks exactly like a bug.

## Evidence
- Error text or screenshot: above, from the `make dev` log and the Slack thread
- Commit or PR: none (configuration only)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
