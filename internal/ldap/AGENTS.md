# internal/ldap

## Overview

Noord's one LDAPS layer (spec 0007): the dialer verified against our CA, the OU layout and DN helpers, lock state, and the directory reads and the one write the tool server makes as `svc-itsupport`. `cmd/seed` and `internal/seed` use the same dialer, DN helpers, and lock state; only the tool server uses `Client` and `Writer`.

## Key files

| File | Owns |
|---|---|
| `conn.go` | `Dialer` (LDAPS only, CA verified, `Timeout` from `LDAP_TIMEOUT`), `Dial`, `DialContext` (bounded by the call's context), `Bind`, `WaitAndBind`, `BaseDN`, `ReadOne`, `LoadCA` |
| `dn.go` | `NoordDN`, `UsersDN`, `GroupsDN`, `AdminsDN`, `DepartmentDN`, `CheckRelativeOU`, `Under`, `UnderAny` |
| `lock.go` | `LockState`, `State` (the computed lock flag), `LockoutDuration`, the FILETIME conversion |
| `client.go` | `Config`, `Client` (`FindUsersByMail`, `GetUser`, `Lock`, `GroupsByName`, `InChainMemberOf`), `Writer` (`ClearLockout`), `ErrTooManyMatches` |

## Conventions

- "Locked" means `msDS-User-Account-Control-Computed & 0x10`, never `lockoutTime > 0` (it stays set after a lock expires). `svc-itsupport` can read the computed flag (checked live, spec 0007 build step 1).
- Every filter value goes through `EscapeFilter`, every DN value through `EscapeDN`. Compare DNs with `Under` (parsed, RDN by RDN, case insensitive), never a string suffix.
- Each `Client` or `Writer` call dials, binds, runs, and closes; there is no shared connection. The connection closes when the call's context ends.
- `Client` cannot write. The one write, `Writer.ClearLockout`, is held only by the tool layer's `AccountDeps`.
- TLS is always verified against the CA file. There is no skip verify option.
- This package and `go-ldap` are on both agent deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`).

## Gotchas

- `lockoutDuration` is a negative count of 100ns intervals; 0 or the smallest int64 means locked until an admin clears it, so there is no lock end.
- Samba honors the in chain matching rule (`1.2.840.113556.1.4.1941`), so nesting at any depth is one search per denied group.
- AD itself refuses `svc-itsupport` writing an admin account (insufficient access), under the tool layer's own OU fence.

## Commands

```bash
go test -race ./internal/ldap/                                                  # unit tests
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 go test -race ./internal/ldap/   # live, against a throwaway seeded Samba
```

## Related specs

- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (identity, the fence, `account_unlock`)
- [0004 Noord directory](../../docs/specs/0004-noord-directory/index.md) (the directory, `svc-itsupport`'s permissions)

_Drafted by /develop from the introducing change, worth a quick human pass._
