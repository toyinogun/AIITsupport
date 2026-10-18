# AI IT Support Engineer (Noord Logistics)

Mock FDE engagement. The plan lives in `docs/scope/scope.md`; start there.

## Build journal

- When something in this project breaks and gets fixed, worked around, or costs real time, use the `journal` skill to log it in `docs/journal/` before you finish the turn. This includes eval failures and timeboxes that were hit.
- A Stop hook (`.claude/hooks/journal-check.py`) asks for this automatically when a turn had tool failures that are not logged yet. If none of them were real incidents, say so in one line and stop.

## Public repo

- This repo is public. Never write a real employer or client name, real personal data, or secrets into any file. Use the fictional Noord Logistics data only.
- `prd.md` is private and gitignored. Do not link to it or copy it into tracked files.

## Stack

- **Language / Runtime**: Go 1.27 (toolchain 1.27.2), one module `github.com/toyinogun/AIITsupport`, binaries `cmd/agent` and `cmd/toolserver`, plus the dev and ops tool `cmd/seed` (`cmd/eval` arrives with feature #20)
- **Key dependencies**: `pgx/v5` + `sqlc` on PostgreSQL 17, `goose` migrations, `log/slog` JSON logs, OpenTelemetry SDK (no op until `OTEL_EXPORTER_OTLP_ENDPOINT` is set), `go-ldap/v3` over LDAPS to Samba AD, `testcontainers-go` for throwaway Postgres and Samba in tests; later `anthropic-sdk-go`, MCP `go-sdk`, `slack-go`
- **Dev tools**: `sqlc` and `goose` are `tool` entries in `go.mod` (`go tool sqlc`, `go tool goose`), nothing to install
- **Lint, hook, and scan tools**: `golangci-lint` v2 (also runs gofumpt and goimports), `lefthook`, `gitleaks`, and `govulncheck` are `tool` entries in a separate `tools.mod` (`go tool -modfile=tools.mod <tool>`). Its `go` and `toolchain` lines must match `go.mod`. Dependabot can't see it, so bump these by hand. Decision: [docs/specs/0002-coding-standards-tooling/index.md](docs/specs/0002-coding-standards-tooling/index.md)
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
make hooks     # install the git hook shims (lefthook via go tool); run once per clone
make lint      # golangci-lint run (linters plus a format check)
make fmt       # format every Go file (gofumpt + goimports); fmt-check only reports
make check     # everything CI runs except the image builds: lint, gen-check, tidy-check, vuln, secrets, test
make gen-check # fail if the sqlc output is out of date
make tidy-check # fail if go.mod or tools.mod needs go mod tidy
make vuln      # govulncheck on the app code
make secrets   # gitleaks over the full git history
make directory-reset # fresh Samba AD container, seeded from seed/directory/noord.yaml (Postgres untouched)
make seed      # seed the running directory; safe to rerun, says "already seeded" or asks for a reset
make directory-login NAME=<username> # bind as a seeded user and show the lock state
make directory-lock NAME=<username>  # lock a seeded user by failed binds
make certs     # dev CA and Samba server cert in .dev/certs (keeps existing files)
TEST_SAMBA=1 make test # also runs the live Samba directory tests (needs Docker)
```

Hooks: pre commit runs a format check on staged Go files, lint, and gitleaks on the staged changes; pre push runs `go test -race` and `gen-check`. A Claude Code PostToolUse hook (`.claude/hooks/format-go.py`) formats each Go file after an edit.

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
- The credential boundary is checked twice: the depguard rule `agent-credential-boundary` in `.golangci.yml` and `cmd/agent/boundary_test.go`. A new client system package goes on both deny lists.
- Only `cmd/seed` binds as the domain Administrator (`SEED_*` settings); `internal/seed` is on both deny lists, so the agent can never import it. See [internal/seed/AGENTS.md](internal/seed/AGENTS.md).
- Log with `log/slog` only; the standard `log` package is denied (depguard `no-std-log`). slog messages are static strings with snake_case keys (sloglint).
- Silence a real false positive with `//nolint:<linter> // <reason>` on the narrowest line. A wider exclusion goes in `.golangci.yml` with a comment saying why.
- CI (`.github/workflows/ci.yml`) has one required check, `ci-ok`. Actions are pinned by full commit SHA. The Postgres image tag in `compose.yaml` and the workflow must match.

## Agent skills

- [golang-error-handling](.claude/skills/golang-error-handling/): `samber/cc-skills-golang`, error creation, wrapping, and handling
- [golang-security](.claude/skills/golang-security/): `samber/cc-skills-golang`, secrets, injection, network, threat modeling
- [golang-database](.claude/skills/golang-database/): `samber/cc-skills-golang`, pgx scanning, transactions, DB testing
- [golang-concurrency](.claude/skills/golang-concurrency/): `samber/cc-skills-golang`, goroutines, channels, sync primitives
- [opentelemetry](.claude/skills/opentelemetry/), [tempo](.claude/skills/tempo/), [grafana-oss](.claude/skills/grafana-oss/): `grafana/skills`, tracing and dashboards (feature #18 on)
- [journal](.claude/skills/journal/): project local, writes `docs/journal/` entries
- [testcontainers-go](.agents/skills/testcontainers-go/): `testcontainers/claude-skills`, throwaway containers in Go tests (the live Samba tests); linked from `.claude/skills/`

Declined: Agent Skills and MCP servers for `golangci-lint`, `lefthook`, `gitleaks`, `govulncheck` (2026-10-17, spec 0002)

Declined: ActiveDirectoryMCP (it would hold admin credentials outside the tool server; spec 0004)

MCP servers: `grafana/mcp-grafana` (recommended, feature #18 on), a community Zammad MCP (recommended, review first, feature #8 on)

## Context files

- [internal/config/AGENTS.md](internal/config/AGENTS.md) (how settings are declared, validated, and kept in sync with `.env.example`)
- [db/AGENTS.md](db/AGENTS.md) (goose migrations, sqlc queries, and the generated code)
- [internal/audit/AGENTS.md](internal/audit/AGENTS.md): the audit recorder and approvals, the only writer of `audit_log` and `approvals`
- [internal/seed/AGENTS.md](internal/seed/AGENTS.md): the Noord directory seeder (`cmd/seed`, `seed/directory/`), its marker, locks, and live tests

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
