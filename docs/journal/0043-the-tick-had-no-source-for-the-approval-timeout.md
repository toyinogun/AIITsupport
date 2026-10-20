# 0043. The approval tick had no source for the approval timeout

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #14 MFA re-registration with approval
**Time lost:** ~5 min (estimate) · **Story worthy:** post

## What I saw
Building the agent's approval tick (spec 0012, AC-14), the adopt step has to list approvals created since "now minus (`APPROVAL_TIMEOUT` + 1 hour)". But AC-21 moves `APPROVAL_TIMEOUT` to the tool server's settings only, so the agent process never loads it. The spec named a source for the value that one of the two binaries could not read.

## What I thought it was
At first it looked like local wiring: hardcode a window, or use the setting's 24h maximum. Both pick the source of a value an acceptance criterion constrains, so it was a real decision, not a detail, and the build stopped to ask.

## What it actually was
A gap between two parts of the same spec: the Value sourcing table assumed the agent knows the timeout, while the settings list gave it to the tool server alone. Nothing checked that each binary can read every value its own code paths need.

## How I fixed it
The engineer chose to have both binaries read the one setting. `APPROVAL_TIMEOUT` moved into a small `ApprovalWindow` struct in `internal/config`, embedded in `Approvals` (the tool server) and in `Agent`, so it is still declared once. `LoadAgent` checks the same 5m to 24h range, and `.env.example` says both binaries read it. Spec 0012 AC-21 still needs an `/architect` amendment to match. Not committed yet.

## What I would tell a team doing this for real
When a spec splits settings between processes, check every value source against the process that computes it, not just against the config file. (draft)

## Evidence
- Error text or screenshot: none; found while reading AC-14 against AC-21
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
