# internal/cause

## Overview

A leaf package that turns a failure into a fixed token for logs and spans, read from the error's type, never its text (spec 0010, AC-3). It imports pgx's `pgconn` only, so any package may use it.

## Key files

| File | Owns |
|---|---|
| `cause.go` | `Of` (`none`, `deadline`, `canceled`, `postgres_<sqlstate>`, `postgres`, an adapter's own token, `network`, `other`) and `Causer` |

## Conventions

- Log `cause.Of(err)` under the `cause` key, never `err.Error()`: error text can quote a message, a name, or a row.
- An adapter whose error types callers do not import implements `Causer` (`internal/slack`'s `slackError` gives `slack_<code>`). A token that is not `^[a-z0-9_]{1,64}$` is reported as `other`.
- Packages with their own error types layer on top: `internal/agent`'s `cause()` checks `not_open`, `anthropic_<status>`, and `mcp_rpc_<code>` first, then calls `Of`. `internal/tools` keeps its own `cause()` in `refusal.go` for LDAP and Zammad errors.

## Related specs

- [0010 PII redaction and retention](../../docs/specs/0010-pii-redaction-retention/index.md) (AC-3)

_Drafted by /develop from the introducing change, worth a quick human pass._
