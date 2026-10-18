# internal/seed

## Overview

Builds Noord's fictional on prem Active Directory (spec 0004): a Samba AD container that provisions itself on first boot, then filled over LDAPS from one YAML file. `cmd/seed` is the only caller and the only code that binds as the domain Administrator. It is a dev and ops tool, never a long running service. The eval reuses it.

## Key files

| File | Owns |
|---|---|
| `certs/certs.go` | `Generate` (dev CA and Samba server cert, keeps files that exist), `Pool`, the cert SANs |
| `directory/model.go` | `Load` / `Parse`: the seed file plus the optional overlay into a `Model`; eval truth (`Left`, `Locked`, `Synced`) lives here only |
| `directory/validate.go` | every seed file rule, reported together in one error |
| `directory/dn.go` | the OU layout and DN builders (values are escaped) |
| `directory/conn.go` | `Dialer` (LDAPS verified against our CA), `WaitAndBind`, `BaseDN` |
| `directory/apply.go` | `Apply`: fills a fresh directory, writes the marker last, returns `ErrNeedsReset` on any other seeded content |
| `directory/lock.go` | `Lock`, `LockAndConfirm`, `State` |
| `directory/sambatest/` | starts a throwaway Samba container for tests (`TEST_SAMBA`) |
| `entra/scope.go` | `ReadScope`: the direct members of `SYNC-Entra-Users` with their AD managers; refuses any member outside `OU=Users,OU=Noord` (spec 0005) |
| `entra/plan.go` | `MakePlan`: diffs the AD scope against Entra (live and recycle bin) into actions; every guard (delete limit, collisions, duplicates) fails before any write |
| `entra/sync.go` | `Syncer.Run`: reads Entra, plans, then writes one call at a time; `Result` holds the logged counts |
| `entra/phone.go` | `GeneratedPhones` (`+1 20255501NN` by bytewise sorted position), `ExpectedPhones`, `RandomPassword` |
| `entra/demo.go` | `LoadDemo` / `ParseDemo` for `seed/entra/demo.local.yaml`, `CheckDemoPassword` |
| `../../cmd/seed/` | the `certs`, `validate`, `directory`, `login`, `lock`, `entra sync`, `entra mfa` commands |
| `../../seed/directory/` | `noord.yaml`, `overlay.example.yaml`, and the Samba image (`samba/Dockerfile`, `samba/entrypoint.sh`) |

## Commands

```bash
make directory-reset          # replace the Samba container and seed it
make seed                     # seed again; "already seeded" when the marker matches
go run ./cmd/seed validate    # check the seed file, no network, no passwords
TEST_SAMBA=1 go test -race ./internal/seed/...   # live tests against a throwaway container
make entra-sync [ONLY=<username>]  # make Entra match SYNC-Entra-Users; skips when SEED_GRAPH_* is unset
make entra-mfa NAME=<username> [CLEAR=1]  # list (or clear) a user's MFA methods with the tool server app
make noord-reset              # directory-reset, then entra-sync
DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 TEST_ENTRA=1 go test -race -run Live ./internal/seed/entra/  # live tenant tests, laptop only, .env loaded
```

## Conventions

- Change the directory by editing `seed/directory/noord.yaml`, then `make directory-reset`. Every rebuild is a fresh container (no volume), so two rebuilds always match.
- Never write eval truth to AD. A leaver's `left` date, `locked`, and `synced` stay in the YAML and are read through `directory.Load`.
- "Locked" means `msDS-User-Account-Control-Computed & 0x10`, read as the Administrator, never `lockoutTime > 0` (it stays set after a lock expires). Locks are made by failed binds, because AD only lets you clear `lockoutTime`.
- TLS is always verified against the CA file. There is no skip verify option anywhere.
- The marker on `OU=Noord` is `seed:sha256:<YAML hash>:<overlay hash or none>`, written last. Any seeded content without the matching marker means "run `make directory-reset`", never a partial top up.
- Passwords come from settings (`SEED_ADMIN_PASSWORD`, `SEED_USER_PASSWORD`, `LDAP_BIND_PASSWORD`), never from the YAML or the image. Error messages name the setting, never the value.
- `svc-itsupport` (the tool server's account) is fenced by AD permissions set in `entrypoint.sh`: it may clear `lockoutTime` on users under `OU=Users` and change `member` on groups under `OU=Groups`, nothing else.
- `seed/directory/overlay.local.yaml` is gitignored and only replaces `mail` for listed demo users.
- The Entra sync touches only users marked `employeeType` = `noord-sync`. An unmarked user holding an in scope `mailNickname` or UPN (live or in the recycle bin) stops the run before any write; it is never edited, restored, or deleted.
- A managed user out of scope is soft deleted (recycle bin) and restored with the same object id when it returns, never created again. More than `SEED_ENTRA_DELETE_LIMIT` deletes stops the run before any write.
- Demo users live only in the gitignored `seed/entra/demo.local.yaml` (at most 2); tracked files hold only `+1 20255501NN` numbers. No command resets a password: the seed app has no right to (journal 0016).

## Gotchas

- The Samba container must have the hostname `samba`, or it starts but never listens (journal 0008).
- On macOS, check LDAPS with Homebrew OpenLDAP against `ldaps://127.0.0.1:55636`: Apple's `ldapsearch` ignores `LDAPTLS_CACERT`, and OpenLDAP swaps `localhost` for your machine's hostname before the cert check (journal 0009).
- A seed that fails partway (a weak `SEED_USER_PASSWORD`, say) leaves content behind, so the next `make seed` refuses until you reset.
- Live tests hang forever on Docker's credential helper unless `DOCKER_AUTH_CONFIG='{"auths":{}}'` is set (journal 0011).
- Graph's reads lag its writes, so the first sync after a drift may miss part of it; the next run fixes the rest (journal 0015). Live tests sync until a run makes no writes.

## Related specs

- [0004 Noord directory](../../docs/specs/0004-noord-directory/index.md) (the contract, with [verify.md](../../docs/specs/0004-noord-directory/verify.md))
- [0005 Entra tenant and sync](../../docs/specs/0005-entra-tenant-sync/index.md) (the `entra/` sync, with [verify.md](../../docs/specs/0005-entra-tenant-sync/verify.md)); setup in [the Entra runbook](../../docs/runbooks/entra-tenant.md)

_Drafted by /sync from the introducing change, worth a quick human pass._
