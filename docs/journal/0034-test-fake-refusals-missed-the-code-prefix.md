# 0034. The agent's test fake sent refusals without the code the real tool server sends

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #11 Privileged request refusal & escalation
**Time lost:** ~5 min (estimate) · **Story worthy:** post

## What I saw
A new agent test for the failure path after a refused privileged request failed:

```
reason = assistant_failure, want privileged_request
IT posts = [":rotating_light: *Privileged request refused*\n...\nRan: privileged_access_change unknown, escalate ok\n..."]
```

The blocked call showed up as `unknown` instead of `blocked`, so the agent didn't see the blocked attempt and escalated with the wrong reason.

## What I thought it was
A bug in the new parser that reads the refusal code from a stored tool result.

## What it actually was
The parser was right; the fake was wrong. The real tool server's refusal text starts with `<code>: ` (for example `blocked: Privileged changes are never made...`), and spec 0009 AC-9 reads the code from that prefix. `agenttest.Refused` built the text without the prefix, so every refusal in the agent's tests looked different from what production stores. Until #11 nothing read the stored text, so the gap never showed.

## How I fixed it
`agenttest.Refused(code, sentence)` now builds `code + ": " + sentence`, the same as the tool server. All agent tests pass with it. Not committed yet.

## What I would tell a team doing this for real
(draft) A fake has to match the real wire format byte for byte the moment any code starts parsing it; check it against a recorded real response, not memory.

## Evidence
- Error text or screenshot: the test output above (`TestFailurePathEscalation/blocked:_privileged_request`)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
