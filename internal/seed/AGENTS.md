# internal/seed

## Overview

Builds Noord's fictional on prem Active Directory (spec 0004): a Samba AD container that provisions itself on first boot, then filled over LDAPS from one YAML file. `cmd/seed` is the only caller and the only code that binds as the domain Administrator. It is a dev and ops tool, never a long running service. The eval reuses it. Since spec 0006 it also builds Noord's Zammad helpdesk (`helpdesk/`) from `seed/helpdesk/`: a Rails bootstrap inside Zammad, a REST fill through `internal/zammad`, and a Rails backdate step that writes the marker last.

## Key files

| File | Owns |
|---|---|
| `certs/certs.go` | `Generate` (dev CA and Samba server cert, keeps files that exist), the cert SANs; the CA loader is `ldap.LoadCA` |
| `directory/model.go` | `Load` / `Parse`: the seed file plus the optional overlay into a `Model`; eval truth (`Left`, `Locked`, `Synced`) lives here only |
| `directory/validate.go` | every seed file rule, reported together in one error |
| `directory/dn.go` | `Model.UserDN`, `Model.GroupDN` (values are escaped); the OU layout, `Dialer`, `WaitAndBind`, and `BaseDN` live in `internal/ldap` (spec 0007) |
| `directory/apply.go` | `Apply`: fills a fresh directory, writes the marker last, returns `ErrNeedsReset` on any other seeded content |
| `directory/lock.go` | `Lock`, `LockAndConfirm`, `LockoutThreshold`; lock state is `ldap.State` |
| `directory/sambatest/` | starts a throwaway Samba container for tests (`TEST_SAMBA`) |
| `entra/scope.go` | `ReadScope`: the direct members of `SYNC-Entra-Users` with their AD managers; refuses any member outside `OU=Users,OU=Noord` (spec 0005) |
| `entra/plan.go` | `MakePlan`: diffs the AD scope against Entra (live and recycle bin) into actions; every guard (delete limit, collisions, duplicates) fails before any write |
| `entra/sync.go` | `Syncer.Run`: reads Entra, plans, then writes one call at a time; `Result` holds the logged counts |
| `entra/phone.go` | `GeneratedPhones` (`+1 20255501NN` by bytewise sorted position), `ExpectedPhones`, `RandomPassword` |
| `entra/demo.go` | `LoadDemo` / `ParseDemo` for `seed/entra/demo.local.yaml`, `CheckDemoPassword` |
| `helpdesk/content.go`, `helpdesk/validate.go` | `Load`: `seed/helpdesk/` (KB articles, tickets) into `Content`, every rule reported together; eval truth (`status`, `contradicts`, `retired_system`, `dead_contact`, `duplicate_of`) lives here only (spec 0006) |
| `helpdesk/seed.go`, `helpdesk/fill.go` | `Seeder.Run`: bootstrap, the login check against `users:`, the REST fill, then backdate with the marker last, then the retention step; on already seeded, the token check then the retention step; `ErrNeedsReset` |
| `helpdesk/rails.go` | `Runner`, `ComposeRunner` (`docker compose exec -T -e NAME... zammad-railsserver timeout ... rails r`), the `NOORD_RESULT` parser, secret redaction |
| `helpdesk/marker.go` | `Marker` (sha256 over repo relative paths and bytes of `seed/helpdesk/` except `unmarked`, plus `noord.yaml` and the CA), `ToHTML` (goldmark, raw HTML escaped) |
| `../../cmd/seed/` | the `certs`, `validate`, `directory`, `login`, `lock`, `entra sync`, `entra mfa`, `helpdesk` commands |
| `../../seed/directory/` | `noord.yaml`, `overlay.example.yaml`, and the Samba image (`samba/Dockerfile`, `samba/entrypoint.sh`) |
| `../../seed/helpdesk/` | `kb.yaml`, `kb/<slug>.md`, `tickets.yaml`, the Rails steps (`rails/bootstrap.rb`, `backdate.rb`, `retention.rb`, plus `report.rb` and `ldapsync.rb` for the live tests), and the vendored Zammad Compose files (`zammad/`) |

## Commands

