# internal/config

## Overview

The single source of every setting the agent and the tool server read. Each binary parses and validates its settings at startup and refuses to start when a required value is missing or malformed, reporting every problem in one error.

## Key files

| File | Owns |
|---|---|
| `config.go` | `Common`, `Agent`, `ToolServer`, `Graph`, `Zammad`, `SeedEntraMFA`, `SeedHelpdesk`, `Migrate`, `Seed`, `SeedFiles` structs, `LoadAgent`, `LoadToolServer`, `LoadGraph`, `LoadSeed`, `LoadSeedEntraMFA`, `LoadSeedHelpdesk`, `LoadSeedFiles`, `Names()` |
| `config_test.go` | load behavior and the `.env.example` drift test |

## Conventions

- Declare a setting once, as a struct field with tags: `env:"NAME"`, optional `default:"..."`, `required:"true"`, `oneof:"a|b"`. No other package reads the environment; `main` only passes `os.LookupEnv` in.
- Supported field types: `string`, `int`, `bool`, `time.Duration` (`20s`), and `[]string` (comma separated). Add a case in `parseInto` for anything else.
- Shared settings go in `Common`; agent only settings in `Agent`; anything touching AD, Entra, or Zammad goes in `ToolServer` (or a `cmd/seed` struct), never in `Agent`. Zammad's API settings (`ZAMMAD_*`) are the `Zammad` struct, embedded in `ToolServer` and in `SeedHelpdesk`, like `Graph` (spec 0006).
- `SLACK_BOT_TOKEN` and `SLACK_APP_TOKEN` are set as a pair or not at all, with `xoxb-` and `xapp-` prefixes (`LoadAgent`); `ZAMMAD_TOKEN`, when set, is at least 32 characters from `A-Za-z0-9_-`. Errors name the setting, never the value.
- With both Slack tokens set, `LoadAgent` also requires `ANTHROPIC_API_KEY` and checks the agent loop bounds (`AGENT_MAX_CONCURRENT`, `AGENT_MAX_RESPONSE_TOKENS`, `AGENT_SHUTDOWN_GRACE`, `AGENT_CONVERSATIONS_PER_HOUR`); without Slack none of these checks run (spec 0008).
- With both Slack tokens set, `LoadAgent` also requires `SLACK_IT_CHANNEL` as a channel id (`^[CG][A-Z0-9]{2,20}$`, format only, never membership). `LoadToolServer` accepts an optional `ZAMMAD_PUBLIC_URL` (an http or https base with a host and no credentials, query, or fragment; trailing slashes trimmed) and `PRIVILEGED_ATTEMPT_WINDOW` (default `24h`, at least `1h`) (spec 0009).
- `LoadToolServer` treats `RESET_PUBLIC_URL`, `RESET_HTTP_ADDR`, `RESET_LDAP_BIND_DN`, and `RESET_LDAP_BIND_PASSWORD` as one set: all unset turns the reset feature off, a partial set fails naming each missing one. `RESET_PUBLIC_URL` follows the `ZAMMAD_PUBLIC_URL` rules and must be https unless its host is `localhost`, `127.0.0.1`, or `[::1]`. `RESET_DENIED_GROUPS` is separate from `TOOL_DENIED_GROUPS` (spec 0011, AC-17).
- Database URLs are per role, never shared: `AGENT_DATABASE_URL` in `Agent`, `TOOLSERVER_DATABASE_URL` in `ToolServer`, `MIGRATE_DATABASE_URL` in `Migrate`. A setting read outside the two binaries (like migrations) goes in a struct that `Names()` also walks, so the drift test covers it.
- `cmd/seed`'s settings (`SEED_*`, including the domain Administrator password) live in `Seed`, read only by `cmd/seed`; `SeedFiles` is the no secret subset `seed validate` loads. Never add them to `Agent` or `ToolServer`.
- Entra has two app registrations, never mixed (spec 0005): the tool server's `Graph` (`GRAPH_*`, embedded in `ToolServer`, empty at boot for the AD only fallback, required by `LoadGraph`) and the seed app (`SEED_GRAPH_*`, in `Seed`). `SeedEntraMFA` gives `seed entra mfa` the tool server's app, never the seed app. `LoadSeed` treats all `SEED_GRAPH_*` unset as "not configured" and a partial set as an error naming each missing setting.
- `LoadToolServer` refuses tool layer values out of range (`checkToolLayer`: positive timeouts, limits of at least 1, at least one `TOOL_USER_OUS` and `TOOL_GROUP_OUS` entry) and an `LDAP_URL` that is not an `ldaps://` URL with a host (spec 0007, AC-16).
- `TICKET_RETENTION_DAYS` (default `30`) is the `Retention` struct, embedded in `Common` and in `SeedHelpdesk`, so the agent's purge and the Zammad scheduler read one value. `checkRetention` refuses anything outside 1 to 365 in `LoadAgent`, `LoadToolServer`, and `LoadSeedHelpdesk` (spec 0010).
- The medium tier settings (spec 0012) are the `Approvals` struct in `ToolServer` (`SLACK_IT_APPROVERS`, `APPROVAL_EXECUTE_TIMEOUT`, `APPROVAL_EXECUTE_GRACE`, `MFA_DENIED_GROUPS`); `APPROVAL_TIMEOUT` is the `ApprovalWindow` struct, embedded in both `Approvals` and `Agent`, so both binaries read one value (journal 0043). The agent adds `APPROVAL_POLL_INTERVAL` and `APPROVAL_DECIDE_TIMEOUT` (keep it above `APPROVAL_EXECUTE_TIMEOUT`). Spec 0013 adds `ACCESS_APPROVAL_TIMEOUT` to `ApprovalWindow` (the owner's window, 5m to 24h) and `ACCESS_MAX_PENDING` to `Approvals` (1 to 10).
- The knowledge base settings (spec 0014) are `ToolServer` fields: `KB_SEARCH_LIMIT` (1 to 5), `KB_SEARCH_CANDIDATES` (from `KB_SEARCH_LIMIT` to 20), `KB_STALE_AFTER_DAYS` (30 to 3650), `KB_BODY_MAX_RUNES` (200 to 8000), and the optional `KB_STALE_AS_OF` (a `YYYY-MM-DD` date, unset in production). `LoadToolServer` refuses any value out of range or a bad date, naming the setting. `ZAMMAD_PUBLIC_URL` is reused for article links.
- A setting starts optional with a `// feature #N` comment and flips to `required:"true"` when that feature's code starts using it.
- Tests pass a map backed `LookupFunc`, never the real environment.

## Gotchas

- Adding or renaming a setting means editing `.env.example` too: `TestEnvExampleMatchesConfig` fails on any drift in either direction.
- A blank value counts as unset, so it falls back to the default (or fails if required).
- `READ_ONLY` is only the boot default for the kill switch; the mounted `KILL_SWITCH_FILE` wins at call time (feature #9).

## Related specs

- [0001 stack and architecture](../../docs/specs/0001-stack-architecture/index.md) (Configuration required)
- [0003 data model](../../docs/specs/0003-data-model/index.md) (the per role database URLs and the audit settings)
- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (the `TOOL_*`, `TICKET_*`, `KILL_SWITCH_FILE`, and `LDAP_*` tool server settings)
- [0004 Noord directory](../../docs/specs/0004-noord-directory/index.md) (the `SEED_*` settings and the tool server's `LDAP_*` dev values)
- [0005 Entra tenant and sync](../../docs/specs/0005-entra-tenant-sync/index.md) (the `GRAPH_*`, `SEED_GRAPH_*`, and `SEED_ENTRA_*` settings)
- [0006 helpdesk, KB, and Slack](../../docs/specs/0006-helpdesk-kb-slack/index.md) (the `Zammad` struct, `SeedHelpdesk`, `SEED_ZAMMAD_*`, and the Slack token pair)
- [0008 locked out user flow](../../docs/specs/0008-locked-out-user-flow/index.md) (the `LLM_*`, `ANTHROPIC_API_KEY`, and `AGENT_*` loop settings)
- [0010 PII redaction and retention](../../docs/specs/0010-pii-redaction-retention/index.md) (`TICKET_RETENTION_DAYS`)
- [0012 MFA re-registration with approval](../../docs/specs/0012-mfa-reregistration-approval/index.md) (the `APPROVAL_*`, `SLACK_IT_APPROVERS`, and `MFA_DENIED_GROUPS` settings)
- [0014 Knowledge base answers](../../docs/specs/0014-knowledge-base-answers/index.md) (the `KB_*` settings)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
