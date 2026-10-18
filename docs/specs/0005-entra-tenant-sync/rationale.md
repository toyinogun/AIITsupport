# 0005. Rationale: Entra tenant and a Graph sync from Samba AD

Decision record for [index.md](index.md). The build reads index.md only.

## Context

> ⚠️ Premise note: the scope asks for users "synced" to Entra, which reads as Microsoft's sync. Both Microsoft agents need a domain joined Windows Server, and neither lists Samba AD as a supported source, so the real thing is unproven here, not just inconvenient. What the agent and the eval actually need is a cloud directory holding the right users (and missing the right ones) with real MFA methods. This spec builds that and names it honestly; the real agent stays out of scope.

Noord is mid migration: its on prem AD (Samba, spec 0004) is partly synced to Entra ID, and registering MFA again after a phone change is one of the five pain points in the discovery doc. Features #14 (MFA with approval) and #19 (the eval) need a cloud tenant where Noord's synced staff exist with MFA methods that can be cleared through Graph, and where about 6 people are missing (the sync gap, already marked by `SYNC-Entra-Users` in 0004).

The forces:
- **Timebox.** Two evenings, ending Wed Oct 21, with an AD only fallback. Anything that may eat both evenings is a scope risk, not a detail.
- **Repeatability.** The eval replays 50 tickets against a fresh environment. Samba is rebuilt from nothing each time; an internet tenant cannot be, so it must converge to a known state instead.
- **Safety claim.** The tool server is the only holder of client credentials (0001), and 0004 fenced its AD account with directory permissions. The Entra half needs the same split, in a tenant that also holds your own admin account.
- **Public repo and fictional data.** No tenant ids, secrets, or real phone numbers in git.
- **Unknowns.** Licensing for app only auth methods writes on a Free tenant is unconfirmed, and new tenants now come through a free Azure account.

## Options considered

### Option 1: our own Graph sync from Samba AD (chosen)

A Go `seed entra sync` reads `SYNC-Entra-Users` from Samba over LDAPS and reconciles a dedicated tenant through Graph.

**Pros**: no Windows, no unsupported agent; reconciles to a known state for the eval; reuses `cmd/seed`, its config, and its AD connection; two least privilege apps mirror the AD split.
**Cons**: not Microsoft's sync (cloud only users, no `onPremises*` attributes, no password hash sync); we own the reconcile logic and its edge cases (recycle bin, replication lag).

### Option 2: real Cloud Sync agent on a Windows Server VM

Windows Server 2022 evaluation VM on Proxmox, joined to Samba, running the Cloud Sync provisioning agent scoped to `SYNC-Entra-Users`.

**Pros**: the most authentic hybrid story; group scoping is a native feature.
**Cons**: needs a gMSA (KDS root key) and `msDS-ExternalDirectoryObjectId` in Samba's schema, both unconfirmed on Samba; Samba must be reachable from Proxmox (today it listens on `127.0.0.1` in Compose); a Windows VM to patch; no clean way to reset per eval run. Very likely to eat both evenings.

### Option 3: spike the real agent, then fall back to Option 1

Evening one on Option 2; if not syncing, evening two builds Option 1.

**Pros**: authentic if it works; the failure is good journal material.
**Cons**: evening two must then fit the whole of Option 1, including the licence question that Option 1 answers on evening one. The likely outcome is the AD only fallback by default.

### Option 4: AD only now, Entra as phase 2

**Pros**: zero risk to the timeline.
**Cons**: MFA tickets (10 of the 50 eval tickets) only ever escalate, and the "hybrid, mid migration" part of the story is gone.

## Rationale

The timebox and the eval's need for repeatability decide it. Option 2's blockers (gMSA and schema on Samba, network reach from Proxmox, no reset) are each an unknown with no fallback inside two evenings, and even a success leaves a tenant the eval cannot reset. Option 1 answers the only question that matters for #14 (can an app clear MFA through Graph on this tenant) on evening one, and delivers a sync gap that is checkable from the same YAML the eval reads. Its cost is honesty in how it is described, which the Consequences and the README follow up carry.

