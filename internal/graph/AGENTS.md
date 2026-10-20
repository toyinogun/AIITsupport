# internal/graph

## Overview

A thin client for Microsoft Graph, Entra ID's REST API (spec 0005): the token, a transport that retries throttling and replication lag, paging, error mapping, and the reads and phone method writes the tool server may make. It is not the Graph SDK; tokens come from `azidentity` client credentials. User writes (create, update, delete, restore) live in `internal/seed/entra` only, never here.

## Key files

| File | Owns |
|---|---|
| `client.go` | `New`, `NewWithSecret`, `Options`, `Credentials`, `Client.Do` (per request timeout, retries), `List` / `ListAll` (follow `@odata.nextLink`, same host only) |
| `errors.go` | `ErrNotFound`, `ErrNotManaged`, `Error` (its `Cause` gives the fixed `graph_<status>` token for `cause.Of`), `Status`, `IsNotFound`, `IsForbidden`, `IsConflict` |
| `users.go` | `ManagedMarker` (`noord-sync`), `User`, `ManagedUser`, `Managed`, `UserByUsername`, `UsersByNickname`, `Query`, `UserPath` |
| `methods.go` | `AuthMethods`, `PhoneMethods`, `DeletePhoneMethod`, `AuthenticatorMethods`, `DeleteAuthenticatorMethod`, `SignInPreference` (spec 0012), `MaskPhone` |
| `graphtest/fake.go` | an `httptest` fake Graph for unit tests (users, recycle bin, managers, phones, Authenticator registrations, the sign in preference and the default method rule, faults, write counts) |
| `live_test.go` | the spec 0012 Authenticator probe against the real tenant (`TEST_ENTRA=1`, `TEST_ENTRA_MFA_USER`; `TEST_ENTRA_DELETE=1` also deletes); it deletes a Temporary Access Pass at once if Graph ever issues one |

## Conventions

- **The fence.** Every user lookup resolves by `mailNickname` only and goes through `Managed`: a user without `employeeType` = `noord-sync` is `ErrNotManaged`, an unknown one `ErrNotFound`, before any auth method call. Methods take a `ManagedUser`, so code here cannot reach an unmarked user (your tenant admin included).
- Two app registrations, never mixed: the tool server app (`GRAPH_*`, `config.Graph`) may read users and auth methods, change phone methods, and delete Microsoft Authenticator methods (spec 0012) only; the seed app (`SEED_GRAPH_*`, `config.Seed`) writes users and is used only by `cmd/seed`. Pass `ClientIDSetting` so errors name the setting, never a value.
- Calls run one at a time. 429 and 503 wait for `Retry-After` (or back off) up to `MaxRetries`; a 404 is retried only when the request asks for it (an object created moments ago).
- Mask every phone number with `MaskPhone` (last 2 digits) before it reaches a log or output. Error text never carries the query string.
- This package is on both credential deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`), together with `azidentity`: the agent never imports it.

## Gotchas

- Graph's reads lag its writes by tens of seconds (journal 0015). A read right after a write may show the old state; tests and the sync converge by trying again, never by assuming the write is visible.
- Graph checks phone numbers against carrier allocations, so most "fictional" ranges are rejected; only the North American `+1 20255501NN` range worked (journal 0013). The fake refuses the rejected ranges too.
- Graph refuses to delete a user's default MFA method while others remain (400, journal 0041), and the tool server app cannot read `signInPreferences` (400), so `mfa_clear` uses a fixed order with the Authenticator last and defers a refused Authenticator once when another is still to come.
- Application permissions are tenant wide: Graph itself lets the tool server app read your admin's phone methods (journal 0018). The fence above is the only wall until an administrative unit scopes the app (spec 0005 follow up).

## Commands

```bash
go test -race ./internal/graph/...   # unit tests against the fake, no network
```

## Related specs

- [0005 Entra tenant and sync](../../docs/specs/0005-entra-tenant-sync/index.md) (the contract, with [verify.md](../../docs/specs/0005-entra-tenant-sync/verify.md)); setup in [the Entra runbook](../../docs/runbooks/entra-tenant.md)
- [0012 MFA re-registration with approval](../../docs/specs/0012-mfa-reregistration-approval/index.md) (the Authenticator methods, the sign in preference, the delete order, AC-11 and AC-20)

_Drafted by /sync from the introducing change, worth a quick human pass._
