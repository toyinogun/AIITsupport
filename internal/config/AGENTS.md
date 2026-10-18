# internal/config

## Overview

The single source of every setting the agent and the tool server read. Each binary parses and validates its settings at startup and refuses to start when a required value is missing or malformed, reporting every problem in one error.

## Key files

| File | Owns |
|---|---|
| `config.go` | `Common`, `Agent`, `ToolServer`, `Graph`, `SeedEntraMFA`, `Migrate`, `Seed`, `SeedFiles` structs, `LoadAgent`, `LoadToolServer`, `LoadGraph`, `LoadSeed`, `LoadSeedEntraMFA`, `LoadSeedFiles`, `Names()` |
| `config_test.go` | load behavior and the `.env.example` drift test |

## Conventions

- Declare a setting once, as a struct field with tags: `env:"NAME"`, optional `default:"..."`, `required:"true"`, `oneof:"a|b"`. No other package reads the environment; `main` only passes `os.LookupEnv` in.
- Supported field types: `string`, `int`, `bool`, `time.Duration` (`20s`), and `[]string` (comma separated). Add a case in `parseInto` for anything else.
- Shared settings go in `Common`; agent only settings in `Agent`; anything touching AD, Entra, or Zammad goes in `ToolServer` only.
- Database URLs are per role, never shared: `AGENT_DATABASE_URL` in `Agent`, `TOOLSERVER_DATABASE_URL` in `ToolServer`, `MIGRATE_DATABASE_URL` in `Migrate`. A setting read outside the two binaries (like migrations) goes in a struct that `Names()` also walks, so the drift test covers it.
- `cmd/seed`'s settings (`SEED_*`, including the domain Administrator password) live in `Seed`, read only by `cmd/seed`; `SeedFiles` is the no secret subset `seed validate` loads. Never add them to `Agent` or `ToolServer`.
- Entra has two app registrations, never mixed (spec 0005): the tool server's `Graph` (`GRAPH_*`, embedded in `ToolServer`, empty at boot for the AD only fallback, required by `LoadGraph`) and the seed app (`SEED_GRAPH_*`, in `Seed`). `SeedEntraMFA` gives `seed entra mfa` the tool server's app, never the seed app. `LoadSeed` treats all `SEED_GRAPH_*` unset as "not configured" and a partial set as an error naming each missing setting.
- A setting starts optional with a `// feature #N` comment and flips to `required:"true"` when that feature's code starts using it.
- Tests pass a map backed `LookupFunc`, never the real environment.

## Gotchas

- Adding or renaming a setting means editing `.env.example` too: `TestEnvExampleMatchesConfig` fails on any drift in either direction.
- A blank value counts as unset, so it falls back to the default (or fails if required).
- `READ_ONLY` is only the boot default for the kill switch; the mounted `KILL_SWITCH_FILE` wins at call time (feature #9).

## Related specs

- [0001 stack and architecture](../../docs/specs/0001-stack-architecture/index.md) (Configuration required)
- [0003 data model](../../docs/specs/0003-data-model/index.md) (the per role database URLs and the audit settings)
- [0004 Noord directory](../../docs/specs/0004-noord-directory/index.md) (the `SEED_*` settings and the tool server's `LDAP_*` dev values)
- [0005 Entra tenant and sync](../../docs/specs/0005-entra-tenant-sync/index.md) (the `GRAPH_*`, `SEED_GRAPH_*`, and `SEED_ENTRA_*` settings)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
