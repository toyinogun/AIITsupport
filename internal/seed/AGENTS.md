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
| `../../cmd/seed/` | the `certs`, `validate`, `directory`, `login`, `lock` commands |
| `../../seed/directory/` | `noord.yaml`, `overlay.example.yaml`, and the Samba image (`samba/Dockerfile`, `samba/entrypoint.sh`) |

## Commands

```bash
make directory-reset          # replace the Samba container and seed it
make seed                     # seed again; "already seeded" when the marker matches
go run ./cmd/seed validate    # check the seed file, no network, no passwords
TEST_SAMBA=1 go test -race ./internal/seed/...   # live tests against a throwaway container
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

## Gotchas

- The Samba container must have the hostname `samba`, or it starts but never listens (journal 0008).
- On macOS, check LDAPS with Homebrew OpenLDAP against `ldaps://127.0.0.1:55636`: Apple's `ldapsearch` ignores `LDAPTLS_CACERT`, and OpenLDAP swaps `localhost` for your machine's hostname before the cert check (journal 0009).
- A seed that fails partway (a weak `SEED_USER_PASSWORD`, say) leaves content behind, so the next `make seed` refuses until you reset.

## Related specs

- [0004 Noord directory](../../docs/specs/0004-noord-directory/index.md) (the contract, with [verify.md](../../docs/specs/0004-noord-directory/verify.md))

_Drafted by /sync from the introducing change, worth a quick human pass._
