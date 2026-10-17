# 0002. Lint, format, hooks, and CI checks for the Go monolith

**Date**: 2026-10-17
**Status**: Accepted

## Summary

This spec picks the tools that keep the code consistent and safe before it reaches the public repo. golangci-lint v2 is the one linter, and it also runs the formatters (gofumpt and goimports). lefthook runs fast checks on every commit and the tests on every push. gitleaks blocks secrets. One GitHub Actions workflow runs the same checks on pushes to main and on every pull request, and main only accepts green builds. Every tool version is pinned in one `tools.mod` file, so your laptop and CI always run the same thing.

## Rationale

The context, the options weighed, and why this one won: see [rationale.md](rationale.md).

## Decision

**Chosen option**: Option 1: golangci-lint v2, lefthook, gitleaks, tools pinned in `tools.mod`, enforced now on the whole repo

One golangci-lint v2 config runs the linters and the formatters (gofumpt plus goimports), lefthook runs the fast checks on commit and the tests on push, gitleaks catches secrets locally and in CI, and one GitHub Actions workflow must be green before anything merges to main.

## Standard definition

**Canonical pattern** (what every Go file looks like, and what the tools enforce):

```go
package zammad // internal/zammad: only the tool server may import this

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/toyinogun/AIITsupport/internal/config"
)

// Ticket fetches one ticket by id.
func (c *Client) Ticket(ctx context.Context, id int64) (Ticket, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(id), nil) // noctx: every request carries ctx
	if err != nil {
		return Ticket{}, fmt.Errorf("build ticket request: %w", err) // errorlint: wrap with %w
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Ticket{}, fmt.Errorf("get ticket %d: %w", id, err)
	}
	defer resp.Body.Close() // bodyclose; allowed by the std-error-handling preset
	c.log.InfoContext(ctx, "ticket fetched", "ticket_id", id, "status", resp.StatusCode) // sloglint: static msg, snake_case keys
	// ...
}
```

