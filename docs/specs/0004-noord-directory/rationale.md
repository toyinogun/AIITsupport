# 0004. Noord directory: rationale

Decision record for [index.md](index.md). `/develop` builds from the index; this file explains why.

## Context

> ⚠️ Premise note: Samba AD is close to Windows AD, but it is not the same thing, and the video and README should say so. Three differences matter here. Samba runs no SDProp (the job that locks down admin accounts on Windows), so protecting admins depends on OU placement and our own ACLs. "Logging in as a Noord user" is an LDAP bind over LDAPS, not a Windows logon. And lockout is counted on failed LDAP binds, which Samba supports but which you should prove with a test on day one. None of this changes the decision; it only keeps the "real AD" claim honest.

Noord's directory is the client system the agent acts on most often: lookups, unlocks, group owners, access requests. It has to look like an 80 person freight forwarder's AD after years of one busy admin, with stale leavers, messy group names, unowned groups, and a privileged group the agent must never touch, because that mess is what the eval and the video test. Spec 0001 already fixed the engine (Samba AD DC in our own Debian image, `go-ldap` over LDAPS, seed scripts under `seed/`) and left this feature to decide how the directory is built, seeded, secured, and rebuilt.

The forces:
- **Reproducibility.** The repo is public, and a stranger must rebuild Noord from the seed alone. The eval (#20) replays 50 tickets against a fresh seed on every run, so two rebuilds must come out identical, and quickly enough to run in CI.
- **Least privilege at the source.** The brief says the agent's service account can only touch an allow list of OUs and groups. #9 enforces that in code; whether AD itself also enforces it decides how much a tool server bug could hurt.
- **Fictional data only.** No real names, domains, or mailboxes may land in the repo, but a live Slack demo needs a few directory users to match real Slack test accounts.
- **Two architectures and two runtimes.** You develop on arm64 with Compose; the cluster is amd64 k3s, where Samba's usual need for `CAP_SYS_ADMIN` would force the `noord-directory` namespace to the privileged pod security level (an open item in 0001).
- **Downstream contracts.** The OU layout becomes #9's allow list, the owner field feeds #15, the mail attribute feeds #10's identity check, and the sync gap feeds #7. Getting these wrong means reworking four later features.
- **Week 1 timebox.** This shares week 1 with the Entra timebox (#7, ending Oct 21), so the local path has to come first and the k3s shape can wait for #17.

## Options considered

### Option 1: First boot image with a security skeleton, content from one YAML via a Go seeder, fresh container per rebuild (chosen)

The image provisions a domain on first boot and lays down the fixed security skeleton (top OUs, the service account, the delegation ACLs, the lockout policy). A Go `cmd/seed` reads `seed/directory/noord.yaml` and creates every user and group over LDAPS. A rebuild throws the container away (it has no volume, so its filesystem is the whole state).

**Pros**:
- Identical rebuilds by construction; nothing survives from the last run.
- Seeding over one LDAP connection takes seconds, and the eval imports the same package.
- The least privilege wall lives in the image, so content changes can't weaken it.

**Cons**:
- Every rebuild pays for a provision (about 20 to 40 seconds).
- The security skeleton is shell (`samba-tool`), so only the integration test proves it.

### Option 2: Everything in the container with samba-tool

A shell script inside the image runs `samba-tool user add`, `group add`, `group addmembers`, and friends for every object.

**Pros**:
- Only official Samba tooling, nothing custom to maintain.
- Works with no Go code at all.

**Cons**:
- About a second per call, so roughly 2 to 3 minutes per seed, which adds up across eval runs.
- The eval (Go) can't reuse it, and lock state, owners, and nesting need extra raw LDB edits.
- Hard to test except end to end.

### Option 3: Prebuilt seeded snapshot

Provision and seed at image build time, and start each run from a copy of the seeded database.

**Pros**:
- Starts in seconds.
- No seed step at run time.

**Cons**:
- Every copy of the public image shares one domain SID, and the admin password sits in an image layer.
- The snapshot silently drifts from the YAML until someone rebuilds the image.
- Seeded locks expire inside the snapshot, so "locked at start" is unreliable.

## Rationale

Option 1 is the only one that meets reproducibility and least privilege together without pushing cost onto the eval. Fresh containers make "identical rebuild" true by construction rather than by careful cleanup, which is what a public, stranger rebuildable repo and a 50 ticket eval need. The Go seeder turns the 80 users into a readable YAML diff and a few seconds of LDAP calls, and the eval reuses it rather than shelling out. Baking the delegation ACLs into the image is the important call: the service account literally cannot touch admin accounts or privileged groups, so #9's deny list becomes the second wall rather than the only one, and that holds even if someone edits the YAML carelessly.

Option 2 is the boring choice and would work, but its cost lands on every eval run and it gives the eval nothing to import. Option 3 trades away the public image's safety (shared SID, password in a layer) for start time we don't need at 80 users.

The smaller calls, each made in the conversation with you: Debian trixie for a current Samba (runner up: bookworm, older Samba). Provision at first boot (runner up: at build time, rejected for the shared SID). Our own CA mounted into Samba and the tool server, with cert-manager on k3s (runner up: Samba's auto cert, which makes the CA hard to hand to the tool server on k3s). `user.NTACL` so the pod can run at baseline without `CAP_SYS_ADMIN`, with a journaled fallback (runner up: `CAP_SYS_ADMIN` from the start). LDAPS only, with no DNS backend (runner up: adding Kerberos for a more real login). Department OUs plus an admin OU, so #9's allow list is just "Users and Groups". `managedBy` for owners. Match Slack on mail, with a gitignored overlay for demo users. One shared dev password from env. A sync group for the Entra gap. Explicit YAML, decoded strictly with `go.yaml.in/yaml/v3` (already in the module graph; runner up: `goccy/go-yaml` for nicer error positions). testcontainers in CI behind `TEST_SAMBA=1`. On rerun, skip if the marker matches and fail if the directory is partial. Local now, with the k3s shape written down for #17.

Calls made at write time:
- **The security skeleton lives in the entrypoint, the content in the YAML.** The delegation ACEs need Samba's `dsacl` tool, which runs inside the container, and keeping them out of the YAML means no content edit can widen the service account's rights. Runner up: build security descriptors in Go, which needs a descriptor library and is easy to get subtly wrong.
- **Schema GUIDs are constants, not looked up.** The base schema GUIDs for `lockoutTime`, `member`, `user`, and `group` are the same in every AD forest, while reading `schemaIDGUID` means decoding a binary GUID with a byte swap, a classic bug. The permission wall test catches a wrong constant. (This reversed the first draft after the cross check.) Runner up: look them up at provision time.
- **Lockout policy values are container env with defaults (5, 15, 15).** They describe the client environment, not our binaries, so they live in `compose.yaml` and later the chart. The seeder reads the threshold back from the domain instead of keeping a second copy.
- **One `cmd/seed` binary with subcommands** (`certs`, `validate`, `directory`, `lock`, `login`), so #8 can add `zammad` and `kb` later. Runner up: separate small tools per job.
- **Certs are made in Go** (`crypto/x509`) by `seed certs`, so no one needs `openssl` on their machine, and macOS LibreSSL quirks never come up.
- **Rebuild budget: under 2 minutes** for `make directory-reset` on your laptop, and the seeder logs its own duration. That is generous for one provision plus a few seconds of LDAP, and it catches a regression.
- **No named volume for Samba locally.** The project name `noord` scopes every Compose volume, so `docker compose down -v` would also drop `pgdata` (the audit log). With no volume, `docker compose rm -sf samba` is the whole reset and "fresh" holds by construction. (Added after the cross check.)
- **Locked is the computed flag.** `msDS-User-Account-Control-Computed & 0x10`, because `lockoutTime` stays set after a lock expires. Seeded locks still expire after 15 minutes (you kept 5/15/15), so the eval relocks just before a ticket that needs it.
- **Bind by DN, not by UPN**, in `lock`, `login`, and the seeder's Administrator bind, so the unproven case (a UPN suffix that isn't the realm) stays off the critical path.
- **Local port `127.0.0.1:55636`**, the same high port pattern as Postgres (55432), bound to loopback only.
- **`internal/seed` goes on both agent deny lists** (depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`), because it holds the AD admin password.

## Tool landscape

No web landscape check ran (you chose no references). Versions in the index (Debian trixie's Samba 4.2x, the `samba-ad-dc` package split) come from knowledge as of mid 2026, so confirm them when you build the image.
