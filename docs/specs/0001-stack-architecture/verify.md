# Verify: Stack & architecture (scaffold) · spec 0001 · updated 2026-10-17
_Steps derived from spec 0001 "Scaffold done means" (SD-1 to SD-5; this decision spec has no AC list). `/check verify` runs these; `/test` locks the durable ones._

## Commands
- [x] On a clean clone with no `.env`, run `make dev` → `.env` is created from `.env.example`, Postgres starts in Compose and reports healthy, goose prints `successfully migrated database to version: N` (the newest migration), and both binaries log `http server listening` → SD-1
- [x] `docker compose exec -T postgres psql -U noord -d noord -c 'select version_id from goose_db_version'` → rows 0 up to the newest version, so the migrations are applied, not just generated → SD-1
- [x] While `make dev` runs: `curl -i localhost:18080/healthz` and `curl -i localhost:18081/healthz` → both `200 ok` → SD-2
- [x] `curl -i -X POST localhost:18080/healthz` → `405` → SD-2
- [x] Ctrl-C on `make dev` → both binaries log `http server stopped` and no `bin/agent` or `bin/toolserver` process is left → SD-1
- [x] `env -i PATH="$PATH" ./bin/agent` → exits 1 and names `AGENT_DATABASE_URL`, `TOOLSERVER_TOKEN`, and `TOOLSERVER_URL` in one message → SD-2
- [x] `env -i PATH="$PATH" TOOLSERVER_DATABASE_URL=x TOOLSERVER_TOKEN=t READ_ONLY=maybe ./bin/toolserver` → exits 1 with `READ_ONLY: not true or false` → SD-2
- [x] Set `AGENT_DATABASE_URL` (or `TOOLSERVER_DATABASE_URL`) to a port nothing listens on and start that binary → exits 1 with `reach database` → SD-2
- [x] `make test` and `make vet` → both pass → SD-3
- [x] `make gen`, then `git status --short internal/db/queries` → no changes → SD-3
- [x] `make docker` → both `aiitsupport-agent:dev` and `aiitsupport-toolserver:dev` build; `docker run --rm aiitsupport-agent:dev` exits 1 with the missing settings message → SD-4
- [x] `docker buildx build --platform linux/amd64 --build-arg CMD=toolserver .` → builds, image architecture is `amd64` → SD-4
- [x] `docker build .` with no `CMD` build argument → fails with `set --build-arg CMD=agent or CMD=toolserver` → SD-4
- [x] Add a setting to `internal/config` without adding it to `.env.example`, run `go test ./internal/config` → `TestEnvExampleMatchesConfig` fails naming it; revert → SD-5
- [x] `git check-ignore .env docs/.agent-cache/x bin/agent` → all three are ignored → SD-5

## Acceptance-criteria coverage
- SD-1 (`make dev` starts Postgres, migrates, starts both binaries) … covered by steps 1, 2, 5
- SD-2 (healthz 200; clear failure on a missing setting) … steps 3, 4, 6, 7, 8
- SD-3 (`make test`, `go vet`, `make gen` no diff) … steps 9, 10
- SD-4 (both images build) … steps 11, 12, 13
- SD-5 (`.env.example` matches config; `.gitignore` covers `.env` and `docs/.agent-cache/`) … steps 14, 15