Imports are in three groups (standard library, third party, this module), and the file is gofumpt formatted. Exported names carry doc comments (revive's defaults). All output goes through `slog`. Listeners take a context too: `(&net.ListenConfig{}).Listen(ctx, "tcp", addr)`, not `net.Listen`.

**Replaces** (now wrong, and caught by a tool):
- `fmt.Print*`, `print`, and `println` anywhere, and the old `log` package anywhere. Use `log/slog`. Writing to a writer with `fmt.Fprint*` stays allowed: `fmt.Fprintf(os.Stderr, ...)` in `main` for the `<service>: <error>` exit line, or `fmt.Fprintln(w, ...)` to an `http.ResponseWriter`. forbidigo doesn't police `Fprint*` to stderr outside `main`; review does.
- Any import of the client system packages (listed under depguard below) from `cmd/agent`, or from a package that `cmd/agent` pulls in.
- Error checks with `==` or a type assertion, and wrapping with `%v`. Use `errors.Is`, `errors.As`, and `%w`.
- An HTTP request or a listener without a context, or an unclosed HTTP response body.
- slog calls with dynamic messages, mixed attr and key value args, or keys that are not `snake_case`.
- A bare `//nolint` or `//nolint:all`, or one with no reason.
- Hand formatted code that gofumpt would change, or imports out of their groups.

**Tool versions.** Each tool in `tools.mod` is pinned at the newest release whose own `go` directive is at most the app's (`go 1.26.0` today). After any `go get -tool -modfile=tools.mod`, the `go 1.26.0` and `toolchain go1.26.5` lines in `tools.mod` must still match `go.mod`. If a newer tool release asks for a newer Go, stay on the older release. At the time of writing, that rule means lefthook v2.1.x (v2.2.2 needs Go 1.27). The others' latest releases fit.

**Enforcement** (each file and what it holds):

| File | What it holds |
|---|---|
| `tools.mod`, `tools.sum` | `tool` entries for `github.com/golangci/golangci-lint/v2/cmd/golangci-lint`, `github.com/evilmartians/lefthook/v2`, `github.com/zricethezav/gitleaks/v8` (not `gitleaks/gitleaks`, which declares the zricethezav path), and `golang.org/x/vuln/cmd/govulncheck`, at versions chosen by the version rule above. The module line, `go` line, and `toolchain` line match `go.mod`. `go mod tidy -modfile=tools.mod` also copies the app's own requirements into `tools.mod`; that's expected, and the committed file is the tidied one. sqlc and goose stay in `go.mod`, where spec 0001 put them. |
| `.golangci.yml` | `version: "2"`. `linters.default: standard` (errcheck, govet, ineffassign, staticcheck, unused), plus: gosec, errorlint, nilerr, errname, bodyclose, noctx, sloglint, misspell, revive (its default rule set, so exported names need doc comments), gocritic (the `diagnostic` tag only), depguard, forbidigo, nolintlint. `formatters`: gofumpt, goimports with `local-prefixes: github.com/toyinogun/AIITsupport`. `exclusions.generated: lax`, so the sqlc output in `internal/db/queries/` is skipped. `exclusions.presets: [std-error-handling]`, so `defer x.Close()` and similar standard calls are not errcheck findings. One commented `exclusions.rules` entry for `_test.go` files that turns off noctx and gosec G101 (hard coded test credentials) there. Tests are otherwise linted like everything else. |
| ↳ depguard | Rule `agent-credential-boundary`, with `files: ["**/cmd/agent/**"]`, denying `github.com/toyinogun/AIITsupport/internal/ldap`, `github.com/toyinogun/AIITsupport/internal/graph`, `github.com/toyinogun/AIITsupport/internal/zammad`, `github.com/go-ldap/ldap` (a prefix, so it covers `/v3`), and `github.com/Azure/azure-sdk-for-go/sdk/azidentity` (the Entra credential library). The message points at the AGENTS.md rule. Rule `no-std-log` on all files, denying `pkg: "log$"` (the `$` makes it exact, so `log/slog` stays allowed). The internal paths come from the spec 0001 layout. A later feature that adds a client package under another name adds it to both this rule and the test's list. |
| ↳ forbidigo | `^fmt\.Print.*$`, `^print$`, `^println$`. |
| ↳ sloglint | `kv-only: true`, `no-mixed-args: true`, `static-msg: true`, `key-naming-case: snake`. |
| ↳ nolintlint | `require-specific: true`, `require-explanation: true`, `allow-unused: false`. |
| `cmd/agent/boundary_test.go` | Holds the denied import prefixes in one package level variable (the same list as the depguard rule). It runs `go list -deps .` (the test's working directory is `cmd/agent`) and fails if any listed package starts with a denied prefix. A second test case runs the same check against a stub package written into `t.TempDir()` that imports a denied path, so the test proves it can fail even before the real client packages exist. |
| `lefthook.yml` | `no_auto_install: true`, so lefthook never overwrites the shims below. `pre-commit` (commands run in parallel): `fmt` runs `go tool -modfile=tools.mod golangci-lint fmt --diff {staged_files}` on staged `*.go` files, `lint` runs `go tool -modfile=tools.mod golangci-lint run` with `glob: "**/*.go"`, so a docs only commit skips it, and `secrets` runs `go tool -modfile=tools.mod gitleaks git --staged --config .gitleaks.toml`. `pre-push`: `go test -race ./...` (DB tests skip locally unless `TEST_DATABASE_URL` is set) and `make gen-check`. |
| `.git/hooks/pre-commit`, `.git/hooks/pre-push` | Written by `make hooks`, not committed. Each is a two line POSIX `sh` shim that runs `exec go tool -modfile=tools.mod lefthook run <hook> "$@"` from the repo root. This works with no lefthook on PATH. |
| `.gitleaks.toml` | `[extend] useDefault = true`, plus a path allowlist for `.claude/skills/**` (vendored skill docs with example keys; today `.claude/skills/golang-security/references/secrets.md` trips `generic-api-key` twice). Use whichever allowlist table form (`[allowlist]` or `[[allowlists]]`) the pinned gitleaks accepts. `.env.example` holds only placeholders, so a finding there is a real problem to fix, never something to allowlist. Every gitleaks call passes `--config .gitleaks.toml`. |
| `.github/workflows/ci.yml` | See *CI checks* below. |
| `.github/dependabot.yml` | Weekly updates, with minor and patch versions grouped into one PR per ecosystem, for `gomod` (`/`, the app `go.mod` only), `github-actions`, and `docker` (the `Dockerfile` base images). |
| `.claude/settings.json` + `.claude/hooks/format-go.py` | A PostToolUse hook with matcher `Edit\|Write\|MultiEdit`, command `python3 "$CLAUDE_PROJECT_DIR/.claude/hooks/format-go.py"`, and `timeout: 300` (a cold tools build is slow). The script reads the hook JSON from stdin and takes `tool_input.file_path`. It skips anything that isn't a `.go` file under the project directory, and anything in `internal/db/queries/`. Otherwise it runs `go tool -modfile=tools.mod golangci-lint fmt <path>` from `$CLAUDE_PROJECT_DIR`. It never blocks the agent: any failure is printed to stderr, and the script exits 0. Same Python style as `journal-check.py`. |
| `Makefile` | New targets: `lint` (`golangci-lint run`), `fmt` (`golangci-lint fmt`), `fmt-check` (`golangci-lint fmt --diff`, which exits 1 on any diff), `gen-check` (runs `make gen`, then `git diff --exit-code -- internal/db/queries`, and fails if `git ls-files --others --exclude-standard internal/db/queries` lists anything), `tidy-check` (`go mod tidy -diff` and `go mod tidy -diff -modfile=tools.mod`), `vuln` (`govulncheck ./...`), `secrets` (`gitleaks git --config .gitleaks.toml`, the full history), `hooks` (writes the two shims above and makes them executable), and `check` (lint, gen-check, tidy-check, vuln, secrets, test: everything CI runs except the image builds). Every tool runs through `go tool -modfile=tools.mod`. The existing `vet` stays as a quick standalone target. |

**CI checks** (`.github/workflows/ci.yml`, one workflow, all must pass):

- **Triggers**: `push` to `main`, and `pull_request`. `concurrency.group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}` with `cancel-in-progress: true`. Top level `permissions: contents: read`. No secrets are used, so fork PRs run safely.
- **Go setup**: `actions/setup-go` with `go-version-file: go.mod`, and its built in cache off (`cache: false`). Each job instead caches `~/go/pkg/mod` and the Go build cache with `actions/cache`, under a key made of the job id plus the hashes of `go.sum` and `tools.sum`, so each job keeps its own tool builds. The first step in each job prints `go version`, and the job fails if it isn't `go1.26.5` (the `toolchain` line).
- **Action pins**: `actions/checkout`, `actions/setup-go`, `actions/cache`, `actions/upload-artifact`, `docker/setup-buildx-action`, and `docker/build-push-action`, each pinned to a full commit SHA (looked up at build time with `gh api repos/<owner>/<repo>/git/ref/tags/<tag>`), with the version in a trailing comment. Dependabot's `github-actions` updates keep both current.
- **Jobs**, each with a `timeout-minutes`:
  - `lint`: `make lint` (it already runs the formatters and fails on a format diff), plus `go tool -modfile=tools.mod lefthook validate`.
  - `test`: a `postgres:17.6-alpine` service container with `POSTGRES_USER=noord`, `POSTGRES_PASSWORD=noord-dev`, `POSTGRES_DB=noord`, `ports: ["5432:5432"]`, and a `pg_isready` health check. Job env sets `TEST_DATABASE_URL=postgres://noord:noord-dev@localhost:5432/noord?sslmode=disable`. A step fails if this image tag differs from the one in `compose.yaml`. Then the job runs `go tool goose -dir db/migrations postgres "$TEST_DATABASE_URL" up` and `go test -race -coverprofile=coverage.out ./...`, so the DB tests run instead of skipping. The total from `go tool cover -func` goes to the job summary and `coverage.out` is uploaded as an artifact. Coverage is reported only and never fails the job.
  - `generated`: `make gen-check` and `make tidy-check`.
  - `vuln`: `make vuln`.
  - `secrets`: checkout with `fetch-depth: 0`, then `make secrets` over the full history.
  - `images`: `docker/setup-buildx-action`, then a matrix over `CMD` = `agent`, `toolserver`. Each builds with `docker/build-push-action` for `linux/amd64,linux/arm64`, with `build-args: CMD=<cmd>` and `push: false`. The Dockerfile already cross compiles from `$BUILDPLATFORM`, so no QEMU is needed. Pushing to GHCR is feature #17's job.
  - `ci-ok`: `needs: [lint, test, generated, vuln, secrets, images]`, with `if: always()`. It fails unless every needed job succeeded. This is the one required check name.
- **Branch protection** (a GitHub setting, done by hand): a ruleset on `main` that requires `ci-ok`, with you (the repo owner) on the bypass list so a docs only commit can still go straight to main. Pushes to main still run the full workflow.

**Rollout**: new code immediately, on the whole repo, in the same change that adds the tooling. `/develop tooling` fixes the first run's findings in that change: switching `cmd/*/main.go` to `net.ListenConfig`, using `slog.DiscardHandler` in tests, and anything else the trial run did not cover. There is no baseline file and no `--new-from-rev`.

**Exceptions**:
- Generated sqlc code in `internal/db/queries/` is excluded from linting and formatting (never hand edit it, per `AGENTS.md`).
- In `_test.go` files only, noctx and gosec G101 are off (one commented rule in `.golangci.yml`).
- A real false positive may use `//nolint:<linter> // <reason>` on the narrowest line. A whole file or whole linter exclusion goes in `.golangci.yml` with a comment saying why.
- `fmt.Fprint*` to a writer is allowed (see *Replaces*).
- `.claude/skills/**` is allowlisted in gitleaks (vendored docs with example keys).
- No other exceptions.

**Done means** (what `/check verify` and `/test` check for feature #4):
1. On a clean clone with only Go installed (lefthook and golangci-lint removed from PATH), `make hooks` writes the shims, a commit runs the pre commit hook, and `make check` passes.
2. Adding `fmt.Println` to any package fails `make lint`. The boundary test's stub case fails on a denied import, and the real case passes for `cmd/agent` today. depguard flags a direct denied import in `cmd/agent` once such a package exists, and verification proves this with a throwaway stub package that isn't committed.
3. A commit that stages a file with a fake but valid looking token (for example a Slack bot token pattern) is blocked by the pre commit hook.
4. A Go file with bad formatting is reformatted by the Claude Code hook after an edit, and a commit of a badly formatted file is blocked by the pre commit hook.
5. A pull request with the tooling change runs all seven CI jobs, and `ci-ok` is green. The `test` job runs the DB tests, not skips them. You verify this after pushing; `/develop` doesn't push.
6. `AGENTS.md` (via `/sync`) lists the new make targets and the tooling rules.

## Consequences

**Positive**:
- The credential boundary, the slog only rule, and the no secrets rule are now checks, not promises. Each is caught before a commit and again in CI.
- One version of every tool, everywhere, with nothing to install but Go.
- Every later feature inherits a working CI that the eval (feature #20) can join as one more job behind `ci-ok`.

**Negative / tradeoffs**:
- The first `go tool -modfile=tools.mod` run builds four tools from source, which takes a few minutes on a fresh clone or a cold CI cache. The Claude Code format hook waits for that build once.
- Dependabot can't see `tools.mod`, so tool versions are bumped by hand (`go get -tool -modfile=tools.mod <pkg>@<version>`, then restore the go and toolchain lines). That's easy to forget. govulncheck only scans the app code, not the tools.
- `tools.mod` also lists the app's own dependencies after tidy. It isn't a tools only file, which can surprise a reader.
- The pre push test run adds tens of seconds to every push, and more once the DB tests grow.
- Branch protection means work goes through PRs. The owner bypass keeps that from blocking you, but using the bypass, like `--no-verify`, skips the checks. CI on main and GitHub push protection are the real backstops.
- The depguard and test deny lists must be kept in sync by hand when a new client package appears.
- The Postgres image tag lives in both `compose.yaml` and the workflow. The CI step catches drift but doesn't fix it.

**Neutral**:
- gofumpt may reformat a few files once, in the tooling change itself.
- Coverage is visible but never gates a merge until feature #9 revisits it.
- `make vet` stays, even though `golangci-lint run` already runs `govet`.

## Follow-up

- [ ] Turn on GitHub push protection for secrets (repo Settings, Code security). It's free for public repos and can't be set from code.
- [ ] Add the `main` ruleset requiring `ci-ok`, with you on the bypass list, after the first green run (a check can only be required once it has reported at least once).
- [ ] Revisit the coverage gate when feature #9 lands real tool server logic.
- [ ] When features #6 to #9 add the `ldap`, `graph`, and `zammad` packages, confirm they live at the paths depguard and the boundary test deny.
- [ ] Agent Skills and MCP servers for these tools were declined on 2026-10-17. Run `/sync` so `AGENTS.md` records the decline and the new targets.
