# db

## Overview

Plain SQL for our own Postgres: goose migrations, the role script, and the sqlc queries that become typed Go in `internal/db/queries`. The data model (audit log, approvals, conversations, usage, Slack event dedup, eval runs) is spec 0003.

## Key files

| File | Owns |
|---|---|
| `migrations/NNNNN_*.sql` | goose migrations, each with `-- +goose Up` and `-- +goose Down` |
| `queries/*.sql` | sqlc queries, one `-- name: X :one|:many|:exec` per query |
| `roles.sql` | creates `noord_owner`, `noord_agent`, `noord_toolserver`, `noord_eval` (run as a superuser; safe to run again) |
| `embed.go` | package `db` (import it as `schema`): embeds `migrations/` and `roles.sql` |
| `../sqlc.yaml` | sqlc config: schema from `migrations/`, output to `internal/db/queries` (pgx/v5), plus the type overrides (uuid, jsonb, timestamptz, date) |
| `../internal/db/dbtest/` | `dbtest.New(t)`: a migrated scratch database per test with a pool per role |
| `../internal/audit/` | the only writer of `audit_log` and `approvals` |
| `../internal/db/db.go` | `Open`: builds the pgx pool and pings it so binaries fail at startup |

## Commands

```bash
make db-roles  # create the app roles in the Compose Postgres (a fresh volume gets them automatically)
make migrate   # db-roles, then goose up as noord_owner via MIGRATE_DATABASE_URL from .env
make gen       # go tool sqlc generate; commit the result, it must leave no diff
make gen-check # fail if the generated code is stale (pre push hook and CI run this)
```

## Conventions

- Migrations are append only: add a new numbered file, never edit one that has run. Editing an unmerged one your local volume already ran breaks `goose down` there (it runs the new Down), so rebuild that volume (journal 0007).
- Every query goes through sqlc. No hand written SQL strings in Go, and no ORM.
- Migrations run as `noord_owner`; migration 00002 refuses any other user and names a missing role. Roles are created by `roles.sql` (CloudNativePG in the cluster), never by a migration.
- Each binary connects as its own role (`AGENT_DATABASE_URL`, `TOOLSERVER_DATABASE_URL`); grants in the migration decide what each may touch. A new table needs its grants in the same migration, plus a row in `internal/db/grants_test.go`.
- Enumerated values are `text` with a `CHECK`, not enum types. Money is `bigint` micro US dollars.

## Gotchas

- `internal/db/queries/` is generated. Edit the `.sql` here and run `make gen` instead.
- sqlc reads the schema from the migrations folder, so a query against a new table needs its migration first.
- `audit_log` is append only twice over: app roles lack UPDATE and DELETE (42501), and a trigger raises 55000 for everyone, the owner included.
- At most one `pending` approval per account, tool, and target: the partial unique index `approvals_one_pending_idx` (migration 00006; per account and tool in 00005). 00006's Down expires pending access requests first, and its target check is `NOT VALID` so a down and up keeps old rows (journal 0057). `Defer` expires the account's overdue rows first (`ExpireDueApprovalsFor`).
- `approval_cards` (migration 00004) is the agent's table: one row per approval it owes an IT card and an outcome for. It has no foreign key to `approvals`, because the agent role cannot read that table.
- `approvals.arguments` holds the unfiltered call only while `pending` or `approved`. Every query that ends an approval must set it to NULL (a CHECK fails otherwise), and a trigger raises 55000 on any other change to it.
- A Compose volume created before roles existed needs `make db-roles` once (`make migrate` runs it). An old `.env` without `MIGRATE_DATABASE_URL` makes `make migrate` stop with a message.
- DB tests need `TEST_DATABASE_URL` as a superuser URL; `dbtest` creates roles and scratch databases under an advisory lock, so packages can run in parallel. CI runs no migration step.

## Agent skills

- [golang-database](../.claude/skills/golang-database/): `samber/cc-skills-golang`, pgx scanning, transactions, DB testing

## Related specs

- [0001 stack and architecture](../docs/specs/0001-stack-architecture/index.md) (DB access, Migrations)
- [0003 data model](../docs/specs/0003-data-model/index.md) (tables, roles, grants, the audit recorder)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
