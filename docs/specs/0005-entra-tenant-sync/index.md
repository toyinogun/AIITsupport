# 0005. Entra tenant and a Graph sync from Samba AD

**Date**: 2026-10-18
**Status**: Proposed

## Summary

This spec gives Noord a cloud identity half: a dedicated Entra ID tenant (Microsoft's cloud directory) holding the staff who sit in `SYNC-Entra-Users`, with the sync gap left out on purpose. Microsoft's own sync agents need a Windows Server and do not support Samba, so a Go command (`seed entra sync`) does the part of the sync this project needs: it reads the users from Samba over LDAPS and makes Entra match, safely and repeatably, over Microsoft Graph (Microsoft's REST API). Two app registrations split the rights the same way AD does: the seeder can create users, the tool server can only read users and their MFA methods, and change only their phone methods. It is timeboxed to Wed Oct 21; if it is not syncing by then, the AD only fallback is taken and journaled.

## Rationale

Context, the options weighed, the landscape check, and the smaller calls: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As the engineer, I want one command that makes Entra match the synced part of Noord's AD, so that MFA tickets act on real cloud accounts and every eval run starts from the same Entra state.
- As the eval (#19, #20), I want users missing from Entra exactly where the YAML says (the sync gap), so that "not in Entra, escalate" tickets have a checkable truth.
- As Noord's IT person, I want the agent's Entra app unable to create users or touch anyone the sync does not manage (such as the tenant admin), so that a bug in our code cannot reach beyond Noord's staff.
- As the video audience, I want to see a real sign in with a real MFA prompt for a demo user, and that user's MFA cleared through the API.

**Acceptance criteria**:
- **AC-1**: A dedicated Entra tenant exists for Noord only, on its `*.onmicrosoft.com` domain (`SEED_ENTRA_DOMAIN`), with security defaults on and your own admin account protected by MFA. Two app registrations exist with admin consent and exactly these application permissions: the **seed app** `User.ReadWrite.All`, `User.DeleteRestore.All`, `UserAuthMethod-Phone.ReadWrite.All`; the **tool server app** `User.Read.All`, `UserAuthenticationMethod.Read.All`, `UserAuthMethod-Phone.ReadWrite.All`. If a phone scoped permission does not work on evening one, the broader `UserAuthenticationMethod.ReadWrite.All` replaces it for that app, with a journal entry and a spec update. The setup steps live in `docs/runbooks/entra-tenant.md` with no tenant id, client id, or secret in it (including: after granting consent, wait for it to propagate and request a fresh token); the secrets live only in the gitignored `.env`.
- **AC-2**: With Samba seeded, `make entra-sync` makes every direct member of `SYNC-Entra-Users` exist in Entra as a managed user with the fields in the data model below, exits 0 in under 2 minutes on your laptop (measured on evening one; if the serial run cannot meet it, this limit is updated in the spec with the measured time), and logs the counts (created, updated, restored, deleted, phones set, managers set, managers cleared, managers skipped) and the duration.
- **AC-3**: The users with `synced: false` (the sync gap) and every account in `OU=Admin Accounts` are absent from Entra after a sync. If `SYNC-Entra-Users` holds any member outside `OU=Users,OU=Noord`, the sync exits 1 naming it, before any Graph write.
- **AC-4**: Each managed user's `manager` points to its AD manager's Entra user when that manager is in scope. When the manager is in the sync gap, `manager` is empty and the sync logs `manager not in scope` with both usernames.
- **AC-5**: Each managed user has a `mobile` phone method with its expected number: the demo file's `mfa_phone` for demo users, otherwise the generated fictional number `+31 60000NNNN`, where `NNNN` is the user's position (starting at 1, zero padded to 4) in the bytewise sorted list of every `users:` username in `noord.yaml` (staff only, not `admins:`). The sync reads the user's phone methods first, then PATCHes a wrong `mobile` number or POSTs a missing one; it never touches other phone types or other method types.
- **AC-6**: The sync reconciles. A second run with nothing changed makes zero Graph writes. After drift (a field edited in Entra, a phone method deleted, a managed user deleted into the recycle bin), the next run puts it back; a deleted managed user is restored from the recycle bin, not created again.
- **AC-7**: Managed users no longer in scope are deleted (soft delete, into the recycle bin). If a run would delete more than `SEED_ENTRA_DELETE_LIMIT` (default 10), it exits 1 before any write. Users without the managed marker are never edited, restored, or deleted; if one already holds an in scope user's `mailNickname` or UPN, the sync exits 1 naming it, before any write. Two managed users (live or in the recycle bin) sharing a `mailNickname` also exit 1 before any write.
- **AC-8**: Demo users live in their own gitignored file, `seed/entra/demo.local.yaml` (`SEED_ENTRA_DEMO_FILE`; a missing file means no demo users), each entry a `username` plus `mfa_phone`. Validation fails unless: at most 2 entries, each a `synced: true` user in `noord.yaml`, numbers distinct and matching `^\+\d{1,3} \d{6,12}$`. The tracked `seed/entra/demo.example.yaml` uses only `+31 60000NNNN` numbers. `SEED_ENTRA_DEMO_PASSWORD` is checked at config load (at least 12 characters, at least 3 of upper, lower, digit, symbol, no token of 3+ letters from any staff name) and is required only when the demo file has entries. New demo users get it at create, with no forced change; `seed entra demo-password <username>` (`make entra-demo-password NAME=`) sets it on an existing demo user. Every other user gets a random 32 character password at create (at least one upper, one lower, one digit) that is never logged, stored, or reused. Signing in as a demo user at `myaccount.microsoft.com` reaches an MFA step: a challenge sent to the seeded phone, or MFA registration (manual check, and the pass condition is whichever evening one shows; if security defaults force Authenticator registration, switch them off for an authentication methods policy with SMS enabled, and journal it).
- **AC-9**: `make entra-mfa NAME=<username>` uses only the tool server app (`GRAPH_*`) and lists that user's auth methods (type, and phone numbers masked to their last 2 digits). With `CLEAR=1` it deletes every phone method the user has (`mobile`, `alternateMobile`, `office`), then polls the list (every 2s, up to 30s) until no phone method remains, and prints the final list. The next `make entra-sync` restores the `mobile` method.
- **AC-10**: `internal/graph` resolves a user only by `mailNickname` and refuses any user without the managed marker (`employeeType` = `noord-sync`) with `ErrNotManaged`, and an unknown one with `ErrNotFound`, before any auth method call. Asking `make entra-mfa` for your tenant admin's nickname fails with "not managed".
- **AC-11**: The tool server app cannot create, update, or delete a user, and cannot issue sign in credentials: with the `GRAPH_*` credentials, `POST /users` and `POST /users/{id}/authentication/temporaryAccessPassMethods` on a managed user both get 403. A live probe also records whether Graph lets the app read or change your tenant admin's phone methods (the result goes in the journal and the runbook; the AC-10 fence stops our code either way).
- **AC-12**: Graph failures are handled: every Graph request has its own timeout (`SEED_GRAPH_TIMEOUT` / `GRAPH_TIMEOUT`) and calls run one at a time; 429 and 503 wait for `Retry-After` (or back off) up to the configured retry count; a 404 on an object created moments ago (Graph replication lag) is retried the same way; a create that returns an object conflict (the user exists but the list was stale) reads the user again and continues as restore or update; a bad or expired secret fails naming the setting, never the value; Samba not answering fails naming the URL before any Graph call. A run killed halfway leaves no special state; the next run converges.
- **AC-13**: With `SEED_GRAPH_*` unset, `make entra-sync` prints "Entra not configured, skipping" and exits 0. `make noord-reset` runs `directory-reset` then `entra-sync`, so it works offline, in CI, and after the fallback. The tool server already boots with `GRAPH_*` empty.
- **AC-14**: Timebox: by the end of Wed Oct 21, either AC-1 to AC-16 pass (the manual sign in in AC-8 excepted, and AC-4 may be cut per the build plan), or the fallback is taken: a journal entry records why, the scope marks #7 as fallback, and #14 escalates MFA tickets with context instead.
- **AC-15**: No secret, tenant id, client id, or real phone number is in a tracked file. Generated numbers all match `+31 60000NNNN`; the hygiene test fails if `noord.yaml` gains a phone field or `demo.example.yaml` holds a number outside `+31 60000NNNN`. Logs never print a full phone number or any password.
- **AC-16**: Without network, unit tests cover the plan (diff of AD vs Entra into actions), the number generator, demo file validation, the marker fence, and retry handling against an `httptest` fake Graph; CI runs them. Live tests run only with `TEST_ENTRA=1` on your laptop, never in CI.

## Decision

**Chosen option**: Option 1: our own Graph sync from Samba AD, in `cmd/seed`.

A reconciling `seed entra sync` reads `SYNC-Entra-Users` live from Samba and makes a dedicated Entra tenant match it through two least privilege app registrations, with MFA phone methods seeded from the YAML and a managed marker fencing everything else in the tenant.

**Implementation skills**: `golang-security` (`samber/cc-skills-golang`, `.claude/skills/golang-security/`) · `golang-error-handling` (`samber/cc-skills-golang`, `.claude/skills/golang-error-handling/`) · `testcontainers-go` (`testcontainers/claude-skills`, `.agents/skills/testcontainers-go/`, for the live Samba side of the live tests)

## Feature design

**Data model sketch** (no Postgres tables; Entra is the store, and reconcile needs no marker or state of its own):

| Entra user field | Source | Rule |
|---|---|---|
| `mailNickname` | AD `sAMAccountName` | **Anchor.** Unique among managed users; the sync and the tool server match on it |
| `userPrincipalName` | `<sAMAccountName>@<SEED_ENTRA_DOMAIN>` | `noord.example` cannot be verified, so the tenant domain is used; compared case insensitively; a changed domain is ordinary drift (PATCH) |
| `displayName`, `givenName`, `surname` | AD `displayName`, `givenName`, `sn` | required |
| `department`, `jobTitle` | AD `department`, `title` | nullable; a blank or missing AD value PATCHes the Entra field to `null`; leavers keep their stale hints |
| `manager` (reference) | AD `manager` → that user's Entra id | empty when the manager is out of scope |
| `accountEnabled` | AD `userAccountControl` disabled bit (`0x2`) | enabled leavers stay enabled |
| `companyName` | constant `Noord Logistics` | |
| `employeeType` | constant `noord-sync` | **Managed marker**; only marked users are ever written |
| `usageLocation` | constant `NL` | |
| `passwordProfile` | demo: `SEED_ENTRA_DEMO_PASSWORD`; others: random 32 characters | set at create (or by `seed entra demo-password`), never by a sync update; `forceChangePasswordNextSignIn: false` |
| phone method `mobile` | demo file `mfa_phone`, else generated `+31 60000NNNN` | one per user (Graph allows one `mobile`), restored every run |

Compared fields for `update`: every row above except `passwordProfile` and the phone method (which has its own read and write). Comparison is exact after normalising AD blanks to `null`, except UPN (case insensitive).

Demo file (`seed/entra/demo.local.yaml`, gitignored; example `seed/entra/demo.example.yaml`, tracked): `users: [{username, mfa_phone}]`. It is loaded by `internal/seed/entra`, not by the 0004 loader, so `overlay.local.yaml`, its validation, and the directory marker are unchanged, and editing a demo phone never forces `directory-reset`. `noord.yaml` gains nothing.

**Reconcile plan** (pure function, unit tested): inputs are the AD scope (members of `SYNC-Entra-Users` with their attributes and `userAccountControl`), all Entra users (`GET /users` with `$select` of the mapped fields plus `id` and `$expand=manager($select=id)`, paged, filtered to the marker in code), deleted users (`GET /directory/deletedItems/microsoft.graph.user` with `$select=id,mailNickname,userPrincipalName,employeeType`), and per managed user its phone methods (one GET each). Output is an ordered action list: `restore`, `create`, `update` (only changed fields), `delete`, then `set phone`, then `set manager` and `clear manager` once every id is known. Guards (AC-3, AC-7, duplicate managed `mailNickname`) run on the plan before the first write.

`--only <username>`: the same plan restricted to that one user: no delete pass and no delete limit, the scope guard on that user only, the phone set, and the manager set only if the manager already exists in Entra (else logged as skipped).

**State transitions** (one managed user, per run): `absent` → `create` · `in recycle bin` → `restore` then `update` · `present, drifted` → `update` · `present, out of scope` → `delete` (into the recycle bin). An unmanaged user is never transitioned.

**API surface** (commands and make targets; no HTTP endpoints of our own):

| Command | Make target | Key inputs | Key outputs | Credentials | Key errors |
|---|---|---|---|---|---|
| `seed entra sync` | `make entra-sync` | `SEED_*` LDAP settings, `SEED_GRAPH_*`, `SEED_ENTRA_*`, seed, overlay, and demo files; flag `--only <username>` (thin thread, debugging) | summary log, exit 0 | seed app; Samba Administrator | exit 1: scope guard, delete limit, unmanaged collision, auth failure, Samba down |
| `seed entra mfa [--clear] <username>` | `make entra-mfa NAME= [CLEAR=1]` | `GRAPH_*` | method list (masked) before and after | tool server app | not managed, not found, 403, clear not settled after 30s |
| `seed entra demo-password <username>` | `make entra-demo-password NAME=` | `SEED_GRAPH_*`, demo file, `SEED_ENTRA_DEMO_PASSWORD` | one PATCH of `passwordProfile` | seed app | not a demo user, not managed |
| (none) | `make noord-reset` | as both above | fresh Samba, then a sync | as both above | first failing step's error |

Graph calls used (v1.0):
- Reads: `GET /users?$select=...&$expand=manager($select=id)` (paged), `GET /users?$filter=mailNickname eq '<x>'`, `GET /directory/deletedItems/microsoft.graph.user?$select=id,mailNickname,userPrincipalName,employeeType`, `GET /users/{id}/authentication/methods`, `GET /users/{id}/authentication/phoneMethods`.
- Writes (seed app): `POST /users`, `PATCH /users/{id}`, `DELETE /users/{id}`, `POST /directory/deletedItems/{id}/restore`, `PUT /users/{id}/manager/$ref` with body `{"@odata.id": "https://graph.microsoft.com/v1.0/users/{managerId}"}`, `DELETE /users/{id}/manager/$ref`, `POST /users/{id}/authentication/phoneMethods`, `PATCH /users/{id}/authentication/phoneMethods/{methodId}`.
- Writes (tool server app): `DELETE /users/{id}/authentication/phoneMethods/{methodId}`.

**Package layout**:
- `internal/graph` (tool server side, already on both deny lists): token via `azidentity.ClientSecretCredential`, the HTTP transport with retries and paging, error mapping, `ManagedMarker`, `UserByUsername` (with the AC-10 fence), `AuthMethods`, `DeletePhoneMethod`. Feature #14 builds on it.
- `internal/seed/entra`: AD scope read (reusing `internal/seed/directory`'s connection), the reconcile plan, and the write calls, built on `graph.Client`'s transport with the seed credentials. User writes live here only, never in `internal/graph`.
- `cmd/seed`: the `entra sync` and `entra mfa` subcommands.

**Value sourcing**:

| Action | Value produced / displayed | Source |
|---|---|---|
| sync | which users are in scope | live `member` of `CN=SYNC-Entra-Users,OU=Groups,OU=Noord` (bound as Administrator, `SEED_ADMIN_*`) |
| sync | every mapped field | AD attribute per the data model table |
| sync | UPN suffix | `SEED_ENTRA_DOMAIN` |
| sync | manager's Entra id | Entra id of the managed user whose `mailNickname` = the AD manager's `sAMAccountName`, from the same run |
| sync | expected phone number | demo file `mfa_phone`, else generated from the bytewise sorted `users:` usernames of the spec 0004 `Model` (position starting at 1) |
| sync | demo user set, demo password | `SEED_ENTRA_DEMO_FILE` entries; `SEED_ENTRA_DEMO_PASSWORD` |
| sync | random password | `crypto/rand`, 32 characters from letters, digits, and `-_!`, with at least one upper, one lower, and one digit forced, generated per create and discarded |
| sync | "managed" | `employeeType` = `noord-sync` (constant `graph.ManagedMarker`) |
| sync | current manager, deleted users, current phones | `$expand=manager`, `deletedItems` with `$select`, phone methods GET (see Graph calls) |
| sync | manager skip log line | `manager not in scope`, with the user's and the manager's usernames |
| sync | delete limit, retries, per request timeout | `SEED_ENTRA_DELETE_LIMIT`, `SEED_GRAPH_MAX_RETRIES`, `SEED_GRAPH_TIMEOUT` |
| entra mfa | user id | `UserByUsername(NAME)` via `GRAPH_*` |
| entra mfa | masked number | method's `phoneNumber`, all but the last 2 digits replaced |
| entra mfa | "cleared" | poll of `phoneMethods` every 2s up to `SEED_ENTRA_CLEAR_WAIT` |
| demo-password | the password | `SEED_ENTRA_DEMO_PASSWORD` |

**Key invariants**:
- Only users carrying the managed marker are ever created, patched, restored, or deleted.
- `mailNickname` is unique among managed users, live and deleted; the sync exits 1 before any write rather than guess between two.
- Scope is only direct members of `SYNC-Entra-Users` inside `OU=Users,OU=Noord`; nothing in `OU=Admin Accounts` ever reaches Entra.
- Passwords are written at create, or by the explicit `demo-password` command, never by a sync update (so a demo user who changes theirs keeps it, and a rerun stays at zero writes).
- One Graph call at a time; no concurrency in the sync.
- No more than `SEED_ENTRA_DELETE_LIMIT` deletes per run.
- MFA methods are never synced from AD (none exist there); the generator and the demo file are their only source, as in real hybrid setups.

**Security model**:
- The seed app (create, update, delete, restore users, write phone methods) is used only by `cmd/seed` through `SEED_GRAPH_*`, read only by `config.Seed`. The agent can never import `internal/seed` or `internal/graph` (both deny lists).
- The tool server app holds `User.Read.All`, `UserAuthenticationMethod.Read.All`, and `UserAuthMethod-Phone.ReadWrite.All` through `GRAPH_*`: it can read every method type but write only phone methods, so it cannot issue a Temporary Access Pass (a one time sign in code) or reset a password (AC-11). These rights apply tenant wide, so the managed marker check in `internal/graph` is the fence around your admin account (a code fence; the Entra side wall is a follow up).
- Client secrets live in `.env` only (gitignored; gitleaks scans every commit). Secret lifetime: 6 months, rotation in the runbook.
- Only fictional data enters the tenant. Your real phone number exists only in the gitignored demo file and in Entra on at most 2 demo users.
- GDPR: no real personal data is processed; the demo phone is yours, by choice.

**Configuration required**:
- `GRAPH_TENANT_ID`, `GRAPH_CLIENT_ID`, `GRAPH_CLIENT_SECRET` (exist): move into a `Graph` struct embedded in `ToolServer`, with a `LoadGraph` that `seed entra mfa` uses; still optional (fallback).
- `GRAPH_MAX_RETRIES` (default `5`), `GRAPH_TIMEOUT` (default `30s`): new, in `Graph`.
- `SEED_GRAPH_TENANT_ID`, `SEED_GRAPH_CLIENT_ID`, `SEED_GRAPH_CLIENT_SECRET`: new, in `Seed`, optional; all unset means "Entra not configured" (AC-13), partly set fails.
- `SEED_ENTRA_DOMAIN` (e.g. `<name>.onmicrosoft.com`), `SEED_ENTRA_DEMO_PASSWORD` (validated as in AC-8), `SEED_ENTRA_DELETE_LIMIT` (default `10`), `SEED_GRAPH_MAX_RETRIES` (default `5`), `SEED_GRAPH_TIMEOUT` (per request, default `30s`), `SEED_ENTRA_CLEAR_WAIT` (default `30s`): new, in `Seed`.
- `SEED_ENTRA_DEMO_FILE` (default `seed/entra/demo.local.yaml`): new, in `SeedFiles` (no secret), so `seed validate` checks it too.
- `.gitignore` gains `seed/entra/demo.local.yaml`.
- `TEST_ENTRA=1`: turns on the live tests (like `TEST_SAMBA`).
- `.env.example` gains every name above with empty secret values (the drift test enforces it).
- Prerequisite before coding: the tenant and both app registrations, per `docs/runbooks/entra-tenant.md`.

**Failure handling**:
- 429 or 503: wait `Retry-After` when sent, else exponential backoff with jitter, up to the max retries, then fail naming the call.
- 404 just after a create or restore: retried as above (replication lag), then fail.
- Object conflict on create (a stale list): read the user by `mailNickname` (live, then deleted) and continue as update or restore; an unmanaged holder exits 1 (AC-7).
- A `mobile` POST that conflicts (one already exists): read the methods again and PATCH.
- A phone method delete refused (e.g. while SMS sign in is enabled for it): fail naming the method type; journal it on evening one.
- 401 or `AADSTS7000222` (expired secret): fail with "client secret for `SEED_GRAPH_CLIENT_ID` rejected or expired, see the runbook".
- 403 on an auth methods call on evening one: a licence gap, take the P1 trial (decided) and journal it.
- Samba down: fail naming `SEED_LDAP_URL` before any Graph call.
- A run killed halfway: the next run converges, since every action is planned from current state.
- Partly set `SEED_GRAPH_*`: config error listing the missing names.

**Critical test scenarios** (live ones need `TEST_ENTRA=1` and a seeded Samba):
- Happy path: fresh `make noord-reset`, then every `SYNC-Entra-Users` member is managed in Entra with the mapped fields, manager, and phone; the gap users and `adm.*` are absent, verifies **AC-2**, **AC-3**, **AC-4**, **AC-5**.
- Rerun: a second sync makes zero writes, verifies **AC-6**.
- Drift: patch a department in Entra, delete a phone method, soft delete a user; the next sync restores all three (the user by restore, same object id), verifies **AC-6**.
- Out of scope and limits: remove a user from the group in Samba, sync deletes it; a plan with 11 deletes exits 1 with no writes; an unmanaged user holding an in scope `mailNickname` exits 1, verifies **AC-7**.
- Scope guard (unit and live): a group member in `OU=Admin Accounts` exits 1 before writes, verifies **AC-3**.
- MFA proof: `entra mfa --clear` through `GRAPH_*` removes the phone; a sync restores it, verifies **AC-9**.
- Fence: `UserByUsername` on the tenant admin's nickname returns `ErrNotManaged`; on a made up name `ErrNotFound`, verifies **AC-10**.
- Least privilege: `POST /users` and a Temporary Access Pass POST with the tool server app both get 403; the tenant admin probe result is recorded, verifies **AC-11**.
- Failure (unit, `httptest`): 429 with `Retry-After` then 200 succeeds; 404 after create then 200 succeeds; endless 503 fails after the max retries; 401 names the setting; create conflict then a read continues as update; `mobile` POST conflict then PATCH, verifies **AC-12**, **AC-5**.
- Plan guards (unit): duplicate managed `mailNickname` (live plus deleted) exits 1 with no writes; `--only` plans no deletes and skips a missing manager, verifies **AC-7**, **AC-4**.
- Unconfigured: no `SEED_GRAPH_*` prints the skip line and exits 0, verifies **AC-13**.
- Demo file, passwords, and hygiene (unit): a demo user not synced, three demo users, a shared number, and a number failing the pattern all fail; a weak or name containing demo password fails config load; 10,000 generated passwords all have upper, lower, and digit; generated numbers match the pattern and number staff only; no phone field in `noord.yaml`; masking never shows more than 2 digits, verifies **AC-5**, **AC-8**, **AC-15**, **AC-16**.

## Build plan

Tracer Bullet inside a two evening timebox: evening one proves the thinnest real thread (one user synced, its MFA cleared by the tool server app on a Free tenant), which is also the go or no go on licensing; evening two thickens it to the full reconcile.

**Evening one (Mon Oct 19): the thread and the licence question**
1. **Tenant and apps.** Create the tenant (free Azure account route), set up security defaults and your admin MFA, register both apps with the AC-1 permissions and admin consent, make 6 month secrets, and write `docs/runbooks/entra-tenant.md` as you go (including secret rotation). Journal anything that costs time, satisfies **AC-1**.
2. **Config and transport.** `Graph` struct plus `LoadGraph`, the new `Seed` fields, `.env.example`. `internal/graph`: token, transport with retries and paging, error mapping, `ManagedMarker`, `UserByUsername` with the fence, `AuthMethods`, `DeletePhoneMethod`, with `httptest` unit tests, satisfies **AC-10**, **AC-12**, **AC-16**.
3. **Thin sync and the MFA proof.** `internal/seed/entra` with the AD scope read and a create plus phone path; `seed entra sync --only <username>`; `seed entra mfa`; `make entra-sync`, `make entra-mfa`. Run it for one user: the seed app adds the phone, the tool server app lists and clears it, and a Temporary Access Pass POST gets 403. Sign in once as that user to see which MFA step security defaults show (AC-8's pass condition). Time a create plus phone to project the full run against AC-2. **Go or no go:** a licence error on the auth methods calls means start the P1 trial; a phone scoped permission that fails means the broad one plus a spec update; journal each, satisfies **AC-5**, **AC-9**, **AC-11**, part of **AC-2**.

**Evening two (Tue Oct 20): the full reconcile**
4. **Full plan, without managers.** All mapped fields with the null rule, `update` with changed fields only, the scope guard, the collision and duplicate nickname guards, restore from the recycle bin, the create conflict path, delete with the limit, the summary log; table tests for the plan, satisfies **AC-2**, **AC-3**, **AC-6**, **AC-7**.
5. **Demo users.** The demo file and its validation, the demo password check, `seed entra demo-password`, the random password generator, the manual sign in, satisfies **AC-8**.
6. **Reset, live tests, hygiene.** `make noord-reset`, the unconfigured skip, the `TEST_ENTRA=1` live tests (rerun, drift, limits, fence, least privilege, admin probe), the hygiene and masking tests, satisfies **AC-11**, **AC-13**, **AC-15**, **AC-16**.
7. **Manager pass (last, first to cut).** `$expand=manager` read, `set manager` and `clear manager` after all ids exist, the skip log, the `--only` rule. If the evening runs out, it ships as "managers not synced" (logged once per run) and AC-4 moves to a follow up, satisfies **AC-4**.

**Wed Oct 21: checkpoint**
8. **Call it.** Run the critical scenarios. Green: move to `/check verify`. Not green: take the fallback (journal entry, scope #7 marked fallback, #14 escalates MFA tickets), satisfies **AC-14**.

## Consequences

**Positive**:
- Fits the timebox: no Windows Server, no unsupported agent, nothing new to run on Proxmox.
- The tenant rebuilds to a known state with one command, so the eval's MFA tickets are repeatable.
- The rights split mirrors AD (Administrator vs `svc-itsupport`), so the safety story is the same on both halves.
- `internal/graph` lands with its fence already tested, so #14 only adds the approval flow.

**Negative / tradeoffs**:
- It is not Microsoft's sync: Entra users are cloud only (`onPremisesSyncEnabled` is false), there is no password hash sync, and no `onPremises*` attributes. The README and video must say "a Graph sync that stands in for Cloud Sync", or the hybrid claim is overstated.
- The tool server app's rights are tenant wide; only our code fence protects your admin account until the AU scoped role follow up lands. Phone only writes also mean #14 can clear phones but not an Authenticator registration without widening the app.
- One call at a time keeps the code simple but makes the run slower (about 300 calls); AC-2's limit rests on the evening one measurement.
- The live tests mutate a real internet tenant and depend on Graph being up; they cannot run in CI.
- Adding a user to `noord.yaml` renumbers the generated phones after it; the next sync rewrites those numbers (harmless, but noisy in the log).
- A client secret is a weaker credential than a certificate until #17.

**Neutral**:
- `internal/seed/AGENTS.md` and `internal/config/AGENTS.md` need the new commands and settings (`/sync` owns that).
- `GRAPH_*` moves into an embedded struct; `cmd/seed` now reads the tool server's Graph settings for the MFA proof only.

## Follow-up

- [ ] **Entra side wall** (after #14): give the tool server app Authentication Administrator scoped to a "Noord Staff" administrative unit instead of tenant wide `UserAuthenticationMethod.ReadWrite.All`; check whether the P1 trial covers it.
- [ ] **#14 scope of "clear MFA"**: if #14 must also remove Authenticator registrations, widen the tool server app deliberately in that spec (and add a Temporary Access Pass guard in code); #7 ships phone only.
- [ ] **#17**: move both apps to certificate credentials when Sealed Secrets arrives; drop the client secrets.
- [ ] **#14**: build on `internal/graph`; MFA tickets for users in the sync gap escalate with "not in Entra".
- [ ] **#19**: add MFA tickets for gap users and for a user whose manager is unsynced.
- [ ] **#22 / #23**: the runbook feeds the handover; the README must describe the Graph sync honestly (Negative, first bullet).
- [ ] **Unconfirmed until evening one** (from the landscape check and the cross check): app only phone method create and delete on a Free tenant, the phone scoped permissions, `employeeType` and `companyName` writable on cloud users, `mailNickname` filter support, soft deleted users holding their UPN, phone delete refused while SMS sign in is on, which MFA step security defaults show a seeded user. Each one that fails is a journal entry and a spec update.
- [ ] `/sync`: add `make entra-sync`, `make entra-mfa`, `make entra-demo-password`, `make noord-reset`, and `TEST_ENTRA=1` to the root `AGENTS.md` commands, and the Entra settings to `internal/config/AGENTS.md`.
