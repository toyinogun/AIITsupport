# 0006 rationale: Noord helpdesk, KB, and Slack

The decision record for [index.md](index.md). `/develop` does not need this file.

## Context

Feature #8 finishes Noord's client environment. Spec 0001 already chose the tools: Zammad (a self hosted helpdesk with a built in knowledge base) for tickets and KB, a free Slack workspace with one Socket Mode app, and `slack-go` in the agent. What is still open is how the helpdesk looks "as you find it", how it is built and rebuilt, and how the agent first hears Slack.

The forces:
- **Realism.** The brief's pain points are Monday morning lockouts and a stale KB. A helpdesk that does not sign in against the directory, or a KB with nothing wrong in it, would not show either.
- **Reproducibility.** The repo is public and the eval runs against a fresh seed, so the helpdesk must rebuild from files with one command and land in the same state every time, including the tool server's token.
- **The credential boundary.** Only the tool server may hold client system credentials; Zammad itself must not hold the tool server's write capable AD account.
- **Eval truth.** #16 and the eval need to know which articles are outdated or contradict, without that truth leaking into what the agent reads.
- **Zammad's admin surface.** The landscape check found that the LDAP source, the custom CA, and backdating are not settable through Zammad's documented REST API.
- **Time.** Week 1 of three weeks of evenings; Zammad is the heaviest piece in the stack.

## Options considered

### Option 1: Zammad on Samba LDAP, seeded by REST plus two Rails steps (chosen)

Zammad syncs and authenticates Noord users against Samba over LDAPS. `cmd/seed` fills content through REST and runs two small Rails scripts for the parts REST cannot reach (LDAP, CA, token, backdating).

**Pros**: real AD sign in and real lockouts; the REST client is the one the tool server reuses; one command rebuilds everything.
**Cons**: two Ruby scripts tied to Zammad internals; LDAP adds a service account and a CA to manage; slower to build.

### Option 2: Zammad with users seeded through the API, no LDAP

`cmd/seed` creates every Noord user as a local Zammad customer with a password.

**Pros**: pure REST, deterministic, no Samba dependency.
**Cons**: Zammad sign in and AD lockout are unrelated, so the done line's "get locked out" happens nowhere a user would feel it; less true to a real SMB.

### Option 3: everything in one Rails script

A single Ruby script builds users, tickets, KB, and settings directly in Zammad's models.

**Pros**: fastest to write; one mechanism; backdating is trivial.
**Cons**: the whole seed lives in Ruby; nothing exercises the REST API the tool server depends on, so API surprises arrive in #9 instead.

### Option 4: REST only, manual setup steps

REST for content; the LDAP source and CA are runbook clicks after each reset; no backdating.

**Pros**: no Ruby at all.
**Cons**: breaks one command rebuilds and every eval reset; the KB's ages would all read "today", leaving #16 nothing to prefer the newer article by.

## Rationale

Option 1 is the only one that satisfies realism and reproducibility together. Option 2 fails the brief's lockout story, Option 4 fails the one command rebuild the eval needs, and Option 3 passes both but leaves the REST surface unproven until #9. Keeping the Rails steps to exactly what the landscape check showed REST cannot do keeps the Ruby small, and writing the content through `internal/zammad` means the client the tool server will rely on is tested against a real Zammad in week 1.

The credential boundary decides the LDAP bind account: a new read only `svc-zammad` rather than `svc-itsupport`, so the helpdesk never holds the tool server's write rights. Keeping eval truth in the seed files (as `left:` is in `noord.yaml`) keeps it out of anything the agent can read.

### The smaller calls

| Call | Pick | Why | Runner up |
|---|---|---|---|
| Who drives the seed steps | `cmd/seed helpdesk` drives all of it, with a Rails runner interface | One place owns ordering, the marker fence, and errors; testable with a fake runner; #17 swaps the runner | Make orchestrating three steps by exit code |
| Marker location | the `Noord Logistics` organization's note | Mirrors the marker on `OU=Noord`; one object, written last | a Zammad tag on a sentinel ticket |
| Where the marker check runs | inside `bootstrap.rb`, before any write | REST has no admin before bootstrap, so only Rails can see a fresh Zammad | a separate state script |
| LDAP user id | `sAMAccountName` | survives `make directory-reset`, which gives new object ids | `objectGUID` |
| Assistant rights | custom role `Noord Assistant` (`ticket.agent`, `knowledge_base.reader`) | Agent's default role carries more than the tool server needs | Zammad's stock Agent role |
| Customer authored articles | admin calls with `X-On-Behalf-Of` | authorship looks right in the UI; falls back to fixing `created_by_id` in the backdate step | always author as admin |
| Holding reply | a constant string in `internal/slack` | it is copy, not a limit or a model id; #10 replaces it | a setting |
| Live tests | against the running Compose stack, `TEST_ZAMMAD=1` | a Zammad of ten services in testcontainers is minutes per test run | the testcontainers Compose module |
| Elasticsearch | on locally | matches the cluster and real KB search | Zammad's disable scenario |
| Fiction's today | `as_of` 2026-10-16 in the seed files | dates stay stable whatever day you reset | the reset day |

