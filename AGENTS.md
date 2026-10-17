# AI IT Support Engineer (Noord Logistics)

Mock FDE engagement. The plan lives in `docs/scope/scope.md`; start there.

## Build journal

- When something in this project breaks and gets fixed, worked around, or costs real time, use the `journal` skill to log it in `docs/journal/` before you finish the turn. This includes eval failures and timeboxes that were hit.
- A Stop hook (`.claude/hooks/journal-check.py`) asks for this automatically when a turn had tool failures that are not logged yet. If none of them were real incidents, say so in one line and stop.

## Public repo

- This repo is public. Never write a real employer or client name, real personal data, or secrets into any file. Use the fictional Noord Logistics data only.
- `prd.md` is private and gitignored. Do not link to it or copy it into tracked files.

## Stack

- **Language / Runtime**: Go 1.26 (toolchain 1.26.5), one module `github.com/toyinogun/AIITsupport`, binaries `cmd/agent` and `cmd/toolserver` (`cmd/eval` arrives with feature #20)
- **Key dependencies**: `pgx/v5` + `sqlc` on PostgreSQL 17, `goose` migrations, `log/slog` JSON logs, OpenTelemetry SDK (no op until `OTEL_EXPORTER_OTLP_ENDPOINT` is set); later `anthropic-sdk-go`, MCP `go-sdk`, `slack-go`, `go-ldap`
- **Dev tools**: `sqlc` and `goose` are `tool` entries in `go.mod` (`go tool sqlc`, `go tool goose`), nothing to install
- **Platform**: Docker Compose locally; k3s + ArgoCD + CloudNativePG in production
- Full decision and rules: [docs/specs/0001-stack-architecture/index.md](docs/specs/0001-stack-architecture/index.md)

## Build approach

**Tracer Bullet**: prove the whole pipe (Slack to agent to tool server to AD to ticket to audit log) on one real ticket first, then thicken one strand at a time.

## Commands

```bash
make dev       # Postgres in Compose, goose migrations, then both binaries (creates .env from .env.example)
make test      # go test -race ./...  (DB tests run only when TEST_DATABASE_URL is set)
make vet       # go vet ./...
make gen       # regenerate sqlc code; must leave no diff
make build     # both binaries into bin/
make docker    # both images from the one Dockerfile (--build-arg CMD=agent|toolserver)
make db-up     # Postgres only, on 127.0.0.1:55432
```

## Specs

Stored in `docs/specs/`. Format: `docs/specs/NNNN-title/index.md` (plus `rationale.md`, `verify.md`).

## Rules

- The tool server is the only process that holds client system credentials (AD, Entra, Zammad). The agent never receives them.
- The requester's identity comes from MCP `_meta` set by agent code, never from the model or a tool argument. Approvals go around the model.
- No audit row, no write. Durable state lives in Postgres, never in memory.
- Every setting is declared once in `internal/config`; see [internal/config/AGENTS.md](internal/config/AGENTS.md).
- Wrap errors with context (`fmt.Errorf("reach database: %w", err)`); `main` calls `run()` and exits 1 with `<service>: <error>`.
- Never hand edit `internal/db/queries/` (sqlc output); change `db/queries/*.sql` and run `make gen`.
- Tests use the standard library `testing` package, live next to the code (`*_test.go`), and must pass with `-race`.
- Never hardcode model ids, prices, or limits; they are config values or `eval/pricing.yaml`.

## Agent skills

- [golang-error-handling](.claude/skills/golang-error-handling/): `samber/cc-skills-golang`, error creation, wrapping, and handling
- [golang-security](.claude/skills/golang-security/): `samber/cc-skills-golang`, secrets, injection, network, threat modeling
- [golang-database](.claude/skills/golang-database/): `samber/cc-skills-golang`, pgx scanning, transactions, DB testing
- [golang-concurrency](.claude/skills/golang-concurrency/): `samber/cc-skills-golang`, goroutines, channels, sync primitives
- [opentelemetry](.claude/skills/opentelemetry/), [tempo](.claude/skills/tempo/), [grafana-oss](.claude/skills/grafana-oss/): `grafana/skills`, tracing and dashboards (feature #18 on)
- [journal](.claude/skills/journal/): project local, writes `docs/journal/` entries

MCP servers: `grafana/mcp-grafana` (recommended, feature #18 on), a community Zammad MCP (recommended, review first, feature #8 on)

## Context files

- [internal/config/AGENTS.md](internal/config/AGENTS.md) (how settings are declared, validated, and kept in sync with `.env.example`)
- [db/AGENTS.md](db/AGENTS.md) (goose migrations, sqlc queries, and the generated code)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
