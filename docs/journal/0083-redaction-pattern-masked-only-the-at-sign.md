# 0083. The Collector's `@` redaction pattern masked only the `@`

**Date:** 2026-10-31 · **Week:** 2 · **Feature:** #18 Observability & ops dashboard
**Time lost:** ~20 min (estimate) · **Story worthy:** both

## What I saw
The live canary check on Compose sent one span with a fake email in an allowed key. The span came back from Tempo with the value `jan.devries****noord.example`. The name and the domain were still there, in Tempo and in the Collector's debug output. Values with a Slack id were fully masked.

## What I thought it was
TODO (you): what did you first think it was? (The spec said the Collector gets a `blocked_values` pattern for an `@`, so the plain `"@"` looked right.)

## What it actually was
The `redaction` processor replaces only the part of a value that the pattern matches. A bare `@` pattern matches one character, so the rest of the address stays. Spec 0016 AC-4 says "a pattern for an @" and does not say the pattern must cover the whole token.

## How I fixed it
Changed the pattern to the whole token around the `@` (`\S*@\S*`) in `collector.yaml`. Added a unit test in `internal/telemetry/collector_test.go` that the pattern matches a full address, not just the `@`. Re-ran the live check: name, email, Slack id, message text and the unlisted key were all absent from Tempo and the debug output. Not committed yet. Spec 0016 AC-4 should be reworded to say "whole token".

## What I would tell a team doing this for real
(draft) A redaction rule is only done when you have fed it a real looking value and read what comes out the other end; a pattern that "matches the @" can still leave the whole address behind.

## Evidence
- Error text or screenshot: `"tool","value":{"stringValue":"jan.devries****noord.example"}` in the Tempo trace
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
