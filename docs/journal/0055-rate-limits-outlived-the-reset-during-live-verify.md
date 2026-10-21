# 0055. The per person rate limits outlived the reset and blocked the live verify

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #15 Access requests via group owner
**Time lost:** ~15 min (estimate) · **Story worthy:** post

## What I saw
During `/check verify access requests`, live run 3 (Noor asks for `SHR-Finance-RW`) stopped at the first tool call:

```
ticket_create ... outcome denied_policy, error "rate limited"
rate_limited: Too many recent requests of this kind for this user.
```

The assistant told Noor it could not open a ticket and to try again later. Earlier in the same verify, direct MCP calls to the tool server got `{"code":"rate_limited"}` on `ticket_create` for a Sales user too.

## What I thought it was
`TOOL_WRITE_LIMIT` (3 per hour). Restarting the tool server with `TOOL_WRITE_LIMIT=10` changed nothing: the next attempt was rate limited again.

## What it actually was
`ticket_create` has its own limit, `TICKET_CREATE_LIMIT` (default 5 per `TOOL_WRITE_WINDOW`, 1h); `TOOL_WRITE_LIMIT` only covers account changes (`Dispatcher.limitFor`). Noor's real Slack id already had 5 `ticket_create` ok rows within the hour from this morning's runs and the verify's runs 1 and 2. Both limits count `audit_log` rows per Slack id, so `make noord-reset` (directory, Entra, Zammad) does not clear them. The direct MCP calls hit the limit earlier because the test helper reused one fake Slack id for every thread.

## How I fixed it
No code change; the limits did their job. For the live session only, the tool server was restarted with `TICKET_CREATE_LIMIT=20` exported (`.env` unchanged), and the direct MCP checks used one fake Slack id per thread. Run 3 then passed. Not committed (nothing to commit).

## What I would tell a team doing this for real
Know which limit guards which tool, and remember that limits counted from the audit log survive every reset, so a demo or acceptance run needs its own raised limits. (draft)

## Evidence
- Error text or screenshot: audit rows 752 and 756, `ticket_create`, `denied_policy`, `rate limited`; `internal/tools/dispatcher.go` `limitFor`
- Commit or PR: none
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
