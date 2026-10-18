# Verify: Ticketing, knowledge base & Slack workspace · spec 0006 · updated 2026-10-18
_Steps derived from spec 0006 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

## Follow-up probe results (build step 2 gate)

Run on 2026-10-18 against Zammad `7.2.2-295796a2.docker` (vendored compose v17.5.2) and a fresh Samba directory.

| Probe | Result | Notes |
|---|---|---|
| `rails r` in `zammad-railsserver` | pass | about 60 s on a cold container, about 10 s warm. Output also carries Rails log lines, so each step prints one `NOORD_RESULT {json}` line for Go to parse |
| `docker compose exec -T -e NAME` passes a value from the environment | pass | the value never appears in the argument list |
| Relative paths in the `include`d override | pass | `../rails` and `../../../.dev/certs/ca.pem` resolve from `seed/helpdesk/zammad/`; `ports: !override` replaces Zammad's `8080:8080` with `127.0.0.1:58080:8080` |
| SSL certificate model | pass | `SSLCertificate.create!(certificate: pem)` with our dev CA; LDAPS to `samba:636` with `ssl_verify: true` works |
| LDAP source and its sync trigger | pass, **spec change owed** | `LdapSource` preferences plus `ImportJob.create!(name: 'Import::Ldap')` and `AsyncImportJob.perform_later`. Zammad has **one** `base_dn` for users and groups, so separate bases cannot be set. Workaround tested: base `OU=Noord,...`, user filter `(&(objectCategory=person)(objectClass=user)(department=*))`, `user_uid` `samaccountname`. Import: 85 created, 0 failed; `ruben.mulder` gets Admin and Agent; no `svc-*` or `adm.*` user. Journal 0019 |
| Token with a chosen value and its permission list | pass | `Token` makes its own value on create; bootstrap then sets `token` to `ZAMMAD_TOKEN`. `preferences: {permission: [ticket.agent, knowledge_base.reader]}` |
| Assistant token rights (AC-6) | **fails as written** | `users/me` 200, `GET /settings` 403, KB category create 403, answer edit 403, answer read 200, ticket create 201, but `POST /users` **201**: Zammad permits user search, create, and update to any `ticket.agent`. Journal 0020 |
| `X-On-Behalf-Of` | pass, **spec change owed** | deprecated; the `From` header does the same (login, email, or id). Customers acting through `From` may only create tickets in state `new`, so the state and owner are set afterwards |
| KB publish call | pass | `POST /api/v1/knowledge_bases/{kb}/answers/{id}/publish` sets `published_at`; the portal at `/help/en-us` shows it. The KB itself must be made in Rails (`kb_locales` is not a REST param), and Zammad makes the primary locale translation by itself. Journal 0021 |
| Search index rebuild | pass (2026-10-18, build) | `backdate.rb` runs `search_index_reload` on `Ticket` and `KnowledgeBase::Answer::Translation` (the answer model itself has no index). After the seed, a search for `hd-001` finds the ticket and a search for `SecureLink` finds the retired VPN article |
| Assistant token editing an Admin user (added by the spec update) | **403** (2026-10-18, build) | `PUT /api/v1/users/<ruben id>` with `ZAMMAD_TOKEN`, a note change and a role change: both `403 Not authorized`, Ruben unchanged. So the residual risk stays at customer accounts (create and edit), not admin takeover. `/architect` may want to narrow the spec's security model sentence |
| KB content row dates | not possible | `knowledge_base_answer_translation_contents` has only `id` and `body`, no timestamps. `backdate.rb` sets the answer's `created_at`, `updated_at`, `published_at` and the translation's `created_at`, `updated_at`, `edited_at`. The spec's backdated column list names content rows; it needs a one line fix |
| AD binds per failed Zammad sign in | 1 | measured with `badPwdCount`; web sign in needs the CSRF token from `GET /api/v1/signshow` first. AC-5's "five wrong passwords" holds: four more locked `eva.peters`, then her right password was refused; `yara.schouten` (seeded locked) was refused too. Journal 0022 |

