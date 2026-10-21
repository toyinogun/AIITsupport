# AI IT Support Engineer (Noord Logistics)

Mock FDE engagement. The plan lives in `docs/scope/scope.md`; start there.

## Build journal

- When something in this project breaks and gets fixed, worked around, or costs real time, use the `journal` skill to log it in `docs/journal/` before you finish the turn. This includes eval failures and timeboxes that were hit.
- A Stop hook (`.claude/hooks/journal-check.py`) asks for this automatically when a turn had tool failures that are not logged yet. If none of them were real incidents, say so in one line and stop.

## Public repo

- This repo is public. Never write a real employer or client name, real personal data, or secrets into any file. Use the fictional Noord Logistics data only.
- `prd.md` is private and gitignored. Do not link to it or copy it into tracked files.

## Stack

- **Language / Runtime**: Go 1.27 (toolchain 1.27.3), one module `github.com/toyinogun/AIITsupport`, binaries `cmd/agent` and `cmd/toolserver`, plus the dev and ops tool `cmd/seed` (`cmd/eval` arrives with feature #20)
- **Key dependencies**: `pgx/v5` + `sqlc` on PostgreSQL 17, `goose` migrations, `log/slog` JSON logs, OpenTelemetry SDK (no op until `OTEL_EXPORTER_OTLP_ENDPOINT` is set), `go-ldap/v3` over LDAPS to Samba AD, `azidentity` client credentials for a thin Microsoft Graph client (Entra), `testcontainers-go` for throwaway Postgres and Samba in tests, `slack-go` (Socket Mode) for the agent's Slack strand, `goldmark` for KB article Markdown, MCP `go-sdk` (the tool server and the agent's client, streamable HTTP, stateless), `anthropic-sdk-go` (the agent's model client, `internal/llm`)
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
make entra-sync [ONLY=<username>] # make the Entra tenant match SYNC-Entra-Users (skips when SEED_GRAPH_* is unset)
make entra-mfa NAME=<username> [CLEAR=1] # list (or clear) a user's MFA methods with the tool server app
make noord-reset # directory-reset, entra-sync, then helpdesk-reset (not atomic)
make helpdesk-up    # start Zammad in Compose (profile helpdesk, http://127.0.0.1:58080) and wait until healthy
make helpdesk-seed  # seed Zammad from seed/helpdesk/; says "already seeded" or asks for a reset
make helpdesk-reset # fresh Zammad containers and volumes, then seed (Samba and pgdata untouched)
TEST_ZAMMAD=1 go test -race -run Live ./internal/seed/helpdesk/ # live helpdesk tests after helpdesk-reset, .env loaded
make install CLUSTER=<name> # seal, commit and push the cluster folder, install or update it through ArgoCD (spec 0015; exit 2: nothing changed)
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
- `internal/install` (the cluster installer, only `cmd/seed` uses it) is on both deny lists too. See [internal/install/AGENTS.md](internal/install/AGENTS.md).
- `internal/zammad` never writes a Zammad user, organization, or user token: its transport refuses them (`ErrUserWrite`), because Zammad lets any agent token do it. See [internal/zammad/AGENTS.md](internal/zammad/AGENTS.md).
- Entra has two app registrations: only `cmd/seed` holds the seed app (`SEED_GRAPH_*`, user writes); the tool server app (`GRAPH_*`) reads users and changes phone methods only. `internal/graph` refuses any user without the `noord-sync` marker (`ErrNotManaged`). See [internal/graph/AGENTS.md](internal/graph/AGENTS.md).
- Log with `log/slog` only; the standard `log` package is denied (depguard `no-std-log`). slog messages are static strings with snake_case keys (sloglint).
- Every log and span key must be on `internal/logging`'s allow list (the source scan fails CI otherwise); any other value prints `[redacted]`. Never log error text: log `cause` from `cause.Of`. See [internal/logging/AGENTS.md](internal/logging/AGENTS.md) (spec 0010).
- Silence a real false positive with `//nolint:<linter> // <reason>` on the narrowest line. A wider exclusion goes in `.golangci.yml` with a comment saying why.
- CI (`.github/workflows/ci.yml`) has one required check, `ci-ok`. Actions are pinned by full commit SHA. The Postgres image tag in `compose.yaml` and the workflow must match. Jobs run on ephemeral ARC runners in the k3sprox cluster (label `k3sprox`, amd64, one fresh pod with its own dockerd per job); only a push to `main` or a PR whose head is this repo runs there, anything else (a fork, or a deleted one) runs on `ubuntu-latest`, so outside code never reaches the cluster. A new job copies the same `runs-on` expression. See [docs/runbooks/ci-runner.md](docs/runbooks/ci-runner.md).

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

Declined: Agent Skills for Microsoft Graph and Entra (`merill/msgraph`, `microsoft/azure-skills` entra-app-registration, `github/awesome-copilot` msgraph-sdk; none covers the Azure SDK for Go) (2026-10-18, spec 0005)

Declined: Microsoft Graph and Entra MCP servers (Microsoft's EnterpriseMCP is delegated only and needs admin provisioning rights; community ones would hold a client secret outside the tool server; spec 0005)

MCP servers: `grafana/mcp-grafana` (recommended, feature #18 on)

Declined: Agent Skills for Slack and Zammad (none covers `slack-go` or Zammad's REST API; the Slack ones target Bolt for Python/JS) (2026-10-18, feature #8)

Declined: Slack and Zammad MCP servers (Slack's official one needs user OAuth tokens; the community Zammad ones would hold a Zammad token outside the tool server) (2026-10-18, feature #8)

Declined: Agent Skills for the MCP Go SDK (`victorzhuk/skills` z-go-mcp-server, `doc-scout/mcp-server` mcp-builder-go; none official, and `mcp-server-patterns` already covers the generic part; use the `go-sdk` examples and pkg.go.dev) (2026-10-18, feature #9)

## Context files

- [internal/config/AGENTS.md](internal/config/AGENTS.md) (how settings are declared, validated, and kept in sync with `.env.example`)
- [db/AGENTS.md](db/AGENTS.md) (goose migrations, sqlc queries, and the generated code)
- [internal/audit/AGENTS.md](internal/audit/AGENTS.md): the audit recorder and approvals, the only writer of `audit_log` and `approvals`
- [internal/seed/AGENTS.md](internal/seed/AGENTS.md): the Noord directory and helpdesk seeders (`cmd/seed`, `seed/directory/`, `seed/helpdesk/`), their markers, locks, and live tests
- [internal/graph/AGENTS.md](internal/graph/AGENTS.md): the thin Microsoft Graph client, its managed marker fence, retries, and the fake Graph
- [internal/zammad/AGENTS.md](internal/zammad/AGENTS.md): the thin Zammad REST client, its user write guard, and the fake Zammad
- [internal/slack/AGENTS.md](internal/slack/AGENTS.md): the agent's Socket Mode listener, event dedup, and the hand off to the agent
- [internal/agent/AGENTS.md](internal/agent/AGENTS.md): the agent loop (spec 0008): the code checked requester, the bounded model loop, the failure path, workers, and the restart sweep
- [internal/tools/AGENTS.md](internal/tools/AGENTS.md): the tool layer (registry, risk tiers, the dispatcher pipeline, kill switch, fence), plus `internal/mcpserver` and `internal/mcpclient`
- [internal/ldap/AGENTS.md](internal/ldap/AGENTS.md): the one LDAPS layer (dialer, DN helpers, lock state, the tool server's directory reads and its one write)
- [internal/logging/AGENTS.md](internal/logging/AGENTS.md): the redacting slog logger, the one allow list for log and span keys, the source scan, and the canary helper (spec 0010)
- [internal/telemetry/AGENTS.md](internal/telemetry/AGENTS.md): OpenTelemetry setup, the redacting span exporter, and the throttled export error handler
- [internal/cause/AGENTS.md](internal/cause/AGENTS.md): `cause.Of`, the fixed failure tokens logged in place of error text
- [internal/server/AGENTS.md](internal/server/AGENTS.md): the health and MCP HTTP server both binaries run, its shutdown, and the fixed `http_server_error` line
- [internal/resetlink/AGENTS.md](internal/resetlink/AGENTS.md): the password reset link store (spec 0011): token, hash, issue, claim, revert, and the hourly cleanup
- [internal/resetpage/AGENTS.md](internal/resetpage/AGENTS.md): the password reset page on its own listener (spec 0011): the form, the audited POST as `svc-pwreset`, the fixed pages and headers
- [internal/approvalapi/AGENTS.md](internal/approvalapi/AGENTS.md): the tool server approval routes (specs 0012, 0013): decide and execute, the owner unreachable cancel, the views, expire and abandon, never MCP tools
- [internal/install/AGENTS.md](internal/install/AGENTS.md): the one command cluster installer (spec 0015): its steps, the shared Secret table, sealing, the commit and push rules, exit codes
- [deploy/AGENTS.md](deploy/AGENTS.md): what ArgoCD installs (spec 0015): the bootstrap chart, our chart's components, the cluster folders, the vendored Zammad chart, waves, hooks, and seed Job hashing

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
