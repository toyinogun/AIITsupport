# internal/telemetry

## Overview

OpenTelemetry tracing setup. With no `OTEL_EXPORTER_OTLP_ENDPOINT` it leaves the no op provider in place; with one, spans go out over OTLP HTTP through the redacting exporter, which applies `internal/logging`'s allow list to every span before it leaves the process (spec 0010).

## Key files

| File | Owns |
|---|---|
| `telemetry.go` | `Setup(ctx, service, endpoint, logger)`, the throttled OpenTelemetry error handler (`telemetry_export_failed`) |
| `redact.go` | `NewRedactingExporter`: listed attributes with plain values pass, others print `[redacted]`; no events; an empty status description (the code stays); links and the instrumentation scope with no attributes |

## Conventions

- **Span attribute keys** follow the log allow list (`logging.Allowed`); the source scan in `internal/logging` checks every `attribute` constructor key and every span name outside this package.
- **Span names are fixed strings** (`agent.turn`, `llm.call`, `tool.call`): the exporter passes `Name()` as is, so never build one from data, in `Start` or `SetName`.
- Put a failure's reason in a `cause` attribute, never in the status description or `RecordError`: the exporter blanks the first and drops the second.
- The exporter builds new slices and never changes the span it was given. The resource (`service.name`) passes unchanged.
- `Setup` always installs the error handler, even with no endpoint. It logs only `cause`, at most once a minute (`errorEvery`).

## Gotchas

- The global tracer provider is first set wins for tracers taken at init (`internal/agent`'s package level tracer). Tests install one provider per test binary (see `traceSinks` in `internal/agent/hygiene_test.go`), and restore the global error handler after a test that calls `Setup`.
- Traces are checked in tests only until a Collector exists (#18), which adds its own redaction processor as a second layer.

## Commands

```bash
go test -race ./internal/telemetry/
```

## Related specs

- [0010 PII redaction and retention](../../docs/specs/0010-pii-redaction-retention/index.md) (AC-4)
- [0001 stack and architecture](../../docs/specs/0001-stack-architecture/index.md) (OpenTelemetry, no op until the endpoint is set)

_Drafted by /develop from the introducing change, worth a quick human pass._
