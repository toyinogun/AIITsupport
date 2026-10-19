# internal/resetpage

## Overview

The password reset page the tool server serves on its own listener (spec 0011). `GET /reset/{token}` shows the form; `POST /reset/{token}` sets the password as `svc-pwreset` inside `audit.Recorder.Run` (tool name `password_reset`, not a registry tool, so no MCP call reaches it) and closes the ticket. No session and no cookie: the token in the path is the only key. Only the tool server imports it.

## Key files

| File | Owns |
|---|---|
| `page.go` | `Route`, the fixed page texts, `Config` and its narrow interfaces (`Links`, `Directory`, `PasswordWriter`, `Tickets`, `Accounts`, `KillSwitch`), `New`, `Handler` (the whole reset mux plus the security headers), the GET form, the cached password policy |
| `submit.go` | the POST: field checks, the in flight slot, the kill switch, the account recheck, the claim, the audited write, the revert, the `reset_submit` outcomes |
| `templates/` | `layout.html`, `form.html`, `message.html`, embedded; no script |

## Conventions

- The listener serves only `GET` and `POST` on `Route` and `GET /healthz`. Every response sets `Cache-Control: no-store`, `Pragma: no-cache`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, and the CSP (plus HSTS when `RESET_PUBLIC_URL` is https). Pages hold no script.
- Every outcome is a fixed text and status from the spec's page table (`invalid` 410, `fields` and `policy` 422, `paused` and `try_later` 503, `blocked` 403, `failed` 502, `done` 200). Never show a directory or Zammad message.
- GET does one indexed read and at most one LDAP read, and writes nothing. The POST reads only the form body (4 KiB cap); field errors return before any write or audit row.
- The POST order is fixed: in flight slot, one `TOOL_CALL_TIMEOUT` deadline, kill switch, account recheck (found, under `TOOL_USER_OUS`, enabled, no protected group), claim, then the write inside `Recorder.Run` with a second kill switch read. Anything short of success reverts the claim. A timeout after the modify rereads `pwdLastSet` before deciding.
- A Zammad failure after a successful write still shows `done`; the audit result has `ticket_closed` false.
- Log one `reset_submit` line per POST past the field checks (`outcome`, `cause`, `trace_id`) in a `reset.submit` span named by `Route`, never the path. Never log the token, its hash, a password, or an account name.
- This package is on both agent deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`).

## Commands

```bash
go test -race ./internal/resetpage/                                                   # unit tests on fakes
go test -race -run TestCanaryResetLink ./internal/agent/ ./internal/mcpserver/        # no token, hash, or password in logs, spans, audit, or stored messages
```

## Related specs

- [0011 password reset links](../../docs/specs/0011-password-reset-links/index.md) (AC-7 to AC-16, AC-19), with [verify.md](../../docs/specs/0011-password-reset-links/verify.md)
- [the password reset runbook](../../docs/runbooks/password-reset.md)

_Drafted by /sync from the introducing change, worth a quick human pass._
