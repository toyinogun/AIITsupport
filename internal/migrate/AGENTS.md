# internal/migrate

## Overview

Applies the embedded goose migrations as `noord_owner`, the way the cluster's migration Job does (`make migrate` on a laptop runs the goose command line against the same files instead, not this package). It first waits until the database accepts the owner and every app role exists, because CloudNativePG creates the managed roles after the database and migration 00002 grants to them. Only `cmd/seed` uses it: `seed migrate` is the cluster's migration Job (spec 0015, AC-9), and `seed smoke` reads the version through it (AC-17).

## Key files

| File | Owns |
|---|---|
| `migrate.go` | `Run` (wait, then apply), `Wait` and `ready` (connect as the owner and count the roles), `Up` (apply every pending migration), `Current` (the database's goose version), `Latest` (the newest embedded file's version), `Migrations` (the embedded folder), `Roles`, `ErrNotReady`, `Result` |
| `migrate_test.go` | the tests: `Latest` equals the newest file, the deadline ends in `ErrNotReady`, a bad URL is refused, and a run migrates once then does nothing |

The migrations themselves live in `db/migrations/` and are embedded by package `db` (`db/embed.go`, imported as `schema`). See [db/AGENTS.md](../../db/AGENTS.md) for how to write one.

## Commands

```bash
go test -race ./internal/migrate/   # the run test needs TEST_DATABASE_URL, a superuser URL; without it that test skips
go run ./cmd/seed migrate           # Run against MIGRATE_DATABASE_URL (the cluster Job does this in the seed image)
make migrate                        # laptop path: db-roles, then go tool goose up, no wait for roles
```

## Conventions

- `Roles` is a hand kept copy of the app roles in `db/roles.sql` (and the `dbtest` role constants). When a migration needs a new role, add it here too, or the Job starts before the role exists and migration fails on the missing grant. Nothing tests the two lists against each other.
- The connecting user is the migration owner, `MIGRATE_DATABASE_URL` (`internal/config`, a secret). Errors name the user but never the URL.
- `MIGRATE_WAIT_TIMEOUT` (default 5m, allowed range 10s to 30m) bounds the wait. `Wait` polls every 2 seconds and ends in `ErrNotReady` wrapping the last connect or role error.
- `Up` and `Current` open their own `database/sql` handle through pgx stdlib and close it; goose is used through its provider, never the global functions.
- The smoke check compares `Current` with `Latest`, so a new migration file moves both without any other edit.

## Gotchas

- `Latest` reads the numeric prefix of every `*.sql` file, so a file name without a number prefix breaks it and the smoke check.
- A run against a database whose roles are missing does not fail fast: it waits the whole timeout. Check `make db-roles` first on a Compose volume that predates the roles (journal 0085).
- In the cluster the migration Job is an ArgoCD Sync hook, so it runs only when the Application syncs. A new migration reaches the database only after a `make release` and `make install` roll the image (journal 0086).

## Related specs

- [0015 One command GitOps install](../../docs/specs/0015-gitops-install/index.md) (AC-9, AC-17)
- [0003 data model](../../docs/specs/0003-data-model/index.md) (the roles and grants the migrations create)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
