# Verify: Coding standards & tooling · spec 0002 · updated 2026-10-17
_Steps derived from spec 0002 "Done means" (DM-1 to DM-6; this decision spec has no AC list). `/check verify` runs these; `/test` locks the durable ones._

## Commands
- [ ] Clone the repo fresh, and put only Go on PATH (no lefthook, golangci-lint, or gitleaks: `PATH=<dir with only go>:/usr/bin:/bin`) → `make hooks` prints `wrote .git/hooks/pre-commit` and `wrote .git/hooks/pre-push` → DM-1
- [ ] In that clone, `make check` → lint `0 issues.`, gen-check and tidy-check clean, govulncheck `No vulnerabilities found.`, gitleaks `no leaks found`, every test package `ok` → DM-1
- [ ] In that clone, commit a docs only change → the pre commit hook runs, `fmt` and `lint` show `(skip)`, `secrets` passes, the commit lands → DM-1
- [ ] Add `fmt.Println("hi")` to a function in any package, run `make lint` → fails with `use of fmt.Println forbidden` (forbidigo) → DM-2
- [ ] `go test -run 'ClientSystems|Boundary' -v ./cmd/agent/` → both `TestAgentDoesNotImportClientSystems` (real case) and `TestBoundaryCheckCatchesTransitiveImport` (stub case) pass → DM-2
- [ ] Throwaway, never committed: create `internal/zammad/zammad.go` (any exported const) and a `cmd/agent/probe.go` that imports it → `make lint` reports `is not allowed from list 'agent-credential-boundary'` (depguard), and `TestAgentDoesNotImportClientSystems` fails naming `internal/zammad`. Delete both files after → DM-2
- [ ] Stage a file holding a fake Slack bot token (`xoxb-` plus two 13 digit groups plus a 24 character mixed case string) and commit → the pre commit `secrets` step fails with `leaks found: 1`, no commit lands → DM-3
- [ ] Ask Claude Code to write a badly formatted Go file (for example `func  X( ) int {return 1}`) → after the edit, the file on disk is gofumpt formatted (`func X() int { return 1 }`) → DM-4
- [ ] Commit a badly formatted Go file by hand (outside Claude Code) → the pre commit `fmt` step fails with a gofumpt diff, no commit lands → DM-4
- [ ] `.git/hooks/pre-push origin x` (or a real push) → `test` and `gen-check` pass → DM-1

## CI (after you push and open a PR)
- [ ] The PR runs `lint`, `test`, `generated`, `vuln`, `secrets`, `images (agent)`, `images (toolserver)`, and `ci-ok`, all green → DM-5
- [ ] In the `test` job log, `internal/db` runs `TestOpen...` against the service container, not `SKIP` (the job summary also shows total coverage) → DM-5
- [ ] Each job's first Go step prints `go version go1.26.9` and passes the toolchain check → DM-5
- [ ] After the first green run, add the `main` ruleset requiring `ci-ok` with you on the bypass list, and turn on secret push protection (spec Follow-up) → DM-5

## Docs
- [ ] After `/sync`, root `AGENTS.md` lists `make lint`, `fmt`, `fmt-check`, `gen-check`, `tidy-check`, `vuln`, `secrets`, `hooks`, `check`, the tooling rules, and toolchain 1.26.9 (it still says 1.26.5 today) → DM-6

## Done means coverage
- DM-1 clean clone, hooks, `make check` · covered by the first three Commands steps and the pre push step
- DM-2 print ban and credential boundary · covered by the forbidigo, boundary test, and depguard stub steps
- DM-3 secret blocked at commit · covered by the fake token step
- DM-4 formatting · covered by the Claude Code hook step and the hand commit step
- DM-5 CI green with real DB tests · covered by the CI section (needs a push)
- DM-6 AGENTS.md · covered by the Docs step (needs `/sync`)
