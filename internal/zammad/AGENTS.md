# internal/zammad

## Overview

A thin client for Zammad's REST API (spec 0006): tickets, articles, and the knowledge base. The tool server uses it with the `noord-assistant` token (`ZAMMAD_TOKEN`, from #9 on); `cmd/seed helpdesk` uses it with the local seed admin to fill the helpdesk. It never writes a Zammad user.

## Key files

| File | Owns |
|---|---|
| `client.go` | `New`, `Options`, `TokenAuth`, `BasicAuth`, `Request` (with the `From` header), `Client.Do` (one attempt, per request timeout, no redirects), the `guard` transport, `Guarded`, `ErrUserWrite` |
| `errors.go` | `Error` (method, path, status, Zammad's message; never the token or the query), `Status`, `IsForbidden`, `IsUnauthorized` |
| `users.go` | `User`, `Me`, `SearchUsers` (reads only) |
| `tickets.go` | `NewTicket`, `NewArticle`, `TicketUpdate`, `CreateTicket`, `UpdateTicket`, `CreateArticle`, `GetTicket` |
| `kb.go` | `CreateCategory`, `CreateAnswer`, `PublishAnswer`, `GetAnswer` |
| `zammadtest/fake.go` | an `httptest` fake Zammad (token and admin auth, tickets, articles, KB, 403s the assistant gets, request log with `From`, faults) |

## Conventions

- **The user write wall.** Zammad lets any `ticket.agent` search, create, and update users, so the assistant token could too (journal 0020). The client's transport refuses any request that is not `GET` to `/api/v1/users`, `/api/v1/organizations`, or `/api/v1/user_access_token`, after normalising the path (case, dot segments, repeated slashes, a `.json` suffix). `TestEveryMethodIsCovered` fails when a new exported method is not added to the guard test's list, so keep `everyMethod` in `guard_test.go` complete.
- Tickets name their customer by numeric `customer_id`, never an email, login, or `customer` object, so Zammad never creates a user on the fly.
- `From` makes Zammad act as another user (admin only). A customer acting through `From` may only create a ticket in state `new`; set state and owner afterwards as the agent.
- Errors name the method, path, and status, never the token or the query string (logins and search terms live there).
- This package is on both credential deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`): the agent never imports it.

## Gotchas

- Zammad's search index lags writes and a fresh LDAP import by seconds, and by more than a minute right after `make helpdesk-reset` (journal 0025). Never use `SearchUsers` to confirm a write; the seed takes user ids from bootstrap's login map instead.
- KB writes return `{id, assets}`, not the object. The assistant token gets 403 on any KB write and on `/api/v1/settings`, but it **can** create and edit customer users at the Zammad level; it cannot edit an Admin (403, probed 2026-10-18).

## Commands

```bash
go test -race ./internal/zammad/...   # unit tests against the fake, no network
```

## Related specs

- [0006 helpdesk, KB, and Slack](../../docs/specs/0006-helpdesk-kb-slack/index.md) (the contract, with [verify.md](../../docs/specs/0006-helpdesk-kb-slack/verify.md))

_Drafted by /sync from the introducing change, worth a quick human pass._
