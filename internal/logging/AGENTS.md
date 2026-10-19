# internal/logging

## Overview

The JSON slog logger both binaries write to stdout, and the one allow list that keeps personal data out of logs and spans (spec 0010). A value prints only under a listed key, outside any group, as a short plain value; everything else keeps its key and prints `[redacted]`. `internal/telemetry` applies the same list to exported spans.

## Key files

| File | Owns |
|---|---|
| `logging.go` | `New`, the unexported `allowed` list behind `Allowed` and `AllowedKeys`, `Plain` (the value rules), `Redacted`, and `redactingHandler` |
| `scan_test.go` | the typed source scan (`golang.org/x/tools/go/packages`): every slog key and span attribute key in the runtime code is a string literal on the list, and every span name (`Start`, `SetName`) is a literal |
| `testdata/scanfixture/` | the calls the scan must reject (each with a `// want` comment) and the ones it must accept |
| `../canary/` | the canary test helper: it loads every person in `seed/directory/noord.yaml` and hunts for them in log and span string values (`TestCanaryUnlockAndRefusal` in `internal/agent` and `internal/mcpserver`) |

## Conventions

- **Add a log key** with one reviewed line in `allowed`. It may only ever hold an id that is not a person's (a Slack channel or event id is fine; a Slack user id is not), a count, a flag, or a fixed token. The scan fails CI, naming the file and line, until the key is listed.
- **Plain values** are a string, int, uint, float, bool, or duration; a string must be at most 128 bytes with no whitespace and no `@`. An `error`, a struct, a two word name, or an email under a listed key still prints `[redacted]`.
- Redaction happens in `redactingHandler`, not `ReplaceAttr`, so only the record's own `time`, `level`, and `msg` pass as is. An attribute keyed `msg`, `time`, or `level` is redacted like any unlisted key, and everything after `WithGroup` (or inside a `slog.Group`) is redacted whole.
- `internal/logging` offers no unredacted constructor. `cmd/seed` builds its own plain JSON logger (`cmd/seed/logger.go`), because an operator tool's user lines are how you debug a sync.
- **Never log error text** (`err.Error()`): log `cause`, a fixed token from `cause.Of` (`internal/cause`) or the agent's `cause()`.
- No SDK has a logger wired: slack-go and Socket Mode, the MCP Go SDK (`mcp.NewServer(..., nil)`), the Anthropic SDK, and pgx all run without one, so nothing bypasses the handler. Keep it that way, or route an SDK logger through `New`.
- A `[redacted]` value in production logs means a key is missing from the list or a value broke the rules: a bug to fix, not a leak.

## Gotchas

- The scan skips `internal/seed` and `internal/db` (the seeders log user lines on purpose; the sqlc output logs nothing), and it skips attribute keys in `internal/telemetry`, where the `service.name` resource attribute is built.
- The scan cannot see a key it cannot resolve: a `*slog.Logger` method value, or a key built with `slog.Record.Add`. The handler still redacts the value at run time.
- A short single token under a listed key still passes (a username logged under `reason`, say). The scan, the canaries, and review catch that, not the handler.

## Commands

```bash
go test -race ./internal/logging/ ./internal/canary/            # handler, scan, and canary helper
go test -race -run TestCanary ./internal/agent/ ./internal/mcpserver/   # the canaries over unlock and refusal
```

## Related specs

- [0010 PII redaction and retention](../../docs/specs/0010-pii-redaction-retention/index.md) (AC-1, AC-2, AC-5, with [verify.md](../../docs/specs/0010-pii-redaction-retention/verify.md))

_Drafted by /develop from the introducing change, worth a quick human pass._
