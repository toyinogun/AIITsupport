# internal/ldap

## Overview

Noord's one LDAPS layer (spec 0007): the dialer verified against our CA, the OU layout and DN helpers, lock state, and the directory reads and the one write the tool server makes as `svc-itsupport`. `cmd/seed` and `internal/seed` use the same dialer, DN helpers, and lock state; only the tool server uses `Client` and `Writer`.

## Key files

| File | Owns |
|---|---|
| `conn.go` | `Dialer` (LDAPS only, CA verified, `Timeout` from `LDAP_TIMEOUT`), `Dial`, `DialContext` (bounded by the call's context), `Bind`, `WaitAndBind`, `BaseDN`, `ReadOne`, `LoadCA` |
| `dn.go` | `NoordDN`, `UsersDN`, `GroupsDN`, `AdminsDN`, `DepartmentDN`, `CheckRelativeOU`, `Under`, `UnderAny` |
| `lock.go` | `LockState`, `State` (the computed lock flag), `LockoutDuration`, the FILETIME conversion |
| `client.go` | `Config`, `Client` (`FindUsersByMail`, `GetUser`, `GetUsers` (many DNs on one connection), `Lock`, `GroupsByName`, `InChainMemberOf`), `Writer` (`ClearLockout`), `ErrTooManyMatches` |
| `groups.go` | spec 0013: `GroupInfo`, `ListGroups`, `GroupByName`, `GroupNestedInto` and `UserGroups` (the security groups holding a group or a user at any depth), `GroupsNestedUnder` (the groups nested into any of several parents, one search), `GroupHasMember`, `ParseDepartments`, `Writer.AddMember` (LDAP 20 or 68 is `ErrAlreadyMember`) |
| `password.go` | spec 0011: `Client.PasswordPolicy`, `PasswordLastSet`, `FindUserByAccount`, `UserInChainMemberOf`; `PasswordWriter.SetPassword` (bound as `svc-pwreset`); `IsPolicyRefusal` (LDAP 19 or 53) |

## Conventions

- "Locked" means `msDS-User-Account-Control-Computed & 0x10`, never `lockoutTime > 0` (it stays set after a lock expires). `svc-itsupport` can read the computed flag (checked live, spec 0007 build step 1).
- Every filter value goes through `EscapeFilter`, every DN value through `EscapeDN`. Compare DNs with `Under` (parsed, RDN by RDN, case insensitive), never a string suffix.
- Each `Client` or `Writer` call dials, binds, runs, and closes; there is no shared connection. The connection closes when the call's context ends.
- `Client` cannot write. `Writer` holds the tool layer's two writes: `ClearLockout` (`AccountDeps`) and `AddMember` (`GroupDeps`, spec 0013).
- The password reset is a second, separate write: `PasswordWriter.SetPassword` binds as `svc-pwreset` (`RESET_LDAP_BIND_*`), and only `internal/resetpage` holds one. It replaces `unicodePwd` (quoted, UTF-16LE) and sets `lockoutTime` to 0 in one modify, with the policy hints control.
- TLS is always verified against the CA file. There is no skip verify option.
- This package and `go-ldap` are on both agent deny lists (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`).

## Gotchas

- `lockoutDuration` is a negative count of 100ns intervals; 0 or the smallest int64 means locked until an admin clears it, so there is no lock end.
- Samba honors the in chain matching rule (`1.2.840.113556.1.4.1941`), so nesting at any depth is one search per denied group.
- AD itself refuses `svc-itsupport` writing an admin account (insufficient access), under the tool layer's own OU fence.
- `TEST_SAMBA=1` builds the Samba image before any test runs; a cold build can outlast `go test`'s default 10 minute timeout, so pass `-timeout 25m` (journal 0056).
- Samba accepts the password policy hints control but does not apply the history rule to a reset (journal 0036). Length and character classes still apply.
- Samba keeps a replaced password valid for 60 minutes by default, LDAP binds included. The dev image sets `old password allowed period = 0` in `smb.conf`, because provision silently drops that option (journal 0038).
- Samba, unlike Windows AD, accepts a password that contains the `sAMAccountName` even with complexity on. Use a password with too few character classes to test a policy refusal.

## Commands

```bash
go test -race ./internal/ldap/                                                  # unit tests
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 go test -race ./internal/ldap/   # live, against a throwaway seeded Samba
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 go test -race -run 'TestLive(ResetPassword|ResetAccount|PasswordPolicy|UserInChain)' ./internal/ldap/   # svc-pwreset's least privilege (spec 0011, AC-15)
```

## Related specs

- [0007 tool server and risk tiers](../../docs/specs/0007-tool-server-risk-tiers/index.md) (identity, the fence, `account_unlock`)
- [0004 Noord directory](../../docs/specs/0004-noord-directory/index.md) (the directory, `svc-itsupport`'s permissions)
- [0011 password reset links](../../docs/specs/0011-password-reset-links/index.md) (`svc-pwreset`, the reset write, the password policy read)
- [0013 Access requests via group owner](../../docs/specs/0013-access-requests-group-owner/index.md) (the group reads, department tags, the add)

_Drafted by /develop from the introducing change, worth a quick human pass._