The smaller calls, made with you in the conversation:
- **Tenant**: a brand new dedicated tenant (runner up: an M365 developer sandbox, which now mostly turns individuals away and may hold sample users).
- **Source**: live from Samba, so a change in AD shows up on the next run like a real sync (runner up: the YAML, a second seed that can drift from AD). MFA phones come from YAML and overlay, as MFA methods never sync in real life either.
- **Apps**: two, split like Administrator and `svc-itsupport` (runner up: one app, which hands the tool server user creation rights).
- **Fence**: the managed marker checked in `internal/graph` now, an AU scoped role later (runner up: the AU role now, likely P1 only and unconfirmed on Free).
- **Credential**: client secret now, certificate in #17 (runner up: certificate now, extra setup inside the timebox).
- **Trigger**: on demand plus `make noord-reset` (runner up: a loop, which would race the eval's resets).
- **Rerun**: reconcile to the YAML (runner up: wipe and recreate, which fights the 30 day recycle bin).
- **Sign in**: one or two demo users with a known dev password (runner up: nobody signs in, which loses the real MFA prompt on video).
- **Mess**: sync gap only (runners up: a cloud only orphan, stale drift; both left for later to protect the timebox).
- **Licence gap**: start the P1 trial; 30 days from Oct 19 or 20 covers the engagement to Nov 15.
- **CI**: live tests local only (runner up: a protected GitHub environment, which puts tenant secrets in CI for a public repo).

Calls made at write time:
- **Anchor `mailNickname`**: it is required at create, always writable, and equals the AD username; UPN cannot be the anchor because its suffix differs from AD (runner up: `employeeId`, unconfirmed as writable).
- **Marker `employeeType` = `noord-sync`**: a standard, filterable user property that no other tooling sets (runner up: `companyName`, which a human might reasonably set on any Noord user).
- **List all users and filter in code**: under 100 users, so no advanced query headers (runner up: `$filter` on `employeeType` with `ConsistencyLevel: eventual`).
- **Soft delete and restore, never permanent delete**: restoring keeps the object id stable across eval runs, and the seed app needs no purge right (runner up: purge from `deletedItems`, which is irreversible).
- **Delete limit 10**: the gap is about 6, so 10 allows a deliberate change but stops a broken scope read from deleting everyone (the same idea as Connect Sync's accidental delete threshold).
- **Generated phones by sorted username position**: stable, collision free, needs no YAML field (runner up: a hash of the username, which collides about a third of the time at 80 users with 4 digits).
- **Secret lifetime 6 months**: under Microsoft's recommended 12, past the engagement end.
- **Package split**: user writes stay in `internal/seed/entra`, so even the code the tool server imports has no user create or delete calls.

## Update (Oct 18, 2026): the fictional phone range

The first real sync created a user, then Graph refused its phone with `400 invalidPhoneNumber`: "formatted properly, but is an invalid phone number" (journal 0013). Graph checks that a number could exist, and `+31 60` is not an allocated Dutch mobile range (Dutch mobiles start `+31 61` to `+31 65` or `+31 68`). The fake Graph in the unit tests stored any number, so they never caught it.

Options weighed:

- **Ofcom's drama range, `+44 7700 900000` to `900999` (chosen).** The UK regulator reserves these numbers for fiction, so they can never reach a real person, which keeps AC-15's "fictional only" promise. They were expected to pass as UK mobiles, but they did not; see the second update below. 1,000 numbers is plenty for 85 staff. The cost is cosmetic: Dutch staff with UK numbers.
- A real but unused looking Dutch range such as `+31 61...`: valid to Graph, but those numbers belong to real subscribers, and an SMS sign in could text a stranger. Rejected under AC-15.
- Seeding no phone for non demo users: breaks AC-5 and the MFA tickets that need a phone to clear. Rejected.

The engineer chose the drama range. The format keeps the one space shape the demo file already validates (`+44 7700900NNN`), with a 3 digit position instead of 4.

### Seventh update (Oct 18, 2026): Microsoft retires its SMS and voice delivery

The portal announced, and Microsoft Learn confirms (page updated Sep 23, 2026), that Microsoft stops delivering SMS and voice MFA on Feb 1, 2027 (Jul 1, 2027 for Global Administrators and external users) and has made passkeys the default since Sep 1, 2026. It does not retire phone methods or the Graph `phoneMethods` API. Two consequences: the cross check's "SMS only for a demo users group" control was dropped (texts are blocked here and retiring), and #14's "clear MFA" gets a dated reason to cover passkeys and authenticator apps (Follow-up). The page does not mention error 399287, so the block is not attributed to the retirement.

### Sixth update (Oct 18, 2026): how the demo sign in reaches MFA

The first demo sign in went straight in with no second step: security defaults only challenge ordinary users when a sign in looks risky. Options weighed: per user MFA on the demo users (free, permanent, needs security defaults off; chosen), the P1 trial with a Conditional Access policy (realistic, but the trial ends around Nov 17 and the policy with it), or recording "no MFA step" and skipping it in the video. With per user MFA on and SMS enabled in the authentication methods policy, the sign in reached "Verify your identity" offering a text to the seeded number, but Microsoft refused to send it (error 399287, which Microsoft staff on Q&A tie to telephony fraud protection, a "bad reputation" block that no admin setting lifts). The engineer chose to register an authenticator app on the demo user instead (journal 0017). AC-1 also now records the Default Directory as Noord's tenant (journal 0012).

### Fifth update (Oct 18, 2026): no password reset command

`seed entra demo-password` got `403 Authorization_RequestDenied` on the real tenant. Graph's Update user docs: `User.ReadWrite.All` covers every user property except `passwordProfile`; changing an existing user's password needs `User-PasswordProfile.ReadWrite.All`, and in app only mode also at least the User Administrator directory role. Setting a password at create needs neither, which is why creates worked (journal 0016).

Options weighed: give the seed app that permission and the role (one command resets demo passwords, but the seed app could then reset any non admin password in the tenant); recreate the demo user instead of resetting it (no new rights, more code, a new object id each time); or drop the command (chosen). Demo users still get the demo password at create, and the rare reset of an existing demo user is a manual portal step, so the seed app keeps exactly the rights AC-1 lists.

### Fourth update (Oct 18, 2026): AC-6 and Graph's read lag

The live drift test showed that for tens of seconds after a write, Graph's reads still return the old state, and its read paths catch up at different times (journal 0015). A sync cannot fix drift it cannot see, so "the next run puts it back" was not something any code could promise. AC-6 now says the next run that can see the drift puts it back, and the live test syncs until a run makes no writes, then checks the totals.

### Third update (Oct 18, 2026): the AC-2 time limit

The first full run on the real tenant took 119,018 ms: 78 creates, 78 phones, and 78 manager links (234 writes), one call at a time. A read only rerun took 24,926 ms, so the writes cost about 0.4 s each, which is normal Graph latency rather than retries. That passes a 2 minute limit by about a second, so a slower day would fail it at random. The engineer raised the limit to 3 minutes, as AC-2 allows, instead of adding concurrency the spec ruled out.

### Second update (Oct 18, 2026): the drama range failed too

The rerun with `+44 7700900017` got the same `400 invalidPhoneNumber`. A check with Google's libphonenumber (the `phonenumbers` Python port, 9.0.41) explains both failures: it marks `+31 600000017` and `+44 7700900017` invalid, because neither range is allocated to a carrier. Ranges reserved for fiction are, by design, mostly unallocated, so libphonenumber style validation rejects most of them. The same check marked these valid: `+1 2025550117` (the North American 555-0100 to 555-0199 range, reserved for fiction), `+61 491570017` (Australia reserves only a few scattered drama numbers, too few for 85 staff), and `+46 701740617` (a Swedish reservation that could not be confirmed).

A live probe then set `+1 2025550117` on `eva.peters` through the seed app, and Graph accepted it (`phones_set: 1`). Chosen: `+1 20255501NN` with a 2 digit position, which holds 99 staff (Noord has 85). Lesson for the spec: check a fictional range against the real validator before writing it into acceptance criteria.

## Cross check (Oct 18, 2026)

An independent read only review on a different model found 12 gaps, all applied with your approval: the demo users moved out of the 0004 overlay into their own file (the overlay requires `mail` and its hash is the directory marker); the reads behind "zero writes" (`$expand=manager`, `deletedItems` with `$select`, phone GETs) and the compare rules are now named; phone writes are read then PATCH or POST; the tool server app was narrowed to phone only writes, because `UserAuthenticationMethod.ReadWrite.All` would let it issue a Temporary Access Pass (a takeover path); the password generator forces character classes and the demo password is validated; `--only`, per request timeouts, serial calls, create conflicts, duplicate nicknames, and the manager skip log are defined; the checkpoint covers AC-1 to AC-16; and the manager pass moved last, as the first cut.

## Landscape check (Oct 18, 2026)

A cheap background check against Microsoft Learn, 5 searches and 8 pages:
- Cloud Sync agent: Windows Server 2016 to 2025, domain joined, not Server Core; the installer creates a gMSA; the schema needs `msDS-ExternalDirectoryObjectId`. Samba support: not listed, no reports found either way.
- Connect Sync: domain joined Windows Server 2016 to 2025 with a GUI, a writable DC, .NET 4.7.2 or later. Samba support: not listed, no reports found.
- Cloud Sync scoping: groups are supported, within a 4 MB scope configuration and 50,000 members per group for delta sync. Nested membership: unconfirmed.
- New Workforce tenants: from the admin center, only paid customers can create one; free and trial users go through a free Azure account. The E5 developer sandbox mostly turns away individuals without a Visual Studio subscription (forum reports, no official statement).
- Phone methods: list is documented with app permissions, with `UserAuthMethod-Phone.Read.All` as the least privilege. App only create and delete, and Free versus P1: unconfirmed.
- User create: `POST /users` requires `accountEnabled`, `displayName`, `mailNickname`, `userPrincipalName`, `passwordProfile`; `onPremisesImmutableId` is needed only for a federated domain.
- Client secrets: up to 24 months in the portal, under 12 recommended; certificates recommended over secrets.

## References

**Project sources**:
- Spec [0001](../0001-stack-architecture/index.md): the thin Graph client with `azidentity`, the `GRAPH_*` settings, and the Windows Server warning for #7
- Spec [0004](../0004-noord-directory/index.md): `SYNC-Entra-Users`, the overlay, the Administrator vs `svc-itsupport` split, fresh rebuilds
- Root `AGENTS.md`: the credential boundary, `internal/seed` and `internal/graph` on both deny lists, settings declared once in `internal/config`
- `docs/scope/scope.md`: the #7 timebox and fallback, the cut order
- `docs/discovery.md`: the hybrid identity situation and the MFA pain point

**Practices & standards**:
- Least privilege per credential (separate seed and runtime identities)
- Reconcile loops (desired state, idempotent convergence) over imperative seeding for state you cannot rebuild
- Accidental delete threshold, as in Connect Sync
- Timeboxed spikes with a go or no go checkpoint

**Links** (web verified in the landscape check):
- Cloud Sync prerequisites: https://learn.microsoft.com/en-us/entra/identity/hybrid/cloud-sync/how-to-prerequisites
- Connect Sync prerequisites: https://learn.microsoft.com/en-us/entra/identity/hybrid/connect/how-to-connect-install-prerequisites
- Create a new tenant: https://learn.microsoft.com/en-us/entra/fundamentals/create-new-tenant
- List phone methods: https://learn.microsoft.com/en-us/graph/api/authentication-list-phonemethods
- Create user: https://learn.microsoft.com/en-us/graph/api/user-post-users
- Add app credentials: https://learn.microsoft.com/en-us/entra/identity-platform/how-to-add-credentials
