# internal/resetlink

## Overview

The password reset link store (spec 0011): the token, its hash, and the `reset_links` rows. Only `sha256(token)` is stored; the plain token lives only in the tool result, the agent's turn, and the Slack message. The tool server is its only importer: `password_reset_link` issues links, `internal/resetpage` checks and claims them, and `cmd/toolserver` runs the cleanup.

## Key files

| File | Owns |
|---|---|
| `resetlink.go` | `NewToken` (32 bytes from `crypto/rand`, base64url, 43 characters), `WellFormed`, `Hash`, `Link`, `Issue`, `Store` (`Issue`, `CountRecent`, `Valid`, `Claim`, `Revert`, `DeleteExpired`), `ErrNotValid` |
| `cleanup.go` | `Cleanup`: deletes expired rows at start and then hourly, each run bounded by 5 minutes, logging `reset_links_cleaned` or `reset_links_cleanup_failed` |
| `../../db/migrations/00003_reset_links.sql` | the table, its one live link per account index, and the `noord_toolserver` grants |
| `../../db/queries/reset_links.sql` | the sqlc queries behind `Store` |

## Conventions

- Every expiry check uses the database's clock (`now()`), never Go's.
- `Issue` runs in one transaction: the account's advisory lock, every `live` link for the account set `voided`, then the new row. A partial unique index keeps at most one `live` link per account.
- `Valid` changes nothing, so a link preview fetching the page is harmless. `Claim` is one guarded UPDATE (`live` to `used`, only while unexpired) that commits before anything touches AD; a concurrent claim gets `ErrNotValid`.
- `Revert` undoes a claim only when guarded on the claim's own `used_at` and only while unexpired. If it fails, the link stays `used` (fail closed).
- Never log or store the plain token or its hash. `TestCanaryResetLink` (in `internal/agent` and `internal/mcpserver`) guards this.
- This package is on both agent deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`).

## Commands

```bash
TEST_DATABASE_URL='postgres://noord:noord-dev@localhost:55432/noord?sslmode=disable' go test -race ./internal/resetlink/   # store tests on a scratch database
```

## Related specs

- [0011 password reset links](../../docs/specs/0011-password-reset-links/index.md) (AC-4, AC-13, AC-18)
- [the password reset runbook](../../docs/runbooks/password-reset.md)

_Drafted by /sync from the introducing change, worth a quick human pass._
