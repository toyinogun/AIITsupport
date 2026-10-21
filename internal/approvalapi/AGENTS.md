# internal/approvalapi

## Overview

The tool server's approval routes (spec 0012, AC-10 to AC-13; spec 0013 adds the group owner decision, the cancel, and the owner fields), mounted on the MCP listener's mux beside `/mcp`, behind the same bearer token, body cap, and trace context extract (`mcpserver.RequireToken`, `CapBody`, `ExtractTrace`). They are plain JSON, never MCP tools, so the model cannot list or call them. Only agent code calls them (`internal/mcpclient`'s `ApprovalClient`), with the Slack id of the person who clicked.

## Key files

| File | Owns |
|---|---|
| `approvalapi.go` | `Handler`, `Options`, and the five routes: `POST /approvals/{id}/decision` (`Recorder.Decide`, then `Dispatcher.Execute` on a moved approve), `POST /approvals/{id}/cancel` (a pending group owner approval whose owner cannot be reached, to `expired`), `GET /approvals/{id}` (the view, with `execution` once executed or failed; `?owner=skip` is the tick's peek, without the live owner reads), `GET /approvals?since=` (views without `execution`, up to 100), `POST /approvals/expire` (`Recorder.Expire`, then `Recorder.Abandon`) |
| `approvalapi_test.go`, `access_test.go` | the routes against the in memory store, a fake Graph, and a fake directory (`access_test.go`: the group owner view, decide, and cancel) |
| `../mcpclient/approvals.go` | the agent's side: `ApprovalsPath`, `ExpirePath`, the status and reason constants, `ApprovalView`, `DecisionResponse` |

## Conventions

- A decision body is exactly `{clicker, approve}` plus an optional `clicker_email` of the right shape, and the clicker must match the `_meta` Slack id format (`^[UW][A-Z0-9]{2,20}$`); anything else is 400 and writes nothing. An unknown id is 404. A group owner approval needs the email: `Dispatcher.ResolveDecider` maps it to an AD account, and no email or no match is refused `not_allowed`.
- A cancel body is exactly `{"reason": "owner_unreachable"}`. Anything but a pending group owner approval is 409 and writes nothing.
- A committed approve always runs its execution on `context.WithoutCancel`, under `APPROVAL_EXECUTE_TIMEOUT`, so a client that goes away never stops a clear halfway. After a committed move, a failed view read still answers 200 with what `Decide` saw; the agent's tick settles the rest.
- Refusal reasons go out as tokens (`not_allowed`, `self`, `expired`, `decided`), mapped from spec 0003's stored reasons.
- Expire and abandon never run a stored call; both moves are guarded SQL, so a retry after a 503 is safe.
- Logs carry `approval_id`, `outcome`, `kind`, and `rows` only.
- `internal/approvalapi` is on both agent deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`).

## Related specs

- [0012 MFA re-registration with approval](../../docs/specs/0012-mfa-reregistration-approval/index.md) (AC-10 to AC-13)
- [0013 Access requests via group owner](../../docs/specs/0013-access-requests-group-owner/index.md) (AC-10, AC-11, AC-13)
- [0003 data model](../../docs/specs/0003-data-model/index.md) (the approvals table and its guarded moves)

_Drafted by /sync from the introducing change, worth a quick human pass._
