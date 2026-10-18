# Verify: Noord directory · spec 0004 · updated 2026-10-18
_Steps derived from spec 0004 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

Before you start: your `.env` needs the `SEED_*` block and the `LDAP_*` dev values from `.env.example` (an older `.env` will not have them).

## Commands
- [x] Clean clone, Docker running: `make directory-reset` → exits 0 in under 2 minutes, logs one `directory seeded` JSON line with `duration_ms`; `docker volume ls` still shows `noord_pgdata` → AC-1
- [x] `make directory-reset` twice, then dump the AC-2 attribute list per DN under `OU=Noord` after each → identical (or run `TEST_SAMBA=1 go test -run TestLiveDirectory/identical ./internal/seed/directory/`) → AC-2
- [x] Count users per department OU as Administrator → 80 current staff (Warehouse 28, Transport 25, the rest office); every user has UPN and mail `<username>@noord.example` and a manager except `annemiek.bakker` → AC-3
- [x] Read the 5 leavers (`thomas.meijer`, `mark.dekker`, `lotte.hendriks`, `patrick.claassen`, `ad.vanloon`) → `userAccountControl` 512, only weak hints (stale title, a manager who left, "contract ended"), no leave date anywhere in AD → AC-4
- [x] Search `(cn=Jan de Vries)` → two users, `jan.devries` (Transport) and `jan.devries2` (Finance), different managers and titles; `sanne.jansen` and `sanne.janssen` both exist → AC-5
- [x] List `OU=Groups` → about 20 groups including `SG-Finance`, `Finance_Users`, `fin-readonly`; security groups `groupType` -2147483646, distribution lists 2 with `mail`; at least 4 with no `managedBy` → AC-6
- [x] `SG-IT-Admins` sits in `OU=Admin Accounts`, is in `Domain Admins`, holds `adm.ruben.mulder`; an in chain search (`1.2.840.113556.1.4.1941`) for Domain Admins under `OU=Groups` returns nothing → AC-7
- [x] Domain root as Administrator → `lockoutThreshold` 5, duration and window 15 minutes; within 15 minutes of seeding `make directory-login NAME=jan.devries2` (also `yara.schouten`, `kevin.smits`) → fails with "the account is locked" → AC-8
- [x] `make directory-login NAME=eva.peters` → "bound as CN=Eva Peters,…", locked false; `make directory-lock NAME=eva.peters` → "locked eva.peters"; login again → "the account is locked"; `make directory-login` with no NAME → usage message → AC-9
- [x] `seed/directory/noord.yaml` with `synced: false` users → exactly those 6 are missing from `SYNC-Entra-Users`, everyone else is in it → AC-10
- [x] Bind as `svc-itsupport` (`LDAP_BIND_DN`) → clearing `lockoutTime` on `eva.peters` and adding a member to `SG-Finance` work; unlocking `adm.ruben.mulder`, adding to `SG-IT-Admins` or `Domain Admins`, setting `unicodePwd`, editing `title`, creating a user under `OU=Users` or a computer in `CN=Computers`, and deleting a user all fail with insufficient access (50); `ms-DS-MachineAccountQuota` is 0 → AC-11
- [x] OpenLDAP `ldapsearch` (on macOS the Homebrew one, `/opt/homebrew/opt/openldap/bin/ldapsearch`; Apple's ignores `LDAPTLS_CACERT`) against `ldaps://127.0.0.1:55636` (not `localhost`, which OpenLDAP swaps for your machine's hostname before the cert check, journal 0009) with `LDAPTLS_CACERT=.dev/certs/ca.pem` → works; without the CA → certificate error; `nc -z 127.0.0.1 389` → nothing; inside the container a simple bind over `ldap://localhost:389` → "Transport encryption required" → AC-12
- [x] `make seed` right after a reset → "already seeded", exit 0; append a comment to `noord.yaml` and `make seed` → exit 1 naming `make directory-reset`; clear the `OU=Noord` description and `make seed` → exit 1 → AC-13
- [x] Break `noord.yaml` three ways at once (an unknown field, an unknown department, a missing manager) and run `go run ./cmd/seed validate` → one error listing all three, nothing written → AC-14
- [x] Copy `overlay.example.yaml` to `overlay.local.yaml`, reset → `eva.peters` mail is the overlay value, everyone else keeps `@noord.example`; `git status` does not show the overlay; remove it and reset → mail back to default → AC-15
- [x] `make secrets` stays clean after commit; `grep -ri password seed/directory/noord.yaml` → nothing → AC-16
- [x] `docker inspect noord-samba-1 --format '{{.HostConfig.CapAdd}} {{.HostConfig.Privileged}}'` → `[] false` → AC-17
- [x] CI `samba-image` job builds `linux/amd64,linux/arm64`; locally `docker buildx build --platform linux/amd64 seed/directory/samba` works on the Mac → AC-18
- [x] `TEST_SAMBA=1 go test -race ./...` → passes in well under 20 minutes; CI's `test` job runs it and `ci-ok` needs it → AC-19

