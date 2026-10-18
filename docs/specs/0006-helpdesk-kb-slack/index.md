# 0006. Noord helpdesk: Zammad on Samba LDAP, a seeded KB, and the Slack app

**Date**: 2026-10-18
**Status**: In Progress

## Summary

This spec builds the rest of Noord's IT world as you would find it: a Zammad helpdesk (the ticketing system, with its own knowledge base) where Noord staff sign in with their AD passwords, an out of date knowledge base full of planted contradictions, a backlog of 20 old tickets, and a free Slack workspace with the bot app registered. Everything rebuilds from files in `seed/helpdesk/` with one command, the same way the directory does. The agent gets its first real Slack strand: it hears a DM or an @mention, drops duplicates, and posts one fixed holding reply, ready for #10 to plug the agent loop in. Zammad runs locally in Compose only; the cluster install is #17.

## Rationale

Context, the options weighed, the smaller calls, and the landscape check: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As a Noord user, I want to sign in to the helpdesk with my normal AD password and raise a ticket, and I get locked out of both when I mistype it too often, just like at a real company.
- As the agent (#9, #10, #16), I want a Zammad user of my own with ticket rights and KB reading rights only, and a token that stays the same across rebuilds, so the tool server works after every reset.
- As the eval (#19, #20) and #16, I want KB articles whose ages and planted mistakes are known truth in the seed files, so "outdated" and "contradicts" can be scored.
- As the engineer, I want the agent to hear Slack and answer once per message, so the Slack strand of the tracer bullet is proven before the agent loop exists.
- As the video audience, I want to see a messy, believable helpdesk: Monday morning lockouts, a stale KB, and the bot answering in Slack.

**Acceptance criteria**:
- **AC-1**: `make helpdesk-up` starts Zammad 7.2.2 from Zammad's official Compose file, vendored and pinned at `seed/helpdesk/zammad/compose.yaml` (its header names the source commit and date) and pulled into `compose.yaml` with `include`, plus our override `seed/helpdesk/zammad/override.yaml`. Zammad's own Compose variables (`VERSION`, its Postgres credentials, the nginx port) live in `seed/helpdesk/zammad/zammad.env`, passed through the `include` entry, never in `.env`. Every Zammad service carries the Compose profile `helpdesk`; the backup service carries the unused profile `disabled`, so it never starts. Elasticsearch runs. Zammad shares the Compose project network with Samba. Only Zammad's nginx publishes a port, on `127.0.0.1:58080`. `make dev` and `make db-up` do not start any Zammad service.
- **AC-2**: The Samba seed gains two things. `entrypoint.sh` creates `svc-zammad` in `OU=Service Accounts,OU=Noord` with password `SEED_ZAMMAD_BIND_PASSWORD`, no expiry, and no ACEs of its own (an ACE is one entry in an object's permission list), so it can read users under `OU=Users,OU=Noord`, and any write as it fails with insufficient access. `noord.yaml` gains the app group `APP-Helpdesk-Agents` (owner and only member `ruben.mulder`). Spec 0004's checks still pass.
- **AC-3**: `make helpdesk-reset` first waits for Samba to be healthy and seeded (the `seed directory` marker check). It then removes only the Zammad containers and the named Zammad volumes (`docker compose --profile helpdesk rm -sfv` on the Zammad services, then `docker volume rm` on the list in the Makefile; never `pgdata`, never Samba), starts Zammad fresh, and seeds it. Afterwards: the setup wizard is done, with `fqdn` `127.0.0.1:58080`, `http_type` `http`, locale `en-us`, and timezone `Europe/Amsterdam`; Zammad's stock content (its welcome ticket, demo customer Nicole Braun, demo organization, and `Users` group, deleted in that order) is gone; a local admin `SEED_ZAMMAD_ADMIN_EMAIL` exists with password `SEED_ZAMMAD_ADMIN_PASSWORD`, used only by `cmd/seed`; both of Zammad's `Email::Notification` channels (smtp and sendmail) are inactive and no `Email::Account` channel exists, so no mail can leave; our dev CA is trusted; one LDAP source as in the data model below; the group `IT Support` exists; self signup and password reset are off (AC-17).
- **AC-4**: After the LDAP sync, every `users:` entry in `noord.yaml` (leavers included) is an active Zammad user whose login is its username, in the organization `Noord Logistics`, with no local Zammad password. `ruben.mulder` has Agent and Admin; every other synced user has Customer only. No `admins:` entry and no service account exists in Zammad. The sync searches `OU=Noord` and keeps only people with a `department` (Zammad has one search base for users and groups); seed validation already requires a department on every `users:` entry, and admin and service accounts never carry one.
- **AC-5**: A Noord user signs in to Zammad at `http://127.0.0.1:58080` with `SEED_USER_PASSWORD` and raises a ticket in `IT Support` by hand. Five wrong passwords at the Zammad login lock that user's AD account (`make directory-login NAME=<user>` shows locked), and the right password is then refused by Zammad too until the lock ends or is cleared. After `make directory-lock NAME=yara.schouten`, she cannot sign in to Zammad. Manual check in `verify.md`, plus a live test of the bind behaviour.
- **AC-6**: A local user `noord-assistant` ("Noord IT Assistant") holds only the role `Noord Assistant` (permissions `ticket.agent` and `knowledge_base.reader`, full access to `IT Support` and no other group) and a token whose value is exactly `ZAMMAD_TOKEN`. With that token: `GET /api/v1/users/me` returns the assistant; it can create and update a ticket in `IT Support` for a Noord customer and read every KB answer; it gets 403 on `GET /api/v1/settings` and on any KB write. Zammad lets every `ticket.agent` create and update users, so that guard is ours: `internal/zammad` has no call that creates, updates, or deletes a Zammad user, and a unit test against the fake Zammad fails if any of its calls sends `POST`, `PUT`, `PATCH`, or `DELETE` to a `/api/v1/users` path. The same token works after every `make helpdesk-reset`.
- **AC-7**: The KB holds one knowledge base in English with exactly 6 categories and 15 answers, all published so Noord users see them in the Zammad portal. Each answer's created and updated dates in Zammad, read as `Europe/Amsterdam` local dates, equal its file's `created` and `updated`. The planted set is at least the minimum set in the data model below (2 contradicting pairs, 2 retired systems, 2 dead contacts, 1 near duplicate pair). The eval truth fields never reach Zammad. KB search after the seed finds answers by their backdated content (the search index was rebuilt).
- **AC-8**: 20 tickets from `tickets.yaml` exist in `IT Support`, each tagged with its `key`, with its customer set, with each article authored by the user its `from` names and the ticket fields from the data model below, and every date from the file. At least 14 are lockouts or password resets, and at least 8 were created on a Monday between 07:00 and 10:00 Amsterdam time; 2 are open, 1 is pending, 17 are closed.
- **AC-9**: The marker `seed:sha256:<hash>` (hash as defined under Key invariants) is written to the `Noord Logistics` organization's note as the very last step. `make helpdesk-seed` on a Zammad whose marker matches prints "already seeded" and exits 0 with no writes; on one with seeded content and no matching marker (changed files, new certs, or a run that stopped halfway) it exits 1 telling you to run `make helpdesk-reset`. Two resets in a row give the same content (counts, titles, dates; the pending ticket's reminder is set from the run time and left out of the comparison). On "already seeded" the seed also checks `GET /api/v1/users/me` with `ZAMMAD_TOKEN`; if it is not the assistant (the token changed since the seed), it exits 1 telling you to run `make helpdesk-reset`. `make noord-reset` runs `directory-reset`, `entra-sync`, then `helpdesk-reset`; it is not atomic, so a failed `helpdesk-reset` leaves a fresh directory beside the old Zammad until you rerun it. After `make directory-reset` alone, Zammad sign in still works, because users match by login, not by AD object id. A Samba container older than this feature (no `svc-zammad`) makes `seed helpdesk` exit 1 telling you to run `make directory-reset`.
- **AC-10**: `go run ./cmd/seed validate` also checks the helpdesk files with no network and reports every problem together: slugs unique; `kb.yaml` and `tickets.yaml` carry the same `as_of`, and no `left` date in `noord.yaml` is after it; every `contradicts` target exists and is `current` while the article holding it is `outdated`; every `duplicate_of` target exists and is older than the article holding it; every `dead_contact` is a user with a `left` date later than the article's `updated`; `updated` not before `created`; every ticket `customer` and article `from` is a `users:` username; ticket times in order and not after `as_of`; no local time that does not exist in `Europe/Amsterdam` (the spring DST gap); `closed` present exactly when the state is `closed`; states from the allowed list; categories from `kb.yaml`, each with an icon and at least one answer.
- **AC-11**: A free Slack workspace "Noord Logistics" holds the app created from `deploy/slack/manifest.yaml`: Socket Mode on, interactivity on, App Home messages tab on, bot events `app_mention` and `message.im`, bot scopes `app_mentions:read`, `chat:write`, `im:history`, `users:read`, `users:read.email`, and an app level token with `connections:write`. Channels `#it-help` (public) and `#it-escalations` (private) exist with the bot in both. Besides you, at most 2 demo accounts, each with an email that `seed/directory/overlay.local.yaml` gives a Noord user. `docs/runbooks/slack-workspace.md` holds the steps with no token, workspace id, or real email in it; the tokens live only in `.env`. Manual check, recorded in `verify.md`.
- **AC-12**: With both Slack tokens set, the agent connects over Socket Mode at start. A DM to the bot (a `message.im` event), or an @mention in `#it-help` (an `app_mention` event), is acknowledged at once, marked seen with `MarkEventSeen`, logged as `slack_event_received` (event id, channel id, event type; never the text or a name), and answered with the fixed holding reply in the message's thread within 5 seconds (manual timing, recorded in `verify.md`). A redelivered event id gets no second reply. An `app_mention` in a DM is ignored, so a DM that mentions the bot gets one reply. Messages from any bot (the bot itself included) and message subtypes (edits, deletes, joins) are ignored. If `MarkEventSeen` errors (database down), the agent logs `slack_event_dedup_failed` and sends no reply (at most once, spec 0003). `PurgeSeenEventsBefore` runs at start and then hourly, removing ids older than `SLACK_EVENT_DEDUP_TTL`.
- **AC-13**: With both Slack tokens unset, the agent starts, logs `slack_disabled`, and `GET /healthz` returns 200. With exactly one set, `LoadAgent` refuses and names the missing setting. A bot token not starting `xoxb-` or an app token not starting `xapp-` fails at load, naming the setting, never the value. With both set, a failed first connect retries with backoff and logs each attempt; Slack answering `invalid_auth` ends `run()`, and the agent exits 1 naming the setting.
- **AC-14**: Failures are explicit. Zammad not healthy within `SEED_ZAMMAD_WAIT_TIMEOUT` fails naming the URL and the timeout. `bootstrap.rb` runs the LDAP import inline and fails naming the step when the import records an error or any `failed` user. Each Rails step is stopped after `SEED_ZAMMAD_WAIT_TIMEOUT`, inside the container (so Ruby stops too), and the message says to run `make helpdesk-reset`, since a stopped bootstrap reads as `needs_reset`. After bootstrap, the seed compares the login to id map bootstrap returns with the `users:` list: any username missing, inactive, outside `Noord Logistics`, or (for `ruben.mulder`) without Agent and Admin fails the seed, listing them all, before any ticket or KB write. Samba unreachable from Zammad fails naming `ldaps://samba:636`. A REST error names the method, path, and status, never the token. A Rails step error shows its output with every secret value removed. A dropped Slack socket reconnects with backoff, and events redelivered after it are deduplicated.
- **AC-15**: Secrets reach the Zammad containers only as environment variables (never in an argument list, a file, or a log line). Everything in `seed/helpdesk/` is fiction: a hygiene test fails if a KB article or ticket names a person who is not in `noord.yaml`, or holds an email address outside `noord.example`.
- **AC-16**: CI runs unit tests with no network: helpdesk file validation, `internal/zammad` against an `httptest` fake Zammad (including the no user writes guard), the seed's ordering and marker fence with a fake Rails runner, and the Slack handler with a fake socket and poster. Live tests run only with `TEST_ZAMMAD=1` on your laptop (one of them runs a second LDAP import after the seed and checks nothing changed), against the running Compose stack after `make helpdesk-reset`, never in CI.
- **AC-17**: Zammad offers no way around AD sign in. After bootstrap the settings `user_create_account` (self signup) and `user_lost_password` (password reset) are both false (read from `report.rb`'s snapshot). An unauthenticated `POST /api/v1/users` with `signup: true` and an unauthenticated `POST /api/v1/users/password_reset` for `eva.peters` are both refused: a status that is not 2xx (or a failure body), no new user, and no reset token. The build probes the real status once and records it in `verify.md`. The live test sends both with a plain `net/http` client, since `internal/zammad`'s wall refuses any non GET under `/api/v1/users`, and signs up with an unused `@noord.example` address. The login page shows neither "Register as a new customer" nor "Forgot password" (manual check, in `verify.md`).

## Decision

**Chosen option**: Option 1: Zammad signing in against Samba over LDAP, seeded through REST with two small Rails steps, plus a manifest built Slack app and a holding reply listener.

`cmd/seed helpdesk` drives the whole seed: it runs a Rails bootstrap inside Zammad for what REST cannot set (including the LDAP import, the organization and its members, and the empty KB), fills tickets, KB categories, and answers through `internal/zammad` (the thin REST client the tool server reuses in #9, which never writes a Zammad user), then runs a Rails backdate step that writes the marker last. (basis: spec 0001 for Zammad and Slack, spec 0004's marker and reset pattern, the Oct 18 landscape check for what REST cannot set)

**Implementation skills**: `golang-security` (`samber/cc-skills-golang`, `.claude/skills/golang-security/`) · `golang-error-handling` (`samber/cc-skills-golang`, `.claude/skills/golang-error-handling/`) · `golang-concurrency` (`samber/cc-skills-golang`, `.claude/skills/golang-concurrency/`, the Socket Mode loop) · `golang-database` (`samber/cc-skills-golang`, `.claude/skills/golang-database/`, the dedup query)

## Feature design

**Data model sketch** (no new tables in our Postgres; the agent reuses `slack_events` from spec 0003):

Seed files, the source of truth:

| File | Holds | Rules |
|---|---|---|
| `seed/directory/noord.yaml` (changed) | adds `APP-Helpdesk-Agents`, kind `app`, owner `ruben.mulder`, members `[ruben.mulder]` | existing validation |
| `seed/directory/samba/entrypoint.sh` (changed) | creates `svc-zammad` from `SAMBA_ZAMMAD_PASSWORD` (Compose passes `SEED_ZAMMAD_BIND_PASSWORD`) | no write ACEs; fails if the password is empty |
| `seed/helpdesk/kb.yaml` | the KB name, `as_of` (the fiction's "today", 2026-10-16), and the categories in order, each `{name, icon}` (`icon` is a Font Awesome code such as `f023`, Zammad's `category_icon`) | category names unique; icon required |
| `seed/helpdesk/kb/<slug>.md` | YAML header: `title` (req), `category` (req), `created` (req date), `updated` (req date); eval truth: `status` (`current` or `outdated`, req), `contradicts` ([slug], opt, held by the outdated article only), `retired_system` (string, opt), `dead_contact` (username, opt), `duplicate_of` (slug, opt, held by the newer article). Body: Markdown, converted to HTML with `github.com/yuin/goldmark` with raw HTML off | slug = file name, unique; truth fields never sent to Zammad |
| `seed/helpdesk/tickets.yaml` | `as_of` (same as `kb.yaml`), then 20 tickets: `key` (req, unique), `customer` (username, req), `title` (req), `state` (`closed`, `open`, `pending`), `created` (req), `closed` (required exactly when closed), `articles` [{`from` (username), `at`, `body`}] at least 1 | first article is from the customer; times in order; nothing after `as_of` |
| `seed/helpdesk/rails/bootstrap.rb`, `backdate.rb` | the two Rails steps | inside the marker hash |
| `seed/helpdesk/rails/report.rb`, `ldapsync.rb` | helpers for the `TEST_ZAMMAD=1` live tests only: a read only content snapshot (including the settings `user_create_account` and `user_lost_password`), and one more inline LDAP import (added Oct 18, from the build) | inside the marker hash; never run by `seed helpdesk` |

Times in the seed files: a datetime is written `2026-09-14 08:12` and means `Europe/Amsterdam` local time (no offset in the file); a bare date means 09:00 local that day. Go converts each to RFC 3339 UTC before it reaches Ruby.

KB categories: Accounts and passwords · MFA and sign in · VPN and remote work · Email and Microsoft 365 · Printers and hardware · Starters and leavers.

Minimum planted set (slugs are the build's to name, the facts are fixed):

| Kind | Planted facts |
|---|---|
| Contradicting pair 1 | An old password policy (8 characters, change every 90 days, `outdated`) against a newer one (14 characters, no forced change, `current`) |
| Contradicting pair 2 | One article says a lockout clears itself after 30 minutes (`outdated`); another says 15 minutes, which matches Samba's real `SAMBA_LOCKOUT_DURATION` (`current`) |
| Retired system 1 | Setting up the old VPN client, replaced by a newer VPN article |
| Retired system 2 | Registering SMS as an MFA method (SMS retired, spec 0005 update) |
| Dead contact 1 | The new starter checklist says to ask `mark.dekker` (left 2026-03-31) |
| Dead contact 2 | A shared drive article names `thomas.meijer` (left 2026-06-30) as the person to ask |
| Near duplicate | Two password reset articles with different titles and slightly different steps |

Zammad objects the seed builds:

| Object | Key | Relationships and rules |
|---|---|---|
| Organization `Noord Logistics` | name; created by bootstrap after the LDAP import, which then sets it on every user whose source is `Ldap::<id>` (leavers included) | 1:N users; `shared` false (a customer sees only their own tickets), `domain_assignment` false; `note` holds the marker, written last by backdate. A later LDAP sync must leave the organization alone (live tested, AC-16) |
| Group `IT Support` | name = `ZAMMAD_DEFAULT_GROUP`; created by bootstrap | 1:N tickets; the Agent role and the `Noord Assistant` role have full access |
| LDAP source | one; created by bootstrap | host `samba`, port 636, SSL, certificate verification on; bind DN `CN=svc-zammad,OU=Service Accounts,OU=Noord,DC=ad,DC=noord,DC=example`; one search base for users and groups, `OU=Noord,DC=ad,DC=noord,DC=example`; user filter `(&(objectCategory=person)(objectClass=user)(department=*))`; attributes `sAMAccountName` → login (also the user id, `user_uid`), `givenName` → firstname, `sn` → lastname, `mail` → email, `department` → department; group filter `(objectClass=group)`, group id `dn`; role map `APP-Helpdesk-Agents` (its DN in lower case, as Zammad matches it) → Agent and Admin, unmapped users → Zammad's signup roles (Customer); Zammad's default sync interval |
| LDAP user | login = `sAMAccountName` | N:1 organization; no local password; roles from the mapping |
| Local admin | `SEED_ZAMMAD_ADMIN_EMAIL` | login and email = the setting, name `Seed Admin`; roles Admin and Agent (for KB editing and `From`); only `cmd/seed` uses it |
| Local `noord-assistant` | login | role `Noord Assistant` only; token = `ZAMMAD_TOKEN`, with the token's own permission list set to `ticket.agent` and `knowledge_base.reader`; Zammad makes its own token value on create, so bootstrap then overwrites it with `ZAMMAD_TOKEN`. Name `Noord IT Assistant`, email `noord-assistant@noord.example`, no password; token name `noord-assistant`, persistent, no expiry |
| Ticket | Zammad id; tag = `key` | N:1 customer, N:1 group, 1:N articles; priority `2 normal`; owner `ruben.mulder` when closed or pending, none when open; Zammad state `closed`, `open`, or `pending reminder` with its reminder 30 days after the seed run (so the scheduler never flips it); customer articles are public `web` articles, Ruben's replies `note` articles with `internal: false`; closed by `ruben.mulder`. Order per ticket: create it with `From: <customer>` and the customer's numeric id (Zammad lets a customer create a ticket only in state `new`; probed, the `tags` param works there), post every further article with `From: <author>` while it is still `new`, then set state, owner, and any `pending_time` with `From: ruben.mulder` (a customer article on a closed ticket would reopen it) |
| Knowledge base | one; created by bootstrap (REST cannot set its locale) | locale `en-us`, primary; Zammad makes the translation for that locale itself, and bootstrap titles it `Noord IT Help`; active, Zammad's default colours and `grid` layouts, `FontAwesome` icons; the public portal at `/help/en-us` needs no sign in (`kb_active` and `kb_active_publicly` on) |
| KB answer | Zammad id | N:1 category; published; dates backdated |

Backdated columns, with their sources: tickets `created_at` (`created`), `updated_at` (the last article's `at`, or `closed`), `close_at` and `last_close_at` (`closed`, empty when not closed), `first_response_at` (Ruben's first article, empty if none), `last_contact_at` (the last article), `last_contact_customer_at` and `last_contact_agent_at` (the last article from that side, empty if none); Zammad's escalation and time to close columns are cleared; articles `created_at` and `updated_at` (`at`); KB answers and their translations `created_at` (`created`) and `updated_at` (`updated`), translations' `edited_at` (`updated`), answers' `published_at` (`created`). The translations' content rows hold only the body and have no date columns (journal 0026). History rows keep the seed time (accepted). `backdate.rb` then rebuilds the search index for tickets and KB answers before it writes the marker.

**State transitions**: seeded tickets are written straight into their final state (`closed`, `open`, `pending`); nothing transitions during the seed. The helpdesk as a whole: `fresh` → (bootstrap) `bootstrapped` → (REST fill) `filled` → (backdate, marker) `seeded`. `bootstrap.rb` decides first, before any write: `already_seeded` when the `Noord Logistics` note equals the expected marker; `fresh` when the setup wizard is not done (`system_init_done` false) and neither the seed admin nor the `Noord Logistics` organization exists; `needs_reset` for anything else.

**API surface** (commands, the Rails steps, the Zammad calls `internal/zammad` makes, and the Slack listener):

| Surface | Kind | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `make helpdesk-up` | make | `.env` | Zammad running (`docker compose --profile helpdesk up -d --wait`) | none | Docker out of memory |
| `make helpdesk-seed` → `go run ./cmd/seed helpdesk` | command | `Seed` settings, `seed/helpdesk/` | "seeded" with counts, or "already seeded" | admin password | exit 1 "run make helpdesk-reset"; timeout |
| `make helpdesk-reset` | make | as above | fresh seeded Zammad | as above | as above |
| `make noord-reset` (changed) | make | `.env` | directory, Entra, helpdesk rebuilt | as above | first failing step stops it |
| `go run ./cmd/seed validate` (extended) | command | seed files | all problems at once | none | exit 1 listing problems |
| `bootstrap.rb` (via the Rails runner) | Rails step | env: expected marker, admin email and password, bind password, `ZAMMAD_TOKEN`, `ZAMMAD_DEFAULT_GROUP`; CA at `/opt/noord-seed/ca.pem` | one line `NOORD_RESULT {json}` on stdout (Rails logs go there too): `fresh`, `already_seeded`, or `needs_reset`, then, on `fresh`, the ids REST needs (group, KB, `kb_locale_id`, seed admin, assistant, roles) and a login to id map of every LDAP user with its active flag, organization, and roles; on `fresh` it also runs Zammad's LDAP import inline (`ImportJob.create!(name: 'Import::Ldap').start`; while that job is unfinished the scheduler queues no second one), then creates `Noord Logistics` and sets it on every synced user | runs inside the container | `NOORD_FAILED <step>: <message>` on stderr, exit 1 |
| `backdate.rb` (via the Rails runner) | Rails step | JSON on stdin: ticket and article ids with dates, answer ids with dates, the marker | marker written last | inside the container | exits non zero naming the id |
| `report.rb`, `ldapsync.rb` (via the Rails runner, live tests only) | Rails step | none | `NOORD_RESULT {json}`: a snapshot of marker, organization members, Ruben's roles, the two AC-17 settings, tickets, articles, answers, categories; or one LDAP import's counts | inside the container | `NOORD_FAILED` on stderr, exit 1 |
| `GET /api/v1/users/search` | REST | `query` (login), paging | user ids and roles | admin basic auth or the assistant token | 401 (read only; for #9's lookups, the seed uses bootstrap's map instead) |
| `POST /api/v1/tickets` | REST | `title`, `group`, `customer_id` (numeric, from bootstrap's map), `article`, `tags`; header `From: <customer>` | ticket id, first article id | admin | 422 |
| `PUT /api/v1/tickets/{id}` | REST | `state`, `owner_id`, `pending_time`; header `From: ruben.mulder` | ticket | admin | 422 |
| `POST /api/v1/ticket_articles` | REST | `ticket_id`, `body`, `sender`, `type` `note` or `web`; header `From: <author>` | article id | admin | 422 |
| `POST /api/v1/knowledge_bases/{kb}/categories`, `/answers` | REST | `category_icon`, title, parent, body (in `translations_attributes`, `kb_locale_id`) | ids | admin (`knowledge_base.editor`) | 422 |
| `POST /api/v1/knowledge_bases/{kb}/answers/{id}/publish` | REST | none | answer with `published_at` | admin | 422 |
| Socket Mode `events_api` | Slack in | `event_id`, event `type`, `channel`, `ts`, `thread_ts`, `bot_id`, `subtype` | ack | app token | socket drop: reconnect |
| `chat.postMessage` | Slack out | `channel`, `thread_ts`, holding text | message ts | bot token | `not_in_channel`, rate limited: log and drop |

The Rails runner is a small Go interface in `internal/seed/helpdesk`. Its local implementation runs `docker compose exec -T -e NAME... zammad-railsserver timeout -k 5 <seconds> bundle exec rails r /opt/noord-seed/rails/<step>.rb` with a fixed argument list (`<seconds>` from `SEED_ZAMMAD_WAIT_TIMEOUT`, so a stopped step stops inside the container too); secret values travel only in the environment. #17 swaps in a cluster implementation. The override mounts `seed/helpdesk/rails/` at `/opt/noord-seed/rails` and `.dev/certs/ca.pem` at `/opt/noord-seed/ca.pem`, read only, into `zammad-railsserver` and `zammad-scheduler` (both run LDAP). Because the override is pulled in through `include`, its relative paths resolve from `seed/helpdesk/zammad/`, so it writes them as `../rails` and `../../../.dev/certs/ca.pem`.

`cmd/seed helpdesk` runs, in order: wait for Zammad healthy; check `svc-zammad` can bind over `SEED_LDAP_URL` (else "run make directory-reset"); `bootstrap.rb` (stop on `already_seeded` or `needs_reset`; on `fresh` it ends with the inline LDAP import and the organization); check every `users:` login exists; REST fill (tickets and articles, KB categories and answers, publish); `backdate.rb` (dates, reindex, marker last).

**Value sourcing**:

| Action | Value | Source |
|---|---|---|
| Bootstrap | admin email and password | `SEED_ZAMMAD_ADMIN_EMAIL`, `SEED_ZAMMAD_ADMIN_PASSWORD` |
| Bootstrap | LDAP bind DN and password | constant DN (data model; the base DN `DC=ad,DC=noord,DC=example` is fixed by the realm in `compose.yaml`, spec 0004); password `SEED_ZAMMAD_BIND_PASSWORD` |
| Bootstrap | LDAP host and CA | `ldaps://samba:636` (the Compose service name, fixed in the override); CA is `.dev/certs/ca.pem` mounted at `/opt/noord-seed/ca.pem` |
| Bootstrap | system settings | constants: `fqdn` `127.0.0.1:58080`, `http_type` `http`, locale `en-us`, timezone `Europe/Amsterdam`, `user_create_account` false, `user_lost_password` false |
| Bootstrap | assistant token value | `ZAMMAD_TOKEN` (the `Zammad` settings struct) |
| Bootstrap | role map group | `CN=APP-Helpdesk-Agents,OU=Groups,OU=Noord,DC=ad,DC=noord,DC=example`, lower cased by bootstrap (Zammad matches the lower case DN) |
| Bootstrap | LDAP search base and user filter | constants in `bootstrap.rb`: `OU=Noord,DC=ad,DC=noord,DC=example` and `(&(objectCategory=person)(objectClass=user)(department=*))` |
| Bootstrap | organization members | every user the LDAP import created (source `Ldap::<id>`) |
| Bootstrap | KB title and locale | constants: `Noord IT Help`, `en-us` |
| Bootstrap and backdate | expected marker | derived, see Key invariants |
| Seed | `svc-zammad` bind check | `SEED_LDAP_URL`, `SEED_CA_CERT`, `SEED_ZAMMAD_BIND_PASSWORD` |
| Seed | Rails step limit | `SEED_ZAMMAD_WAIT_TIMEOUT` |
| Seed | missing login check and customer ids | `users:` usernames in `SEED_FILE` against bootstrap's login to id map (not user search: its index can lag the import) |
| Seed | pending reminder | run time plus 30 days, sent as `pending_time` in the state update |
| Seed | token drift check | `GET /api/v1/users/me` with `ZAMMAD_TOKEN`, on `already_seeded` |
| Bootstrap | category icons | `icon` per category in `kb.yaml` (used by the REST fill) |
| Seed | `From` header value | the ticket's `customer`, the article's `from`, or `ruben.mulder` for state and owner |
| Seed | ticket owner, priority, article type, Zammad state, pending reminder | rules in the data model (Ticket row) |
| Seed | KB answer HTML | the Markdown body through goldmark, raw HTML off |
| Seed | Zammad URL | `ZAMMAD_URL` |
| Seed | group name | `ZAMMAD_DEFAULT_GROUP` |
| Seed | ticket and article content, KB content | `seed/helpdesk/` files |
| Backdate | dates | the files' `created`, `updated`, `closed`, article `at`, as `Europe/Amsterdam` local times, sent as RFC 3339 UTC in the stdin JSON |
| Backdate | Zammad ids | returned by the REST calls in the same run |
| Slack listener | channel and thread for the reply | the event's `channel`, and `thread_ts` or else `ts` |
| Slack listener | holding reply text | a constant in `internal/slack`: "Hi, I'm Noord's IT assistant. I can't handle requests yet, so please raise a ticket in the helpdesk or ask Ruben." |
| Slack listener | whether to answer | `app_mention` with channel type not `im`, or `message` with channel type `im`; no `bot_id`, no `subtype` |
| Slack listener | purge cutoff | now minus `SLACK_EVENT_DEDUP_TTL` |
| Validation | the fiction's today | `as_of` in `kb.yaml` and `tickets.yaml` |

**Key invariants**:
- Truth fields (`status`, `contradicts`, `retired_system`, `dead_contact`, `duplicate_of`) exist only in the seed files.
- The marker is written last and only after every write succeeded; content without a matching marker always means reset, never a top up.
- The marker hash is sha256 over, for each input file in bytewise sorted order of its slash separated path relative to the repo root: the path, a NUL byte, the raw file bytes, a NUL byte. Inputs: every regular file under `seed/helpdesk/` whose name does not start with `.` (the Rails scripts and the vendored Compose files included), plus `seed/directory/noord.yaml` and `.dev/certs/ca.pem`. No line ending normalisation. So a changed user list or regenerated certs also means reset.
- LDAP users never hold a local Zammad password, and Zammad offers no self signup and no password reset, so a locked AD account always blocks Zammad sign in and no one gets a Zammad login AD does not control (beyond the seed admin and the assistant).
- The assistant holds no admin permission and no KB editor permission; its token value comes only from `ZAMMAD_TOKEN`.
- `internal/zammad` never writes a Zammad user. The only user writes are in `bootstrap.rb` (the seed admin, the assistant, the organization membership) and Zammad's own LDAP sync. Two walls hold it: the client's HTTP transport refuses any request that is not `GET` to a path under `/api/v1/users`, `/api/v1/organizations`, or `/api/v1/user_access_token` (path normalised first: case, trailing slash, query), and a unit test calls every exported client method against the fake Zammad and fails on any such request. Tickets name their customer by numeric id, never by an email or login string, so Zammad never creates a user on the fly.
- `svc-zammad` has no write ACE anywhere; `svc-itsupport` never appears in Zammad.
- Each Slack event id gets at most one reply.
- No outbound email ever leaves Zammad.

**Security model**:
- Credentials split by holder: `cmd/seed` holds the Zammad admin password, the `svc-zammad` password, and (to install it) `ZAMMAD_TOKEN`; the tool server holds only `ZAMMAD_*`; the agent holds only the Slack tokens. `internal/zammad` is already on both credential boundary deny lists (`.golangci.yml` and `cmd/agent/boundary_test.go`), so the agent can never import it; the build confirms the entries still match. `internal/seed/helpdesk` is under `internal/seed`, already denied.
- Zammad holds the `svc-zammad` password in its own database, as a real helpdesk would; that account can only read.
- Zammad's built in policy lets any `ticket.agent` search, create, and update users, so the assistant token can do that at the Zammad level and no role setting removes it. The wall is ours: `internal/zammad` has no user write call (AC-6's guard), and #9 must expose no tool that writes a user. Probed Oct 18: the assistant token gets 403 editing an Admin such as Ruben (`verify.md`), so a leaked `ZAMMAD_TOKEN` could create or edit Zammad customer accounts (not AD users, not admins); that is the residual risk.
- Zammad's UI and API listen only on loopback. Admin UI access on the cluster is #17 (Tailscale ingress, spec 0001).
- Demo users' real email addresses (from the gitignored overlay) reach only the local Zammad and the Slack workspace, never a tracked file, and Zammad never mails them.
- GDPR scope: fictional people only. The data processing note (#22) already lists Slack as a processor outside the cluster.
- Self signup and password reset are off (AC-17). A self registered local account would sign in without AD, and a reset would give an LDAP user a local password that keeps working through an AD lockout; both would break the invariant above. Mail being off already stops a reset from finishing, but the settings make it explicit and survive mail being turned on later. A signed in LDAP user cannot set a local password from their profile either: Zammad checks the current password against the local hash, which is empty for LDAP users, so the change always fails (read in Zammad 7.2.2's code, Oct 18). The other ways to get a Zammad user, the web form and email channels, are off.
- Failed Zammad sign ins count toward AD lockout on purpose (AC-5). It is the real behaviour a hybrid shop lives with, and the runbook says so.

**Configuration required**:
- `ZAMMAD_URL`, `ZAMMAD_TOKEN`, `ZAMMAD_DEFAULT_GROUP` (exist): move into a `Zammad` struct embedded in `ToolServer` and in the new `SeedHelpdesk`, like `Graph`. Dev values: `http://127.0.0.1:58080`, a dev token, `IT Support`. `ZAMMAD_TOKEN`, when set, must be at least 32 characters from `A-Za-z0-9_-` (checked at load, naming the setting); if gitleaks flags the dev value in `.env.example`, allowlist that one line in `.gitleaks.toml` with a comment.
- `ZAMMAD_TIMEOUT` (new, `Zammad` struct): per request timeout, default `15s`.
- `SeedHelpdesk` (new struct) embeds `Seed` (for `SEED_LDAP_URL`, `SEED_CA_CERT`, `SEED_FILE`, the bind password) and `Zammad`; it gets `LoadSeedHelpdesk` and an entry in `Names()`.
- `SEED_ZAMMAD_ADMIN_EMAIL` (new, `SeedHelpdesk`): default `zammad-admin@noord.example`.
- `SEED_ZAMMAD_ADMIN_PASSWORD` (new, `SeedHelpdesk`, required): the local Zammad admin's password.
- `SEED_ZAMMAD_BIND_PASSWORD` (new, `Seed`, optional in the struct so existing `LoadSeed` callers keep working; `seed directory` and `seed helpdesk` each fail naming it when empty): `svc-zammad`'s AD password; Compose passes it to Samba as `SAMBA_ZAMMAD_PASSWORD`.
- `SEED_HELPDESK_DIR` (new, `SeedFiles`): default `seed/helpdesk`.
- `SEED_ZAMMAD_WAIT_TIMEOUT` (new, `SeedHelpdesk`): default `10m`.
- `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN` (exist, `Agent`): optional as a pair, with prefix checks, in a new check that `LoadAgent` runs (AC-13). `SLACK_IT_CHANNEL` stays for #11.
- `.env.example` gains every new name; the drift test covers it. Zammad's own Compose variables stay in `seed/helpdesk/zammad/zammad.env`, outside the drift test. `internal/config/AGENTS.md`'s "Zammad goes in `ToolServer` only" line changes to match (for `/sync`). Prerequisite: a Slack workspace and app created by hand per the runbook before AC-11 and AC-12 can pass.
- New dependencies: `github.com/slack-go/slack` (spec 0001) and `github.com/yuin/goldmark`; `make tidy-check` and `make vuln` pass with them.

**Critical test scenarios**:
- Happy path: `make noord-reset`, sign in as `eva.peters`, raise a ticket, see the KB and history; DM the bot and get one holding reply, verifies **AC-1** to **AC-8**, **AC-12**
- Failure case: stop the seed after the REST fill, then `make helpdesk-seed` exits 1 asking for a reset; a redelivered Slack event gets no second reply, verifies **AC-9**, **AC-12**
- Auth and permission: the assistant token gets 403 on settings and on editing the KB, and the guard test fails if `internal/zammad` ever writes a user; five wrong Zammad passwords lock the AD account and then the right one is refused; a signup or password reset request is refused, verifies **AC-5**, **AC-6**, **AC-17**

## Build plan

Tracer Bullet: one thin thread through Compose, Samba, Zammad, and the assistant token first, then the Slack strand, then the content thickens.

1. Settings: the `Zammad` struct, `SeedHelpdesk` with `LoadSeedHelpdesk`, the new `SEED_*` names, the token format check, the Slack pair and prefix check in `LoadAgent`, `.env.example`; confirm the `internal/zammad` deny list entries, satisfies **AC-13**, **AC-15**
2. Thin helpdesk thread: vendor Zammad's Compose file, `zammad.env`, and the override (profiles, mounts, loopback port); `svc-zammad` and `APP-Helpdesk-Agents` in the Samba seed; `bootstrap.rb` (state check, wizard and settings, stock content removed, admin, mail off, CA, `IT Support`, the assistant role, user, and token, the empty KB, LDAP source and role map, the inline LDAP import, the organization and its members); `internal/zammad` with user search and ticket calls and the no user writes guard test; `cmd/seed helpdesk` with the Rails runner and the missing login check; `make helpdesk-up` and `make helpdesk-reset`. One user signs in, raises a ticket, and gets locked out through Zammad; the assistant token works. Probe gate: done on 2026-10-18, results in `verify.md` (the two failures led to this spec update; the search index rebuild is proven in step 4; the Admin edit probe ran during the build and got 403). The build items in this step are built, satisfies **AC-1** to **AC-6**, **AC-14**
3. Slack strand: `deploy/slack/manifest.yaml`, `docs/runbooks/slack-workspace.md`, the workspace and channels; `internal/slack` (Socket Mode loop, filter, `MarkEventSeen` with the failure path, holding reply, the start and hourly purge, `invalid_auth` ending `run()`) started by `cmd/agent` when the tokens are set, satisfies **AC-11**, **AC-12**, **AC-13**, **AC-14**
4. Content: `kb.yaml`, the 15 KB articles with the planted set, the 20 tickets in `tickets.yaml`, and their validation in `seed validate`; goldmark conversion; KB and article calls in `internal/zammad`; `backdate.rb` (columns, pending reminder, reindex, marker last), satisfies **AC-7**, **AC-8**, **AC-9**, **AC-10**
5. Rebuild and hygiene: `make helpdesk-seed` "already seeded" (with the token drift check) and needs reset paths, `make noord-reset`, the fiction hygiene test, the CI unit tests, and the `TEST_ZAMMAD=1` live tests (including a second LDAP import after the seed that leaves the organization, Ruben's roles, and the counts unchanged), satisfies **AC-9**, **AC-15**, **AC-16**
6. Close the side doors (added Oct 18, from the verify run): `bootstrap.rb` sets `user_create_account` and `user_lost_password` to false in its one settings step, beside `fqdn` and the timezone; `report.rb` adds both settings to its snapshot; a live test checks them and sends the two refused requests of AC-17; check the login page by hand and record the probed status in `verify.md`. `bootstrap.rb` and `report.rb` are inside the marker hash, so every existing local Zammad reads `needs_reset` afterwards: run `make helpdesk-reset` once. The two settings are not part of the fence (`already_seeded` comes from the marker alone), satisfies **AC-17**

## Consequences

**Positive**:
- Noord's helpdesk behaves like a real one: AD sign in, real lockouts, a stale KB, a believable backlog.
- One command rebuilds it with the same content and the same token, so the tool server and the eval never need hand edits after a reset.
- `internal/zammad` is written and tested once, here, and #9 builds its ticket tools on it.
- The Slack strand of the tracer bullet is live before #10, so #10 only adds the loop.

**Negative / tradeoffs**:
- **Heavy locally.** Zammad with Elasticsearch adds roughly 4 to 6 GB of memory; Docker Desktop may need its limit raised.
- **Ruby in a Go repo.** Two small Rails scripts depend on Zammad internals (the LDAP source, SSL certificate, and token models), so a Zammad upgrade can break them; the pin and the live tests catch it.
- **Slow resets.** A fresh Zammad takes minutes to start, so a full reset per eval run is too slow (see Follow-up for #20).
- **Lockouts from typos.** Mistyped Zammad passwords lock AD accounts. True to life, but it can surprise you during demos.
- **Thin documentation.** Zammad's KB publish call, the `From` header, and the LDAP internals are documented thinly or only in Zammad's code; the step 2 probes proved them, and the build proved the search index rebuild (see `verify.md`).
- **The guard on users is ours, not Zammad's.** The assistant token can create and edit Zammad users if anything ever calls those endpoints with it; only our client and #9's tool list prevent that.
- **The sync filter leans on `department`.** A Noord person without a department would never reach Zammad; seed validation already refuses one, and a hand made AD account outside the seed would be missed.
- **Slack free plan.** Message history older than 90 days is hidden, and Slack is a US hosted processor.

**Neutral**:
- `noord.yaml` gains one app group, so #15's access requests will see `APP-Helpdesk-Agents` like any other app group (owner approves).
- The agent now opens a Socket Mode connection whenever the tokens are set, so `make dev` with tokens set talks to the real workspace.

## Follow-up

- [x] Build step 2 first proves the unconfirmed facts and records each in `verify.md` (done 2026-10-18; Zammad's single LDAP base and its agent user policy changed this spec, journal 0019 and 0020; the search index rebuild moves to step 4): `rails r` in `zammad-railsserver`, the SSL certificate model, LDAP sources and their sync trigger, a token with a chosen value and its permission list, `X-On-Behalf-Of` (superseded by the `From` header), the KB publish call, the search index rebuild, `docker compose exec -e NAME` passing a value from the environment, relative paths in an `include`d override, and how many AD binds one failed Zammad sign in makes (if more than one, AC-5's "five wrong passwords" changes to the measured number). Any that fails gets a journal entry and a spec update.
- [ ] Feature #9: the ticket tools build on `internal/zammad` with the assistant token; confirm the assistant's rights cover ticket close and tag writes. No tool may write a Zammad user (Zammad would allow it, see the security model); the tool server's tool list test should say so.
- [ ] Feature #10: the identity check uses `users.info` with `users:read.email` against AD `mail` (the overlay), and replaces the holding reply.
- [ ] Feature #16: the KB search endpoint is not documented on Zammad's API pages; confirm it then. The "prefer the newer" rule can use the backdated `updated` dates.
- [ ] Feature #17: Zammad's Helm chart, the Rails steps as a Job or `kubectl exec` runner, the Elasticsearch `vm.max_map_count` init, and installing `ZAMMAD_TOKEN` from a Sealed Secret (the spec 0001 follow-up).
- [ ] Feature #20: a full Zammad reset is too slow per eval run; decide a faster per run cleanup (for example, delete tickets newer than the marker) there.
- [x] Spec 0004: its data model gains `svc-zammad` and `APP-Helpdesk-Agents` (done Oct 18; `internal/seed/AGENTS.md` updated by `/sync`).
- [ ] The community Zammad MCP named in `AGENTS.md` (from #8 on) stays optional; review it before you connect it.
