# 0004. Noord directory: Samba AD image, one YAML seed, fresh rebuilds

**Date**: 2026-10-17
**Status**: Proposed

## Summary

This spec builds Noord's on prem Active Directory: a Samba AD container that sets itself up on first boot, filled from one readable YAML file of 80 fictional staff, their groups, and the mess planted on purpose (enabled leavers, messy group names, unowned groups, look alike users, a privileged group). A Go command, `cmd/seed`, does the filling over LDAPS (encrypted LDAP) in seconds, and the eval reuses it. Every rebuild starts from a brand new container, so two rebuilds always match. The tool server's own AD account is fenced in by AD permissions, so it cannot touch admin accounts or privileged groups even if our code has a bug.

## Rationale

Context, the options weighed, and the calls made at write time: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As the engineer, I want one command that rebuilds Noord's directory from nothing, so that a stranger, CI, and every eval run start from the same state.
- As the eval (#19, #20), I want named users in known states (locked, leaver, look alike, unsynced) and a readable record of the truth behind them, so that each ticket's expected outcome is checkable.
- As Noord's IT person, I want the agent's AD account to be unable to touch admins or privileged groups at the directory level, so that a bug in the agent's code still cannot grant Domain Admin.
- As the video audience, I want a directory that looks lived in (stale leavers, three names for "finance", groups nobody owns), so that the agent's handling of the mess is visible.

**Acceptance criteria**:
- **AC-1**: On a clean clone with Docker running, `make directory-reset` builds the image if needed, makes the dev certs if missing, replaces the Samba container with a fresh one (no volume, so the container's own filesystem is the whole state), provisions the domain `AD.NOORD.EXAMPLE` (NetBIOS `NOORD`), seeds it, and exits 0 in under 2 minutes on your laptop. It never touches the Postgres container or its `pgdata` volume. The seeder logs how long it took.
- **AC-2**: Two rebuilds in a row produce the same set of objects: the same DNs and, per object, the same values for this fixed list: `cn`, `sAMAccountName`, `userPrincipalName`, `mail`, `givenName`, `sn`, `displayName`, `department`, `title`, `description`, `manager`, `managedBy`, `member` (sorted), `groupType`, `userAccountControl`, plus "locked or not". GUIDs, SIDs, timestamps, USNs, `pwdLastSet`, and raw `lockoutTime` are excluded.
- **AC-3**: The directory holds 80 current staff in `OU=<Department>,OU=Users,OU=Noord` (Finance, Sales, Customer Service, HR, IT, Management, Warehouse, Transport; about 27 office, 28 warehouse, 25 drivers). Each has a username `firstname.lastname` (Dutch prefixes folded in, at most 20 characters), UPN and mail `<username>@noord.example`, given name, surname, display name, department, title, and a manager (null only at the top).
- **AC-4**: 5 leavers stay enabled in their old department OU. Only weak hints show it (a stale title, a manager who also left, a vague description such as "contract ended"). The truth (`left` date) lives in the YAML only and is never written to AD.
- **AC-5**: Look alike users exist: two people named Jan de Vries (`jan.devries` and `jan.devries2`) in different departments with different managers and titles, plus `sanne.jansen` and `sanne.janssen`.
- **AC-6**: About 20 groups live in `OU=Groups,OU=Noord`, across four kinds: department security groups (including `SG-Finance`, `Finance_Users`, and `fin-readonly`), app access groups, shared drive groups (read and read/write pairs), and distribution lists. Every security group is global security (`groupType` `-2147483646`); distribution lists are global distribution (`groupType` `2`) and carry `mail` `<name>@noord.example`. At least 4 groups have an empty `managedBy`; every other group has `managedBy` set to a staff member's DN.
- **AC-7**: Privileged access is seeded the realistic way: `SG-IT-Admins` lives in `OU=Admin Accounts,OU=Noord`, is a member of the built in `Domain Admins`, and contains the IT person's separate admin account (`adm.<username>`, also in `OU=Admin Accounts`). No group in `OU=Groups` is a member, direct or nested, of any privileged group.
- **AC-8**: The domain lockout policy is threshold 5, observation window 15 minutes, lockout duration 15 minutes. The 3 users the YAML marks `locked: true` are locked when the seed finishes, locked by failed binds, because AD only allows clearing `lockoutTime`, never setting it. "Locked" always means the computed flag `msDS-User-Account-Control-Computed & 0x10` is set (read as Administrator), never just `lockoutTime > 0`, which stays set after a lock expires.
- **AC-9**: You can log in and lock out by hand: `make directory-login NAME=<username>` succeeds for an unlocked user with the dev password and reports lock state; `make directory-lock NAME=<username>` locks that user; a login while locked fails, and the command says "locked" from an Administrator read, not from the bind error text. A missing `NAME` fails with a usage message.
- **AC-10**: Users with `synced: true` (the default) are members of `SYNC-Entra-Users` in `OU=Groups`; about 6 users are left out to form the sync gap for #7.
- **AC-11**: `svc-itsupport` (in `OU=Service Accounts`, the tool server's bind account) can read the whole directory, clear `lockoutTime` on users under `OU=Users`, and change `member` on groups under `OU=Groups`. AD refuses it (insufficient access) when it tries to unlock or edit anything in `OU=Admin Accounts`, change the membership of `SG-IT-Admins` or `Domain Admins`, reset any password, or create or delete any object (a user under `OU=Users` and a computer in `CN=Computers` are both tried). The domain's `ms-DS-MachineAccountQuota` is 0, so no ordinary account can create computer objects.
- **AC-12**: Only LDAPS is reachable: Samba serves TLS on 636 with a server cert from our dev CA (SANs: `localhost`, `127.0.0.1`, `samba`, `samba.noord-directory.svc`, `samba.noord-directory.svc.cluster.local`), Compose publishes only `127.0.0.1:55636`, and a client that verifies against `LDAP_CA_CERT` connects with no skip verify option anywhere.
- **AC-13**: Rerun is safe. If `OU=Noord` carries a marker that matches the current YAML and overlay, `seed directory` logs "already seeded" and exits 0 without writing. If the directory holds seeded content with no marker or a different marker, it exits 1 telling you to run `make directory-reset`. "Seeded content" means any department OU under `OU=Users`, or any object in `OU=Users`, `OU=Groups`, or `OU=Admin Accounts`; the entrypoint's skeleton and `svc-itsupport` don't count. The marker is written last.
- **AC-14**: The seed file is validated before any write, and every problem is reported in one error: unknown fields; a sAMAccountName or UPN used twice anywhere in the domain (users, admins, and groups together, ignoring case); a username outside `^[a-z][a-z0-9.]*[a-z0-9]$` or over 20 characters (admin names included); a display name clash inside one OU; a manager, owner, or member that does not exist; a manager cycle, or more than one user with no manager; a group membership cycle (including a group inside itself); a non privileged group nested into a privileged one; an unknown department; and an overlay entry for an unknown user. Values that go into a DN are escaped.
- **AC-15**: The optional overlay `seed/directory/overlay.local.yaml` is gitignored; when present it replaces `mail` for the users it lists (for 2 or 3 demo users matched to real Slack test accounts), and when absent the seed runs unchanged. A committed `overlay.example.yaml` shows the format with fictional values.
- **AC-16**: The repo holds no real data: every name is fictional, every mail address ends in `@noord.example`, no password appears in the YAML or the image, and `make secrets` stays clean.
- **AC-17**: Samba runs with an empty `cap_add` (no `CAP_SYS_ADMIN`; Docker's default set already includes `NET_BIND_SERVICE`), storing NT ACLs in `user.NTACL`, so the pod can run at the baseline pod security level. If provisioning cannot work that way, the fallback (`CAP_SYS_ADMIN`, privileged level) is used and recorded in the journal and in this spec's Follow-up.
- **AC-18**: The image builds and runs on both arm64 (your Mac) and amd64 (the cluster and CI).
- **AC-19**: With `TEST_SAMBA=1`, `go test -race ./...` runs an integration test that builds the image, provisions, seeds, and checks AC-2 to AC-15 against the live directory, using one shared container per package plus a second only for the AC-2 comparison, so it fits the test job's 20 minute limit. CI's test job sets `TEST_SAMBA=1`, so `ci-ok` depends on it.

## Decision

**Chosen option**: Option 1: First boot image with a security skeleton, content from one YAML via a Go seeder, fresh container per rebuild

Our Samba image provisions the domain and lays down the fixed security skeleton (top OUs, `svc-itsupport`, delegation ACLs, lockout policy) on first boot. `cmd/seed` fills it from `seed/directory/noord.yaml` over LDAPS, and every rebuild discards the container and its volume.

**Implementation skills**: `testcontainers-go` (`testcontainers/claude-skills`, `.claude/skills/testcontainers-go/`) · `golang-error-handling` · `golang-security` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `golang-testing` (`everything-claude-code` plugin) · `senior-kubernetes-engineer` (`~/.claude/skills/senior-kubernetes-engineer/`, only for the k3s notes #17 builds). Open each only when building in its area.

## Feature design

**Data model sketch** (what `noord.yaml` holds and the AD object each entry becomes)

| Entity | Key | Fields (required unless marked) | Relationships |
|---|---|---|---|
| Domain (one, made by the entrypoint) | realm `AD.NOORD.EXAMPLE` | NetBIOS `NOORD`; base DN `DC=ad,DC=noord,DC=example`; lockout 5 / 15 min / 15 min; Samba's default complexity rule; `ms-DS-MachineAccountQuota` 0 | holds every OU |
| Skeleton OUs (made by the entrypoint) | DN | `OU=Noord`, then `OU=Users`, `OU=Groups`, `OU=Service Accounts`, `OU=Admin Accounts` beneath it | delegation ACEs sit on `OU=Users` and `OU=Groups` |
| Department OU (YAML `departments`) | name | one of the eight departments | child of `OU=Users`; 1 : N users |
| User (YAML `users`) | `username` = sAMAccountName, unique, at most 20 chars | `given_name`, `surname` (with prefix, for example "de Vries"), `department`, `title`; `manager` (nullable username); `description` (nullable); `locked` (bool, default false); `synced` (bool, default true); `left` (nullable date, never written to AD). Derived: `cn` and `displayName` = given name + surname, `userPrincipalName` = `<username>@<mail_domain>`, `mail` = the same or the overlay value; `userAccountControl` 512 | N:1 department OU · N:1 manager (a user) · N:M groups |
| Admin account (YAML `admins`) | `username` (`adm.<person>`, at most 20 chars) | `person` (the staff username it belongs to); `userAccountControl` 66048 (enabled, password never expires) | lives in `OU=Admin Accounts`; member of `SG-IT-Admins` |
| Service account (made by the entrypoint) | `svc-itsupport` | password from `SAMBA_SVC_PASSWORD`, `userAccountControl` 66048 | lives in `OU=Service Accounts`; holds the delegated rights |
| Group (YAML `groups`) | `name` = cn = sAMAccountName, unique | `kind`: `department` · `app` · `share` · `distribution` · `privileged`; `owner` (nullable username, becomes `managedBy`); `members` (usernames, admin usernames, or group names); `description` (nullable); `nested_into` (built in group names, allowed only for `privileged`) | N:M members (users or groups) · N:1 owner. `privileged` lives in `OU=Admin Accounts`; all others in `OU=Groups`. `groupType` `-2147483646` (global security) for every kind but `distribution`, which gets `2` (global distribution) plus `mail` `<name>@<mail_domain>` |
| Sync group (generated) | `SYNC-Entra-Users` | members = every user with `synced: true` | lives in `OU=Groups` |
| Marker (generated) | description of `OU=Noord` | `seed:sha256:<hex of YAML bytes>:<hex of overlay bytes, or none>` | written last |
| Overlay (gitignored file) | `username` | `mail` | replaces mail on the listed users |

File shape (illustration of the schema, not the content):
```yaml
version: 1
mail_domain: noord.example
departments: [Finance, Sales, Customer Service, HR, IT, Management, Warehouse, Transport]
users:
  - {username: jan.devries, given_name: Jan, surname: de Vries, department: Transport, title: Driver, manager: kees.visser}
  - {username: jan.devries2, given_name: Jan, surname: de Vries, department: Finance, title: Accounts Payable, manager: ilse.smit, locked: true}
admins:
  - {username: adm.<it-person>, person: <it-person>}
groups:
  - {name: SG-Finance, kind: department, owner: ilse.smit, members: [jan.devries2]}
  - {name: fin-readonly, kind: share, owner: null, members: [...]}
  - {name: SG-IT-Admins, kind: privileged, owner: null, members: [adm.<it-person>], nested_into: [Domain Admins]}
```

**State transitions**:
- Container (no volume; its own filesystem is the state): created → provisioned and skeleton laid down (entrypoint, before `samba` starts) → healthy → seeded (marker written) → in use (the agent unlocks or edits groups) → removed by the next `make directory-reset`. A plain restart keeps the state (the entrypoint skips provisioning when `sam.ldb` exists).
- User lock: unlocked → locked (5 failed binds within 15 minutes; computed flag `0x10` set) → unlocked (the agent writes `lockoutTime` 0, or 15 minutes pass and the flag clears while `lockoutTime` stays set).

**API surface** (no HTTP; the surface is make targets, the `cmd/seed` CLI, the image's environment, and the Go package the eval imports)

| Surface | Kind | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `make certs` → `seed certs` | make / CLI | `--dir` (default `.dev/certs`), `--host` (repeatable, adds a SAN) | `ca.pem`, `ca.key`, `server.pem`, `server.key`; skips files that exist | local files | dir not writable |
| `make directory-up` | make | depends on `.env` and `certs` | `docker compose up -d --wait samba`: Samba healthy on `127.0.0.1:55636` | none | certs missing (entrypoint names `make certs`), port taken |
| `make seed` → `$(LOAD_ENV) && go run ./cmd/seed directory` | make / CLI | `Seed` config, `SEED_FILE`, optional overlay | seeded directory, marker; one JSON log line with counts and duration | Administrator bind over LDAPS | validation errors (all listed), wait timeout, partial or stale marker (exit 1, says rebuild), a write refused |
| `make directory-reset` | make | depends on `.env` and `certs` | `docker compose rm -sf samba`, then `directory-up`, then `seed`. Never `docker compose down -v`, which would also drop `pgdata` | none | any step's error |
| `seed validate` | CLI | `SEED_FILE`, overlay | exit 0 or every validation problem; no network | none | validation errors |
| `make directory-login NAME=` → `seed login <username>` | make / CLI | username, `--password` (optional, default `SEED_USER_PASSWORD`) | "bound as <DN>", locked or not (computed flag), `badPwdCount` | bind by the user's DN (from the model), then an Administrator read for the state | missing `NAME` (usage), invalid credentials, locked (from the Administrator read) |
| `make directory-lock NAME=` → `seed lock <username>` | make / CLI | username | "locked <username>" after confirming the computed lock flag | binds by the user's DN with a wrong password, then an Administrator read | unknown user, lockout policy off (threshold 0), still unlocked after the binds |
| Samba image `seed/directory/samba/` | container env | `SAMBA_REALM`, `SAMBA_DOMAIN`, `SAMBA_ADMIN_PASSWORD`, `SAMBA_SVC_PASSWORD`, `SAMBA_LOCKOUT_THRESHOLD` (5), `SAMBA_LOCKOUT_DURATION` (15), `SAMBA_LOCKOUT_WINDOW` (15); certs mounted read only at `/certs`; Compose `hostname: samba` | LDAPS on 636; healthcheck `LDAPTLS_CACERT=/certs/ca.pem ldapsearch -H ldaps://localhost:636 -x -b "" -s base`, `start_period` 90s | n/a | provision or skeleton failure exits non zero with Samba's error, before `samba` starts |
| `internal/seed/directory` | Go package | `Load(file, overlay) (Model, error)`, `Apply(ctx, conn, model, opts) (Result, error)`, `Lock(ctx, url, ca, userDN, threshold) error`, `State(ctx, adminConn, userDN) (LockState, error)` | the model (including the `left` truth for the eval), the result counts | caller supplies binds | wrapped errors with context |
| `internal/seed/directory/sambatest` | test helper | `t`, image build context | a running fresh container (certs made in `t.TempDir()`): LDAPS URL, CA path, passwords; cleaned up at test end; one shared per package | n/a | Docker missing (test fails with a clear message when `TEST_SAMBA=1`) |

**Value sourcing**

| Action | Value produced / displayed | Source |
|---|---|---|
| provision (entrypoint) | realm, NetBIOS name | `SAMBA_REALM=AD.NOORD.EXAMPLE`, `SAMBA_DOMAIN=NOORD` in `compose.yaml` |
| provision | Administrator password | `SAMBA_ADMIN_PASSWORD`, which Compose fills from `SEED_ADMIN_PASSWORD` in `.env` |
| provision | `svc-itsupport` password | `SAMBA_SVC_PASSWORD`, which Compose fills from `LDAP_BIND_PASSWORD` in `.env` (so the tool server and the account always agree) |
| provision | lockout threshold, duration, window | `SAMBA_LOCKOUT_*` env, defaults 5 / 15 / 15 in the entrypoint and `compose.yaml` |
| provision | schema GUIDs for the ACEs | constants in the entrypoint (the base schema is the same in every forest): `lockoutTime` `28630ebf-41d5-11d1-a9c1-0000f80367c1`, `member` `bf9679c0-0de6-11d0-a285-00aa003049e2`, `user` class `bf967aba-0de6-11d0-a285-00aa003049e2`, `group` class `bf967a9c-0de6-11d0-a285-00aa003049e2`. Written from memory, so confirm once against the schema when you build; the permission wall test catches a wrong one |
| provision | svc account SID in the ACEs | read from the account right after it is created (`ldbsearch` on `sam.ldb`, `objectSid`) |
| provision | the ACE text | `(OA;CIIO;WP;<lockoutTime>;<user class>;<svc SID>)` on `OU=Users`, and `(OA;CIIO;WP;<member>;<group class>;<svc SID>)` on `OU=Groups`, added with `samba-tool dsacl set` (it appends). `CIIO` = inherited by children only, not the OU itself. Read access needs no ACE (authenticated users read by default) |
| provision | hostname and TLS settings | `--host-name=samba`; smb.conf `tls enabled = yes`, `tls keyfile = /certs/server.key`, `tls certfile = /certs/server.pem`, `tls cafile = /certs/ca.pem`; provision option `acl_xattr:security_acl_name=user.NTACL` |
| provision | machine account quota | `ms-DS-MachineAccountQuota` set to 0 on the domain root |
| provision | TLS cert, key, CA | files mounted at `/certs`, made by `seed certs` (local) or cert-manager (k3s, #17) |
| seed directory | base DN | rootDSE `defaultNamingContext` |
| seed directory | user DN | `CN=<given name> <surname>,OU=<department>,OU=Users,OU=Noord,<base DN>` |
| seed directory | admin account DN, display name, UPN | `CN=<username>,OU=Admin Accounts,OU=Noord,<base DN>`; display name = the person's display name + " (admin)"; UPN `<username>@` + `mail_domain`; no mail |
| seed directory | group DN | `CN=<name>,OU=Groups,OU=Noord,<base DN>`, or `OU=Admin Accounts` for `privileged` |
| seed directory | UPN, default mail | `<username>@` + YAML `mail_domain` |
| seed directory | demo user mail | overlay file entry, else the default |
| seed directory | user password and enable sequence | add the user with `userAccountControl` 514 (disabled), replace `unicodePwd` with the UTF-16LE encoding of the password in double quotes (LDAPS only), then set 512 (66048 for admin accounts). Password: `SEED_USER_PASSWORD` |
| seed directory | `manager`, `managedBy`, `member` values | the DN of the referenced user or group, resolved from the loaded model |
| seed directory | privileged nesting | YAML `nested_into`, resolved by sAMAccountName search for the built in group |
| seed directory | `SYNC-Entra-Users` members | every user with `synced: true` |
| seed directory | which users to lock | YAML `locked: true` |
| seed directory | marker | `seed:sha256:<hex SHA 256 of the YAML bytes>:<hex SHA 256 of the overlay bytes, or none>` |
| seed directory | "seeded content exists" | any department OU under `OU=Users`, or any object in `OU=Users`, `OU=Groups`, or `OU=Admin Accounts` (one search per OU, size limit 1) |
| seed directory, lock | how many failed binds | the domain root's `lockoutThreshold` attribute (error if 0) |
| lock | the wrong password | `SEED_USER_PASSWORD` + a fixed suffix (never a valid password) |
| lock, login | lock state shown | `msDS-User-Account-Control-Computed & 0x10` (locked) and `badPwdCount`, read as Administrator by DN |
| lock, login | the user's bind DN | the DN derived from the model (bind by DN, never by UPN) |
| eval (#19, #20) | who has left, who is locked, who is unsynced | the `Model` from `Load` (YAML truth), never AD |
| tool server (#9) | where and as whom to connect | `LDAP_URL`, `LDAP_BASE_DN`, `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD`, `LDAP_CA_CERT` (0001), dev values below |

**Key invariants**:
- Samba has no named volume locally, and a rebuild always replaces the container (`docker compose rm -sf samba`). No target ever runs `docker compose down -v`.
- The security skeleton (top OUs, `svc-itsupport`, the ACEs, the lockout policy) comes only from the entrypoint; the YAML cannot create objects outside `OU=Users/<department>`, `OU=Groups`, or `OU=Admin Accounts`, and cannot grant rights.
- The skeleton and ACEs exist before `samba` starts serving and before any seeded object is created, so every seeded object inherits them.
- The delegation ACEs are inherited only by `user` objects under `OU=Users` (write `lockoutTime`) and `group` objects under `OU=Groups` (write `member`). Nothing is delegated on `OU=Admin Accounts`, `OU=Service Accounts`, or the built in containers.
- `privileged` groups live in `OU=Admin Accounts`; no other group may be nested into a privileged or built in admin group.
- The marker is written only after every object and every lock succeeded.
- Validation runs fully before the first LDAP write.
- No password, real name, or real address is ever in a tracked file; the overlay and `.dev/` are gitignored.

**Security model**:
- **Compliance scope**: none real (all data is fictional), but the repo is public, so the fictional data rule is enforced by AC-16 and gitleaks.
- **Administrator**: used only by `cmd/seed` (local, CI, and a k3s seed Job in #17). Never given to the tool server or the agent. `internal/seed` joins the agent deny lists (`agent-credential-boundary` in `.golangci.yml` and `cmd/agent/boundary_test.go`).
- **`svc-itsupport`**: the tool server's only identity. Read everywhere (Samba's default for authenticated users), clear `lockoutTime` on staff, write `member` on standard groups, nothing else. Granting a password reset, object creation, or any right on admin objects is a spec change.
- **Seeded users**: share the dev password from `.env`. Anyone with that file can act as any fictional user; that is acceptable only because the domain is fictional, and the handover runbook says so.
- **Transport**: LDAPS only, verified against our CA. Samba's default `ldap server require strong auth = yes` stays on, so no simple bind works over plain LDAP even inside the container network. Only 636 is published, bound to loopback in Compose.
- **Privileges**: the container runs as root (Samba needs it) with an empty `cap_add` (Docker's default set already holds `NET_BIND_SERVICE`) and no `CAP_SYS_ADMIN` (`user.NTACL`), so `noord-directory` can run at the baseline pod security level on k3s.

**Configuration required** (declared once in `internal/config`, mirrored in `.env.example`):
- New `Seed` struct, walked by `Names()` so the drift test covers it:
  - `SEED_LDAP_URL`: where the seeder connects (default `ldaps://localhost:55636`).
  - `SEED_CA_CERT`: CA file for verifying Samba (default `.dev/certs/ca.pem`).
  - `SEED_ADMIN_USER`: the Administrator bind DN (default `CN=Administrator,CN=Users,DC=ad,DC=noord,DC=example`).
  - `SEED_ADMIN_PASSWORD` (required): the domain Administrator password; Compose passes it to the container as `SAMBA_ADMIN_PASSWORD`.
  - `SEED_USER_PASSWORD` (required): the one dev password for every seeded user and admin account (dev default meets Samba's complexity rule).
  - `SEED_FILE`: the seed file (default `seed/directory/noord.yaml`).
  - `SEED_OVERLAY_FILE`: the optional overlay (default `seed/directory/overlay.local.yaml`; a missing file is fine).
  - `SEED_WAIT_TIMEOUT`: how long to wait for LDAPS to answer (default `90s`).
- Existing `ToolServer` settings get dev values in `.env.example` (still optional until #9): `LDAP_URL=ldaps://localhost:55636`, `LDAP_BASE_DN=DC=ad,DC=noord,DC=example`, `LDAP_BIND_DN=CN=svc-itsupport,OU=Service Accounts,OU=Noord,DC=ad,DC=noord,DC=example`, `LDAP_BIND_PASSWORD` (dev value, also feeds `SAMBA_SVC_PASSWORD`), `LDAP_CA_CERT=.dev/certs/ca.pem`.
- Dev passwords (`SEED_ADMIN_PASSWORD`, `SEED_USER_PASSWORD`, `LDAP_BIND_PASSWORD`) use only `[A-Za-z0-9_-]` so bash (`$(LOAD_ENV)`) and Compose read `.env` the same way, mix upper, lower, and digits for complexity, and contain no staff given name or surname token of 3 or more letters (Samba rejects those).
- Container only (in `compose.yaml`, not Go settings): `SAMBA_REALM`, `SAMBA_DOMAIN`, `SAMBA_LOCKOUT_THRESHOLD`, `SAMBA_LOCKOUT_DURATION`, `SAMBA_LOCKOUT_WINDOW`, plus the two passwords mapped from `.env`.
- Tests: `TEST_SAMBA=1` turns on the integration test (like `TEST_DATABASE_URL` for DB tests).
- `.gitignore` gains `.dev/` and `seed/directory/overlay.local.yaml`.

**Failure handling**:
- Samba not answering yet: the seeder retries the LDAPS connect with backoff until `SEED_WAIT_TIMEOUT`, then fails naming the URL.
- Certs missing: the entrypoint exits with "run make certs"; a cert whose SANs don't match gives a TLS error that the seeder wraps with the hostname it tried.
- `user.NTACL` provisioning fails: switch to the `CAP_SYS_ADMIN` fallback (AC-17), journal it, and update this spec.
- A write fails partway: the seeder stops, writes no marker, and exits 1; the next run sees a partial directory and says `make directory-reset`.
- The password is rejected by complexity: the error names the setting (`SEED_USER_PASSWORD`), never the value.
- A lock does not take: `seed lock` fails rather than reporting success.
- Seeded locks expire after 15 minutes; anything that needs a user locked later (a demo, an eval ticket) calls `seed lock` or `directory.Lock` just before.

**Critical test scenarios** (integration ones need `TEST_SAMBA=1`):
- Happy path: fresh container, seed, then counts per OU and kind, the messy finance names, owners and unowned groups, look alikes, leaver hints, the sync group, and the `SG-IT-Admins` nesting all match the YAML, verifies **AC-1**, **AC-3** to **AC-7**, **AC-10**.
- Identical rebuilds: seed a second fresh container and diff the AC-2 attribute list per DN against the shared one, verifies **AC-2**.
- Lockout: the 3 seeded users have the computed lock flag set; `Lock` locks a fresh user in 5 failed binds; a correct bind on a locked user fails; a correct bind on an unlocked user succeeds, verifies **AC-8**, **AC-9**.
- Permission wall: bound as `svc-itsupport`, clearing `lockoutTime` on a staff user and adding a member to `SG-Finance` succeed; clearing it on `adm.<it-person>`, adding to `SG-IT-Admins` or `Domain Admins`, setting `unicodePwd`, creating a user under `OU=Users`, and creating a computer in `CN=Computers` all fail, verifies **AC-11**.
- Transport: a bind over LDAPS without our CA fails verification; with it, it succeeds; nothing answers on 389 from the host, verifies **AC-12**.
- Rerun: seed again with the same files (no writes, exit 0); edit the YAML and seed (exit 1, rebuild hint); kill a seed halfway (no marker, next run exit 1), verifies **AC-13**.
- Validation (unit, no Docker): table tests for each AC-14 rule and the overlay rules, verifies **AC-14**, **AC-15**.
- Data hygiene (unit): every mail ends in `@noord.example`, no YAML field named or containing a password, verifies **AC-16**.
- Privileges and arch: the container's `HostConfig.CapAdd` is empty; CI builds the image for amd64 and arm64, verifies **AC-17**, **AC-18**.

## Build plan

Tracer Bullet inside this feature: first a thin thread from an empty clone to "one fictional user logs in over verified LDAPS", then thicken one strand at a time. Each step lands with its tests.

1. **Image and certs.** `seed certs` (Go `crypto/x509`), `make certs`, `seed/directory/samba/Dockerfile` (pinned `debian:trixie-slim`, `samba`, `samba-ad-dc`, `ldap-utils` for the healthcheck) and `entrypoint.sh`. Entrypoint order: skip to the last step if `sam.ldb` exists; provision (`--host-name=samba`, `--dns-backend=NONE`, `user.NTACL`); write the TLS settings; then, offline against `sam.ldb`, set password settings (`--account-lockout-threshold`, `--account-lockout-duration`, `--reset-account-lockout-after`), the machine account quota, the skeleton OUs, and `svc-itsupport`; finally exec `samba` in the foreground. Compose service `samba` (`hostname: samba`, no volume, empty `cap_add`, `127.0.0.1:55636`, healthcheck as in the API table); `make directory-up`. CI: a separate Samba image step in the `images` job (`file: seed/directory/samba/Dockerfile`, `setup-qemu-action`, layer cache), since its apt step runs per arch. Log a journal entry if the `user.NTACL` path fails, satisfies **AC-12**, **AC-17**, **AC-18**.
2. **Thin seed thread.** `Seed` config struct plus `.env.example` (and the `LDAP_*` dev values), `.gitignore` entries, `internal/seed/directory` with strict `Load` and `Apply` for one department, one user, one group, and the marker, `cmd/seed` with `validate`, `directory`, `login`, the `sambatest` helper, `make seed`, `make directory-reset`, `make directory-login` (all with `.env` as a prerequisite and `$(LOAD_ENV)`; `seed` runs through `go run` and stays out of `BINARIES`), `TEST_SAMBA=1` in CI's test job, and `internal/seed` on both agent deny lists, satisfies **AC-1**, **AC-9**, **AC-13**, **AC-19**.
3. **Permission wall.** In the entrypoint, after the skeleton: look up the svc SID and write the two `CIIO` ACEs with the GUID constants via `samba-tool dsacl set`; add the svc allowed and refused integration cases (including user and computer creation), satisfies **AC-11**.
4. **Locks.** `Lock` and `State` (threshold from the domain, bind by DN, verify the computed lock flag), `seed lock`, `make directory-lock`, locking `locked: true` users at the end of `Apply` before the marker, satisfies **AC-8**, **AC-9**.
5. **Full model and validation.** Every AC-14 rule (domain wide name uniqueness, charset, cycles, single top manager, DN escaping), `admins`, `privileged` with `nested_into`, `groupType` per kind and distribution `mail`, `SYNC-Entra-Users`, overlay merge, `overlay.example.yaml`, satisfies **AC-7**, **AC-10**, **AC-14**, **AC-15**.
6. **Noord's people and mess.** Write `noord.yaml`: 80 staff across the eight departments, 5 leavers with weak hints, the look alike pairs, 3 locked, about 6 unsynced, about 20 groups with the messy finance names and at least 4 unowned; the identical rebuild test and the data hygiene tests; check `make directory-reset` stays under 2 minutes, satisfies **AC-2** to **AC-6**, **AC-16**, **AC-1**.

## Consequences

**Positive**:
- Every run of the eval, CI, and a stranger's clone starts from the same directory, by construction.
- The service account cannot touch admins at the AD level, so the "never grants Domain Admin" claim has two independent walls (this and #9's deny list), and the video can show AD itself refusing.
- The eval reads the truth (leavers, locks, the sync gap) from the same file that built the directory, so expected outcomes cannot drift from reality.
- The seeder is plain Go with a strict loader, so a typo in the YAML fails fast with every problem at once.

**Negative / tradeoffs**:
- Every rebuild pays for a provision, so a run of the eval cannot reseed per ticket, only per run.
- Seeded locks expire after 15 minutes, so a long demo or eval must relock users just in time; forgetting this shows up as a flaky ticket.
- The security skeleton is shell in the entrypoint; only the integration test (several minutes in CI, needs Docker) proves it.
- Samba is not Windows AD: no SDProp, LDAP bind logins instead of Windows logons, and some `samba-tool` behavior differs. The README and video must say "Samba AD", not "Active Directory", when it matters.
- One shared dev password means any fictional user can be impersonated by anyone with `.env`; fine for a fictional domain, wrong for anything real.
- The CI test job gains an image build and a provision on every push.

**Neutral**:
- A fourth binary, `cmd/seed`, joins the module (a dev and ops tool, never a long running service; on k3s it runs as a Job in #17). Spec 0001's "three binaries" line and `AGENTS.md` need a touch (`/sync`).
- `.dev/` becomes the local home for generated certs.
- The `testcontainers-go` skill lives in `.agents/skills/` with a symlink in `.claude/skills/`, unlike the other skills, because that is where `npx skills` installed it.

## Follow-up

- [ ] **#7 Entra**: scope the sync to `SYNC-Entra-Users` (Cloud Sync supports group scoping; confirm when you design #7), and keep the Windows Server host need from 0001 in view for the timebox.
- [ ] **#9 Tool server**: allow list = `OU=Users` and `OU=Groups`; keep `SYNC-Entra-Users` and distribution lists off the grantable list; the deny list must resolve nested membership (`SG-IT-Admins` inside `Domain Admins`); flip the `LDAP_*` settings to required.
- [ ] **#10 Identity check**: match the Slack profile email to `mail`; demo users get real Slack test addresses only through the gitignored overlay.
- [ ] **#17 GitOps install**: Samba as a StatefulSet in `noord-directory` at the baseline pod security level (or privileged if the AC-17 fallback was taken), cert-manager self signed CA Issuer plus a Certificate with the AC-12 SANs, a seed Job (ArgoCD PostSync hook, safe to rerun per AC-13), the Samba image pushed to GHCR for both arches, a NetworkPolicy so only the tool server and the seed Job reach 636, and `SEED_*` passwords as SealedSecrets.
- [ ] **#19 and #20 Eval**: reseed with a fresh container per run (the `sambatest` helper or `make directory-reset`), read expected truth from `directory.Load`, and call `directory.Lock` right before any ticket that needs a locked user.
- [ ] **#23 README**: "rebuild Noord" steps (`make certs`, `make directory-reset`) and the Samba versus Windows AD note.
- [ ] **`/sync`**: add `cmd/seed`, the new make targets, `internal/seed`, and the `testcontainers-go` skill to `AGENTS.md`; record the declined ActiveDirectoryMCP server (it would hold admin credentials outside the tool server) on the `Declined:` line.
- [ ] Verify early (step 1 or 2): Samba counts failed LDAP simple binds toward lockout, and the four schema GUID constants match the schema (`ldbsearch` for `schemaIDGUID`). If either fails, journal it and come back here.
- [ ] Later, not needed by any AC: whether a user can bind by the `@noord.example` UPN (a suffix other than the realm). The tool server and seeder bind by DN, so only #10 or a demo might care.
