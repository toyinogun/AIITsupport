# 0002. Lint, format, hooks, and CI checks for the Go monolith: rationale

Decision record for [index.md](index.md). `/develop` builds from the index; this file explains why.

## Context

Spec 0001 settled the stack: one Go module, two binaries, `go test` with the race detector, GitHub Actions for CI, and "one linter config". It did not name the linter, the formatter, the hook runner, or the checks CI must pass. Feature #4 needs those names before `/develop tooling` can build anything, and every later feature inherits whatever is chosen here.

The forces are specific to this project. **The repo is public**, so a leaked Slack token or Zammad key is exposed the moment it is pushed; catching it in CI is already too late. **The safety claim lives in code**: the tool server is the only process that holds client credentials, and the agent must never import the AD, Entra, or Zammad clients. Today that is a sentence in `AGENTS.md` that nothing checks. **The logs carry PII risk**: feature #12 adds redaction, which only works if every log line goes through `log/slog`. **One engineer, three weeks**: the tooling must cost seconds per commit, never minutes, or it gets skipped with `--no-verify`. **Reproducibility**: spec 0001 asks that everyone, including CI, run the same tool versions with nothing to install.

The current code (17 Go files) already passes `gofmt` and `go vet`. It logs with `slog` key and value pairs in `snake_case` and static messages, writes to stderr only in `main` for the `<service>: <error>` exit line, and imports no client system package from `cmd/agent`. A trial run of the lint set below found about 20 small issues, almost all in tests (unchecked `Close` calls, test only credentials, `net.Listen` without a context). So the standard writes down patterns the code mostly follows already, and no competing pattern needs migrating.

Leaving this undecided means each feature makes up its own lint rules (or none), secrets get caught by GitHub after the push, and the credential boundary stays a promise.

## Options considered

### Option 1: golangci-lint v2, lefthook, gitleaks, tools pinned in `tools.mod`, enforced now on the whole repo

golangci-lint v2 runs a curated set of linters and the formatters from one `.golangci.yml`. lefthook (a single Go binary) runs format, lint, and secret checks on commit, and tests on push. gitleaks scans staged changes locally and the full history in CI. All three, plus govulncheck, are pinned as `tool` entries in a separate `tools.mod`, run with `go tool -modfile=tools.mod <tool>`. CI runs the same commands plus image builds, and a ruleset on main requires them.

**Pros**:
- One config, one runner, one version everywhere. A fresh clone needs only Go.
- Enforcement is automatic and fails CI. The credential boundary and the slog rule become lint errors.
- The repo is small and close to clean, so turning everything on now means one short cleanup in the tooling change.

**Cons**:
- Building the tools from source makes the first run slow (a few minutes, cached after that). The golangci-lint project prefers its prebuilt binaries.
- Dependabot cannot read `tools.mod`, so tool versions are bumped by hand, and only to releases that support the project's Go version.
- Some upkeep: a dozen linters means an occasional false positive that needs a `//nolint` with a reason.

### Option 2: The same tools, enforced on new code only

Configure the same toolset, but have the linter report only issues introduced since a base commit (`--new-from-rev`), with existing findings tracked as debt.

**Pros**:
- No cleanup before the first green build.
- A common way to add linting to a large existing codebase.

**Cons**:
- The debt here is about 20 small findings in 17 files, less than an hour to fix. The base commit logic just adds a way for findings to hide.
- Findings that hide behind a base commit still show up the first time someone touches that file.

### Option 3: Go's own tools only (`gofmt`, `go vet`, `staticcheck`), CI only, no local hooks

Use only what ships with Go, plus staticcheck, run in CI, and rely on review for the rest.

**Pros**:
- Fewest moving parts, nothing to configure, fastest to set up.
- Every Go engineer already knows these tools.

**Cons**:
- No security linting (gosec), no import boundary checks, and no slog consistency checks. The safety claim stays prose.
- Secrets are caught after the push to a public repo, if they're caught at all.
- Problems surface only after CI runs, not before a commit.

## Rationale

The public repo and the credential boundary decide this. Option 3 would leave both the secret risk and the agent import rule to human memory, and those are the two failures that would sink the project's central claim. Option 1 turns each into a check that fails before a commit and again in CI. It does that with one runner and one config file, which matches spec 0001's "one linter config". gitleaks runs in the hook as well as in CI because a secret in a public repo is exposed the moment it is pushed. Only the hook runs before that moment.

Enforcing on the whole repo now (over Option 2) is cheap. The code already follows the logging, stderr, and import patterns the standard names, and the trial run's findings are small and mostly in tests. It also keeps the rule simple. There's no base commit to explain and no debt list to track.

Pinning the tools in a separate `tools.mod` keeps spec 0001's promise that CI and your laptop run the same versions with nothing to install. It also keeps golangci-lint's large dependency tree out of the app's `go.mod`, which would otherwise show up in the app's dependency updates and vulnerability scans. The price is a slow first build and bumping versions by hand. Both are acceptable for a three week build with per job CI caching. The runner up was the golangci-lint GitHub Action with a version pin plus Homebrew locally. It's faster, but the two versions would drift (your Homebrew copy is already 2.12.2).

Some calls are made here at write time. For each one, the runner up is in parentheses.
- Lint the whole repo in the pre commit hook rather than only the staged files. golangci-lint works on whole packages, and at this size a cold run takes about 6 seconds (runner up: `--new-from-rev=HEAD`).
- Back depguard with a transitive dependency test. depguard sees only direct imports, so a shared package that imports the Zammad client would slip past it. The test closes that gap (runner up: depguard alone).
- Write our own git hook shims that run lefthook through `go tool`, because lefthook's generated hook quietly exits when lefthook isn't on PATH (runner up: `lefthook install`, which only works if Homebrew has it).
- Run CI on pushes to main and on pull requests, not on every branch push, so a PR branch isn't checked twice (runner up: any branch push).
- Require one `ci-ok` aggregator job instead of each job by name, since matrix jobs report under names like `images (agent)` (runner up: listing every job name in the ruleset).
- Report coverage in CI without failing on it until feature #9 (runner up: an 80% gate now, which would only reward filler tests over stub packages).
- Pin actions by full commit SHA, because a moved tag could run code in a public repo's CI (runner up: major version tags).
- Drop rowserrcheck and sqlclosecheck. A trial showed they don't flag an unclosed `pgx.Rows`, so they would add config while catching nothing (runner up: keeping them for a future `database/sql` use that isn't planned).
