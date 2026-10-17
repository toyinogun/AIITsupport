# internal/config

## Overview

The single source of every setting the agent and the tool server read. Each binary parses and validates its settings at startup and refuses to start when a required value is missing or malformed, reporting every problem in one error.

## Key files

| File | Owns |
|---|---|
| `config.go` | `Common`, `Agent`, `ToolServer` structs, `LoadAgent`, `LoadToolServer`, `Names()` |
| `config_test.go` | load behavior and the `.env.example` drift test |

## Conventions

- Declare a setting once, as a struct field with tags: `env:"NAME"`, optional `default:"..."`, `required:"true"`, `oneof:"a|b"`. No other package reads the environment; `main` only passes `os.LookupEnv` in.
- Supported field types: `string`, `int`, `bool`, `time.Duration` (`20s`), and `[]string` (comma separated). Add a case in `parseInto` for anything else.
- Shared settings go in `Common`; agent only settings in `Agent`; anything touching AD, Entra, or Zammad goes in `ToolServer` only.
- A setting starts optional with a `// feature #N` comment and flips to `required:"true"` when that feature's code starts using it.
- Tests pass a map backed `LookupFunc`, never the real environment.

## Gotchas

- Adding or renaming a setting means editing `.env.example` too: `TestEnvExampleMatchesConfig` fails on any drift in either direction.
- A blank value counts as unset, so it falls back to the default (or fails if required).
- `READ_ONLY` is only the boot default for the kill switch; the mounted `KILL_SWITCH_FILE` wins at call time (feature #9).

## Related specs

- [0001 stack and architecture](../../docs/specs/0001-stack-architecture/index.md) (Configuration required)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
