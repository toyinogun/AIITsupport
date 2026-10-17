# db

## Overview

Plain SQL for our own Postgres: goose migrations and the sqlc queries that become typed Go in `internal/db/queries`. The audit log, pending approvals, and conversation state will live here (feature #5).

## Key files

| File | Owns |
|---|---|
| `migrations/NNNNN_*.sql` | goose migrations, each with `-- +goose Up` and `-- +goose Down` |
| `queries/*.sql` | sqlc queries, one `-- name: X :one|:many|:exec` per query |
| `../sqlc.yaml` | sqlc config: schema from `migrations/`, output to `internal/db/queries` (pgx/v5) |
| `../internal/db/db.go` | `Open`: builds the pgx pool and pings it so binaries fail at startup |

## Commands

```bash
make migrate   # goose up against DATABASE_URL from .env (starts Postgres first)
make gen       # go tool sqlc generate; commit the result, it must leave no diff
make gen-check # fail if the generated code is stale (pre push hook and CI run this)
```

## Conventions

- Migrations are append only: add a new numbered file, never edit one that has run.
- Every query goes through sqlc. No hand written SQL strings in Go, and no ORM.
- Migrations run as the table owner role; the app roles never own tables (feature #5).

## Gotchas

- `internal/db/queries/` is generated. Edit the `.sql` here and run `make gen` instead.
- sqlc reads the schema from the migrations folder, so a query against a new table needs its migration first.
- Audit rows must be append only, enforced with grants, not just code (feature #5).

## Agent skills

- [golang-database](../.claude/skills/golang-database/): `samber/cc-skills-golang`, pgx scanning, transactions, DB testing

## Related specs

- [0001 stack and architecture](../docs/specs/0001-stack-architecture/index.md) (DB access, Migrations)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