## Value sourcing
- [x] Realm and NetBIOS name: rootDSE `defaultNamingContext` is `DC=ad,DC=noord,DC=example`; change `SAMBA_REALM` in a scratch run → base DN follows → AC-1
- [x] Administrator and svc passwords come from `.env`: change `SEED_ADMIN_PASSWORD` and `LDAP_BIND_PASSWORD`, reset → both binds work with the new values only → AC-1, AC-11
- [x] Lockout settings come from `SAMBA_LOCKOUT_*`: set the threshold to 3 in a scratch run → `seed lock` locks after 3 binds (it reads `lockoutThreshold` from the domain) → AC-8
- [x] Schema GUIDs in the ACEs match the schema (`ldbsearch … schemaIDGUID` for `lockoutTime`, `member`, `user`, `group`) and the svc SID in the ACE is `svc-itsupport`'s `objectSid` → AC-11
- [x] TLS cert comes from `/certs`: replace `.dev/certs` with a new CA and reset → old CA no longer verifies → AC-12
- [x] Machine account quota is 0 on the domain root → AC-11
- [x] User DN is `CN=<given> <surname>,OU=<department>,OU=Users,OU=Noord,…`: a name with a comma or plus in a scratch YAML lands escaped → AC-3, AC-14
- [x] Admin account: `CN=adm.ruben.mulder,OU=Admin Accounts,…`, display name "Ruben Mulder (admin)", UPN `adm.ruben.mulder@noord.example`, no mail, `userAccountControl` 66048 → AC-7
- [x] Group DN under `OU=Groups`, privileged under `OU=Admin Accounts` → AC-6, AC-7
- [x] UPN and default mail use YAML `mail_domain`; demo mail from the overlay only → AC-3, AC-15
- [x] Every seeded user binds with `SEED_USER_PASSWORD`; a password that breaks Samba's rule fails the seed naming `SEED_USER_PASSWORD`, never the value → AC-9
- [x] `manager`, `managedBy`, `member` hold DNs resolved from the model (spot check `jan.devries2` → Ilse Smit's DN) → AC-3, AC-6
- [x] Privileged nesting comes from `nested_into` (Domain Admins found by sAMAccountName) → AC-7
- [x] `SYNC-Entra-Users` members are exactly the `synced: true` users → AC-10
- [x] Locked users are exactly the `locked: true` users → AC-8
- [x] Marker on `OU=Noord` is `seed:sha256:<YAML hash>:none`, and the overlay hash replaces `none` when an overlay exists → AC-13
- [x] Lock state always comes from `msDS-User-Account-Control-Computed & 0x10` read as Administrator: after 15 minutes a seeded lock shows unlocked even though `lockoutTime` is still set → AC-8
- [x] Eval truth (`left`, `locked`, `synced`) is readable from `directory.Load` and absent from AD → AC-4
- [x] Tool server dev values in `.env.example` (`LDAP_URL`, `LDAP_BASE_DN`, `LDAP_BIND_DN`, `LDAP_CA_CERT`) bind as `svc-itsupport` over verified LDAPS → AC-11, AC-12

## Acceptance-criteria coverage
- AC-1 commands step 1, value sourcing realm and passwords · AC-2 step 2 · AC-3 step 3, DN and mail rows · AC-4 step 4, eval truth row · AC-5 step 5 · AC-6 step 6 · AC-7 step 7, admin and nesting rows · AC-8 step 8, lockout and lock state rows · AC-9 step 9, password row · AC-10 step 10, sync row · AC-11 step 11, GUID and quota rows · AC-12 step 12, cert row · AC-13 step 13, marker row · AC-14 step 14, escaping row · AC-15 step 15 · AC-16 step 16 · AC-17 step 17 · AC-18 step 18 · AC-19 step 19