## Landscape check (Oct 18, 2026)

One capped web check (5 searches, 8 pages) before the Zammad questions. Full notes: `docs/.agent-cache/research/zammad-client-env.md`.

- **Version and Compose:** the official Compose file pins Zammad `7.2.2-0000` and runs backup, Elasticsearch 9.5.5, init, memcached, nginx, Postgres 17.11, railsserver, Redis 8.10.2, scheduler, and websocket. No LTS line found.
- **Elasticsearch:** "optional, but strongly recommended"; disabling it needs `ELASTICSEARCH_ENABLED=false` plus a disable scenario file, since init expects it by default. What search falls back to was not found.
- **LDAP:** LDAPS is supported, with an option to skip verification for self signed certificates; a custom CA can be uploaded under Settings, Security, SSL Certificates (snippet only). LDAP sources are configured in the admin UI; no REST API was found. Zammad tries LDAP first, then its own password store, so LDAP users must have no local password for an AD lock to block sign in. Users no longer returned by LDAP become inactive.
- **KB API:** categories and answers are created by REST (documented on Zammad's "next" docs site, possibly pre release); internal publish is confirmed, public publish and KB search are not documented there.
- **Tokens:** users create their own tokens; creating one for another user is not documented. `X-On-Behalf-Of` accepts an id, login, or email (community only). `auto_wizard.json` exists but has no official key reference.
- **Backdating and Rails:** no API for backdating; `rails r` in the container is plausible (a community thread runs a rails task there) but not confirmed.
- **Mail:** no setting to switch outbound mail off was found; leaving the email channel inactive is the approach.

## Cross check (Oct 18, 2026)

A read only pass on a second model found the design sound and raised 21 decision gaps, all closed in `index.md` on your pick of the recommended fixes. The main ones: the marker hash's exact inputs (now including `noord.yaml` and the CA, so a changed user list or new certs force a reset); what "fresh" means and the removal of Zammad's stock content; ordering against Samba and an old Samba without `svc-zammad`; the LDAP sync trigger and poll; a teardown that never touches `pgdata`; mount paths in an `include`d file; Zammad's own Compose variables kept out of `.env`; the config struct split; the Slack failure paths (dedup insert failing, `invalid_auth`, a DM mention firing twice) and the purge; local time rules for the seed files; the backdated columns, the search reindex, and a pending reminder the scheduler cannot flip; ticket owner, priority, and article types; goldmark for the KB bodies; validation for dead contacts, contradictions, and duplicates; exact counts in AC-7 and AC-8; and checking AC-5's seeded lock after `make directory-lock`, since a seeded lock expires before a reset finishes. It also suggested turning Elasticsearch off locally; you had already chosen to keep it.

## References

**Project sources**:
- Spec 0001 (Zammad, Slack over Socket Mode, `slack-go`, the credential boundary, the config list)
- Spec 0003 (`slack_events` and `MarkEventSeen`)
- Spec 0004 (the seed file, marker, and reset pattern; `svc-itsupport` and its ACEs; the overlay)
- Spec 0005 (the `Graph` and `SeedEntraMFA` settings split this spec copies for Zammad; SMS retired)
- `AGENTS.md` (the credential boundary deny lists; the community Zammad MCP note)
- `internal/config/AGENTS.md` (how settings are declared and split)

**Practices and standards**:
- Least privilege service accounts (read only `svc-zammad`, the custom assistant role)
- Rebuild from source of truth with a marker written last (spec 0004's pattern)
- Acknowledge then deduplicate for at least once event delivery

**Links** (web verified):
- Zammad official Compose file: https://raw.githubusercontent.com/zammad/zammad-docker-compose/master/docker-compose.yml
- Zammad Docker Compose repository: https://github.com/zammad/zammad-docker-compose
- Zammad LDAP integration (admin docs): https://admin-docs.zammad.org/en/latest/system/integrations/ldap/
- Zammad Knowledge Base REST API: https://next.zammad.org/reference/rest-api/knowledgebase.html
- Zammad auto wizard example: https://raw.githubusercontent.com/zammad/zammad/develop/contrib/auto_wizard_example.json