Build paused here for `/architect` to settle the LDAP base and AC-6 (2026-10-18). Spec updated the same day: base `OU=Noord` with a `department` filter, and the user write guard moved into `internal/zammad`.

## Build results (2026-10-18)

- `make helpdesk-reset` from scratch: seeded in about 70 s after Zammad was healthy (85 users, 20 tickets, 41 articles, 6 categories, 15 answers); the whole reset took about 2 minutes. A second `make helpdesk-seed` says "already seeded".
- `TEST_ZAMMAD=1` live tests pass: content equals the files (ticket states, owners, authors, article types, every date), assistant rights, AD sign in and lockout through Zammad, and a second LDAP import that changes nothing.
- After `make directory-reset` alone: `make helpdesk-seed` still says "already seeded", Eva signs in to Zammad, and a further LDAP import changes nothing (AC-9).
- Two read only helpers joined the Rails steps for the live tests: `report.rb` (a content snapshot) and `ldapsync.rb` (one more LDAP import). Both are inside the marker hash.

## Verify steps · updated 2026-10-18
_Steps derived from spec 0006 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

### UI / manual
- [x] Open `http://127.0.0.1:58080`, sign in as `eva.peters` with `SEED_USER_PASSWORD`, raise a ticket in IT Support → ticket created, Eva sees only her own tickets → AC-5
- [x] At the Zammad login, type a wrong password for `eva.peters` five times, then run `make directory-login NAME=eva.peters` → locked; Zammad now refuses her right password too → AC-5
- [x] Run `make directory-lock NAME=yara.schouten`, then sign in to Zammad as her → refused → AC-5 (checked by `TestLiveSignInFollowsAD`, which locks her by failed binds and gets 401 from Zammad)
- [x] Open `http://127.0.0.1:58080/help/en-us` signed out → "Noord IT Help", 6 categories, 15 articles, including the SecureLink VPN and SMS sign in articles → AC-7
- [x] Search the KB for `SecureLink` → finds the retired VPN article (search index rebuilt) → AC-7
- [x] In the agent view, check tickets: hd-019 is pending reminder (owner Ruben), hd-018 and hd-020 are open with no owner, the rest closed by Ruben; dates run from August to October 2026 → AC-8
- [x] Create the workspace, app, and channels per `docs/runbooks/slack-workspace.md`; check the app settings match the manifest → AC-11 (done by hand 2026-10-18; the bot is in `#it-help`; demo accounts skipped for now, they matter from #10)
- [x] With both Slack tokens in `.env`, run `make dev` and DM the bot "hello" → one threaded holding reply within 5 seconds (record the time here); the agent log has one `slack_event_received` line with event id, channel id, and event type, and no message text → AC-12 (2026-10-18: `slack_connected` on the first attempt; reply 0.5 s after the message by Slack's own timestamps; one `message.im` log line, no text)
- [x] Mention the bot in `#it-help` → it answers in that message's thread; edit the message → no new reply → AC-12 (2026-10-18: one threaded reply, shown within 5 s; one `app_mention` log line; after the edit still 1 reply and no new log line. The exact gap is not readable: the bot has no `channels:history` scope, by design)
- [x] DM the bot a message that also mentions it → exactly one reply → AC-12 (2026-10-18: one reply 0.4 s later, one `message.im` log line)

### Commands
- [x] `make helpdesk-reset` → logs `helpdesk seeded` with users 85, tickets 20, categories 6, answers 15 → AC-1, AC-3, AC-4
- [x] `make helpdesk-seed` right after → `already seeded`, exit 0, no writes → AC-9
- [x] `docker compose ps` after `make dev` alone (Zammad down first) → no Zammad service starts → AC-1 (checked with `docker compose config --services`, which lists only `postgres` and `samba` without the profile; `make dev` also starts `postgres` by name)
- [x] `go run ./cmd/seed validate` → `seed/helpdesk is valid: 6 KB categories, 15 KB articles, 20 tickets` → AC-10
- [x] `set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -count=1 -run Live ./internal/seed/helpdesk/` after a reset → all pass → AC-4, AC-5, AC-6, AC-7, AC-8, AC-9, AC-16
- [x] `make directory-reset`, then `make helpdesk-seed` → `already seeded`; Eva still signs in to Zammad → AC-9
- [x] Change one KB article's text, run `make helpdesk-seed` → exit 1 asking for `make helpdesk-reset` (revert the change after) → AC-9
- [x] Set `ZAMMAD_TOKEN` to another valid value, run `make helpdesk-seed` → exit 1 asking for `make helpdesk-reset` (revert after) → AC-9
- [x] With Zammad stopped, `SEED_ZAMMAD_WAIT_TIMEOUT=20s make helpdesk-seed` → fails naming the URL and the timeout → AC-14
- [x] Run the agent with only `SLACK_BOT_TOKEN` set → refuses to start, naming `SLACK_APP_TOKEN`; with a bot token not starting `xoxb-` → fails naming the setting, never the value → AC-13
- [x] Run the agent with both Slack tokens unset → logs `slack_disabled`, `curl 127.0.0.1:<AGENT_HTTP_ADDR port>/healthz` → 200 → AC-13
- [x] Revoke the app level token, start the agent → exits 1 naming `SLACK_APP_TOKEN` → AC-13
- [x] `make check` → passes (unit tests with no network, including the user write guard and the fiction check) → AC-6, AC-15, AC-16

### Value sourcing
- [x] Pending reminder: hd-019's `pending_time` is the reset time plus 30 days, not a file date → Seed, pending reminder
- [x] Dates: an article written in the files as `2026-08-03 08:12` shows 08:12 in Zammad (Europe/Amsterdam), not 06:12 → Backdate, dates
- [x] KB title is `Noord IT Help` and categories carry their `kb.yaml` icons → Bootstrap, KB title; category icons
- [x] Ticket authors: customer articles are `web` from the customer, replies are `note` from `ruben.mulder`, none internal → Seed, From header; article type
- [x] Assistant token value equals `ZAMMAD_TOKEN` after every reset (`GET /api/v1/users/me` with it → `noord-assistant`) → Bootstrap, assistant token value
- [x] Holding reply text equals the constant in `internal/slack`, posted in the event's thread (`thread_ts`, else `ts`) → Slack listener, channel and thread; holding reply text

### Acceptance criteria coverage
- AC-1 compose test, reset, `make dev` check · AC-2 directory reset and live sign in · AC-3 reset command · AC-4 reset counts and live content test · AC-5 manual sign in and lockout, live sign in test · AC-6 live assistant rights, guard unit tests · AC-7 portal, KB search, live content test · AC-8 ticket checks, live content test · AC-9 rerun, changed file, changed token, directory reset · AC-10 validate · AC-11 runbook and app settings · AC-12 Slack DM and mention steps · AC-13 agent token steps · AC-14 wait timeout, unit tests for step errors · AC-15 `make check` fiction and redaction tests · AC-16 `make check` and the live tests

## /check verify run · 2026-10-18 · PASS

Every step above ran in this pass, from a fresh `make helpdesk-reset` (seeded in 103 s after Zammad was healthy). Highlights: five wrong passwords at the Zammad web login locked `eva.peters` in AD and Zammad then refused her right password; the public portal lists all 15 answers (7, 2, 3, 1, 1, 1 by category); a KB search for SecureLink finds the retired article; the pending reminder is the reset time plus 30 days; Slack DMs got their threaded reply in 0.57 s and 0.52 s, an `#it-help` mention got one, an edit got none, and the reply text equals the constant; the bot holds exactly the five manifest scopes and sits in the private `#it-escalations`; a bogus app token ends the agent naming `SLACK_APP_TOKEN`; `make check` passes.

Side effect: the hand made ticket #40023 ("Verify test: cannot open the shared drive", Eva) now sits in Zammad, so `TestLiveContentMatchesFiles` fails until the next `make helpdesk-reset`.
