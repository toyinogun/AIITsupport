# internal/server

## Overview

The small HTTP server both binaries run: `GET /healthz` on each, and the MCP endpoint on the tool server. It owns the server's timeouts, its graceful shutdown, and the bridge that keeps the standard library's error text out of the logs (spec 0010).

## Key files

| File | Owns |
|---|---|
| `server.go` | `NewMux` (a mux with `GET /healthz`, plain text `ok`), `Run` (serve until the context ends, then shut down within 10 s; `WithLimits` sets one listener's timeouts and header cap), and `fixedMessage`, the `http.Server.ErrorLog` bridge |
| `../../cmd/agent/main.go` | runs `Run` on `AGENT_HTTP_ADDR` with the bare mux, inside the agent's errgroup |
| `../../cmd/toolserver/main.go` | adds the MCP handler at `mcpserver.Path` to the mux, then runs it on `TOOLSERVER_HTTP_ADDR`; when the `RESET_*` settings are set, also runs the reset page (`internal/resetpage`) on its own listener at `RESET_HTTP_ADDR`, with `WithLimits` and no MCP route or OpenTelemetry HTTP middleware (spec 0011, AC-16) |

## Conventions

- Callers own the listener: they `Listen` on the address setting themselves, so a bad address fails naming the setting before `Run` starts.
- Add a route to the mux `NewMux` returns; never start a second `http.Server`. `Run` sets `ReadHeaderTimeout` (5 s); keep any new server settings here, not in a `main`.
- `Run` returns nil only after a clean shutdown. A serve error (or a failed shutdown) comes back wrapped, which ends the binary's errgroup and exits 1.
- `http.Server.ErrorLog` writes one fixed `http_server_error` line at error level and drops every attribute (spec 0010, AC-3): the standard library's text can quote a TLS peer or a panic value. Never point `ErrorLog` at the logger directly.
- `/healthz` answers `GET` and `HEAD` only (Go's method pattern); any other method gets 405 with an `Allow` header, and an unknown path gets 404.

## Gotchas

- The `http server listening` and `http server stopped` messages are worded with spaces, unlike the newer snake_case event names. Tests and dashboards may match them, so rename both together if you change them. The `addr` key is on the allow list.
- `Run` shuts down on its own fresh 10 s context, because the one it was given is already done; in flight requests finish inside that window (`TestRunFinishesInFlightRequestOnShutdown`).

## Commands

```bash
go test -race ./internal/server/
```

## Related specs

- [0001 stack and architecture](../../docs/specs/0001-stack-architecture/index.md) (the health server each binary exposes)
- [0010 PII redaction and retention](../../docs/specs/0010-pii-redaction-retention/index.md) (AC-3, the fixed `http_server_error` line)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