```bash
make directory-reset          # replace the Samba container and seed it
make seed                     # seed again; "already seeded" when the marker matches
go run ./cmd/seed validate    # check the seed file, no network, no passwords
TEST_SAMBA=1 go test -race ./internal/seed/...   # live tests against a throwaway container
make entra-sync [ONLY=<username>]  # make Entra match SYNC-Entra-Users; skips when SEED_GRAPH_* is unset
make entra-mfa NAME=<username> [CLEAR=1]  # list (or clear) a user's MFA methods with the tool server app
make noord-reset              # directory-reset, entra-sync, then helpdesk-reset
make helpdesk-reset           # fresh Zammad, seeded from seed/helpdesk/ (about 2 minutes)
make helpdesk-seed            # "already seeded" when the marker matches, else asks for a reset
set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run Live ./internal/seed/helpdesk/  # live helpdesk tests, after helpdesk-reset
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
- `svc-pwreset` (the reset page's account, spec 0011) exists only when `SAMBA_PWRESET_PASSWORD` is set: under `OU=Users` it has the Reset Password extended right and write on `lockoutTime`, nothing else. A Samba container made before it existed has no such account; run `make directory-reset`.
- The dev Samba sets `old password allowed period = 0` in `smb.conf` after provision (which drops the `--option`), so a reset shuts the old password out at once (journal 0038).
- `svc-zammad` (Zammad's LDAP bind) has no ACEs of its own: it reads users like any authenticated account and every write fails. `seed directory` and `seed helpdesk` refuse to run without `SEED_ZAMMAD_BIND_PASSWORD`.
- The helpdesk marker (`seed:sha256:<hash>`) lives in the `Noord Logistics` organization's note in Zammad, written last by `backdate.rb`. Any other seeded content means `make helpdesk-reset`, never a top up. Changing `noord.yaml`, a helpdesk file, or the certs changes the marker.
- **The `noord-retention` scheduler (spec 0010).** Every `seed helpdesk` run that reaches the fresh or the already seeded path upserts one Zammad scheduler by name, from `rails/retention.rb` with `NOORD_RETENTION_DAYS` = `TICKET_RETENTION_DAYS`. It deletes tickets tagged `noord-assistant` only: `closed` ones N days after `last_close_at`, `merged` ones N days after `updated_at` (Zammad gives merged tickets no close time, journal 0035). The run logs `retention scheduler` with `days` and `tickets_matching_now`. `rails/retention.rb` is in `unmarked`, so editing it never forces a reset; any other new file under `seed/helpdesk/` does. Checks are in [the data retention runbook](../../docs/runbooks/data-retention.md).
- Secrets reach the Rails steps only as environment variables (`-e NAME` with no value); step output is shown with every secret value redacted.
- The seed takes Zammad user ids from bootstrap's login map, never from user search (its index lags the import). Seeded tickets name customers by numeric id.
- AD is the only way into Zammad: `bootstrap.rb` turns off self signup and password reset (`user_create_account`, `user_lost_password`), which Zammad enforces on REST and GraphQL alike. `TestLiveSideDoorsClosed` checks it (spec 0006 AC-17).
- Demo users live only in the gitignored `seed/entra/demo.local.yaml` (at most 2); tracked files hold only `+1 20255501NN` numbers. No command resets a password: the seed app has no right to (journal 0016).

## Gotchas

- The Samba container must have the hostname `samba`, or it starts but never listens (journal 0008).
- A live test that POSTs to Zammad with no credentials must first `GET /api/v1/signshow` and send its `CSRF-Token` header back with the session cookie, or Zammad refuses on CSRF and the test proves nothing about the setting it targets.
- On macOS, check LDAPS with Homebrew OpenLDAP against `ldaps://127.0.0.1:55636`: Apple's `ldapsearch` ignores `LDAPTLS_CACERT`, and OpenLDAP swaps `localhost` for your machine's hostname before the cert check (journal 0009).
- A seed that fails partway (a weak `SEED_USER_PASSWORD`, say) leaves content behind, so the next `make seed` refuses until you reset.
- Live tests hang forever on Docker's credential helper unless `DOCKER_AUTH_CONFIG='{"auths":{}}'` is set (journal 0011).
- Graph's reads lag its writes, so the first sync after a drift may miss part of it; the next run fixes the rest (journal 0015). Live tests sync until a run makes no writes.
- Zammad's LDAP source has one base DN for users and groups, so it searches `OU=Noord` and keeps only people with a `department` (journal 0019). An account without a department never reaches Zammad.
- A ticket made by hand after a seed stays until the next reset, and `TestLiveContentMatchesFiles` fails until then.
- A Zammad `Job` saved from `rails r` needs `created_by_id` and `updated_by_id` set, and a tag condition takes its value as a string, not an array (journal 0035). `TestLiveRetentionScheduler` sets ticket times through `rails r -` on stdin (`railsEval`), so it needs no extra step file under `seed/helpdesk/`.

## Related specs

- [0004 Noord directory](../../docs/specs/0004-noord-directory/index.md) (the contract, with [verify.md](../../docs/specs/0004-noord-directory/verify.md))
- [0005 Entra tenant and sync](../../docs/specs/0005-entra-tenant-sync/index.md) (the `entra/` sync, with [verify.md](../../docs/specs/0005-entra-tenant-sync/verify.md)); setup in [the Entra runbook](../../docs/runbooks/entra-tenant.md)
- [0006 helpdesk, KB, and Slack](../../docs/specs/0006-helpdesk-kb-slack/index.md) (the `helpdesk/` seeder, with [verify.md](../../docs/specs/0006-helpdesk-kb-slack/verify.md))
- [0010 PII redaction and retention](../../docs/specs/0010-pii-redaction-retention/index.md) (the `noord-retention` scheduler, AC-9 and AC-10, with the Zammad probes in [verify.md](../../docs/specs/0010-pii-redaction-retention/verify.md))

_Drafted by /sync from the introducing change, worth a quick human pass._
