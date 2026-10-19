# Verify: password reset links · spec 0011 · updated 2026-10-19
_Steps derived from spec 0011 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

Live run on 2026-10-19: the demo Slack account mapped to `jan.devries` (a `SYNC-Entra-Users` member), the tool server started with `READ_ONLY=false` for the run (the `.env` default stays `true`). It passed every step of AC-20 (tool calls, unfurl free link post, audit pairs, closed ticket with both notes, `resolved` conversation, fresh `pwdLastSet`), and found that Samba kept the old password valid for an hour (journal 0038, fixed in the image and the live test).

Before the live steps: copy the `RESET_*` lines from `.env.example` into `.env`, run `make directory-reset` (a Samba container made before `svc-pwreset` existed has no reset account), then `make dev`.

## UI / manual
- [x] As a seeded user who is not locked, DM the assistant "I forgot my password" → tool calls are `user_lookup` (code), `ticket_create`, `password_reset_link`; the thread gets the `reset_link` post with no link preview, then the model's reply ending in `footer_resolved` → AC-1, AC-20
- [x] Lock a seeded user (`make directory-lock NAME=<user>`), DM "I forgot my password" → a link is sent; after the reset the account is unlocked and the ticket's note says the lockout was cleared → AC-1, AC-11
- [x] DM "I'm locked out" as a locked user who does not mention the password → the unlock flow runs, no link → AC-1
- [x] Open the link twice in a browser, then set a new password → both GETs show the form for that account, the POST shows `done` → AC-7, AC-11
- [x] Open the same link again → the `invalid` page, status 410 → AC-7, AC-13
- [x] Submit two passwords that differ, then one shorter than the rules → the form again with the `fields` message (422); the link still works afterwards → AC-8
- [x] Submit a password the directory refuses (for example the account name in it) → the form with the `policy` message (422); a good password then works → AC-12
- [x] Ask for a second link in a new conversation before using the first → the first link gets 410 (voided), the second works → AC-4
- [x] Set the kill switch file to `read_only`, POST a valid link → `paused` page (503), audit `read_only`; switch back and the same link works → AC-9
- [x] Ask for links past `RESET_LINK_LIMIT` in `RESET_LINK_WINDOW` → the model says it is rate limited and leaves the ticket open → AC-3
- [x] DM "I forgot my password" as `ruben.mulder` (a member of `APP-Helpdesk-Agents`) → `password_reset_link` refuses `protected`, the model escalates with `privileged_request`, no link is posted → AC-3
- [x] DM "I forgot my password" as any seeded staff member in `SYNC-Entra-Users` (nearly everyone) → a link is sent; that group fences group changes, not resets → AC-3
- [x] Issue a link to a staff user, add them to `SG-IT-Admins` as the Administrator, then POST the link → `blocked` (403), audit `denied_policy` `group is protected`, the link stays live → AC-10

## Commands
- [x] `go run ./cmd/seed login --password '<new>' <user>` → binds; `make directory-login NAME=<user>` (the old seeded password) → refused at once, not after an hour (journal 0038) → AC-11, AC-20
- [x] `psql` as `noord`: `SELECT status, octet_length(token_hash), expires_at - created_at FROM reset_links` → `used`, 32, about 15 minutes; no plain token anywhere in the row → AC-4, AC-11
- [x] `SELECT tool_name, kind, outcome, result FROM audit_log WHERE tool_name IN ('password_reset_link','password_reset') ORDER BY id` → an intent and `ok` outcome for each; `result` holds `link_id`, `expires_at`, `expires_in_minutes`, `voided_previous`, `ticket_noted` (issue) and `was_locked`, `ticket_closed` (POST); no `reset_url`, token, or password → AC-4, AC-11, AC-19
- [x] `SELECT content FROM conversation_messages` for the thread, and the agent logs → no `/reset/` URL or token → AC-5, AC-19
- [x] Zammad ticket for the thread → the "link sent" note with its UTC expiry, then the "set a new password" note, state closed → AC-4, AC-11
- [x] `curl -i http://127.0.0.1:18082/reset/<token>` → 200, with `Cache-Control: no-store`, `Pragma: no-cache`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, and the CSP; no script in the page → AC-16
- [x] `curl -i http://127.0.0.1:18082/mcp` → 404; `curl -i http://127.0.0.1:18081/reset/x` on the MCP listener (`TOOLSERVER_HTTP_ADDR`) → 404 → AC-16
- [x] Start the tool server with only `RESET_PUBLIC_URL` set → it fails naming `RESET_HTTP_ADDR`, `RESET_LDAP_BIND_DN`, `RESET_LDAP_BIND_PASSWORD`; with `RESET_PUBLIC_URL=http://reset.example` it fails on the https rule; with no `RESET_*` the tool is absent from `tools/list` and nothing listens on 18082 → AC-17
- [x] Tool server logs at start → `reset_links_cleaned` with `rows` → AC-18
- [x] `DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 go test -race -run 'TestLive(ResetPassword|ResetAccount|PasswordPolicy|UserInChain)' ./internal/ldap/` → pass (reset and unlock work; svc-pwreset is refused an admin account, another attribute, a group, a create; svc-itsupport cannot reset) → AC-15
- [x] `TEST_DATABASE_URL=... go test -race ./internal/resetlink/ ./internal/db/ ./db/` → pass (one live link per account, one claim wins, revert, cleanup, grants) → AC-4, AC-13, AC-18
- [x] `go test -race ./internal/tools/ ./internal/resetpage/ ./internal/agent/ ./internal/config/` → pass → AC-2, AC-3, AC-5, AC-7 to AC-12, AC-14, AC-16, AC-17
- [x] `go test -race -run TestCanaryResetLink ./internal/agent/ ./internal/mcpserver/` → pass: no log or span holds the token, its hash, the password, or a seed name; no audit row holds the token, its hash, or the password; no stored message or model request holds the token → AC-5, AC-19

## Value sourcing
- [x] token: two links never share a token; each is 43 base64url characters → `crypto/rand`
- [x] `reset_url`: change `RESET_PUBLIC_URL` (with a trailing `/`) → the posted link is that base + `/reset/` + token, no double slash
- [x] `expires_at`, `expires_in_minutes`: set `RESET_LINK_TTL=20m` → the post and the model say 20 minutes, the row expires 20 minutes after `created_at` (database clock)
- [x] `directory_account`: the link always resets the requester matched from `_meta`; passing `{"account": "..."}` as an argument is `invalid_arguments`
- [x] ticket checked and noted: the `_meta` ticket; a ticket of another user gives `not_yours`, a closed one `ticket_closed`
- [x] escalation state: after an `escalate` in the thread → `already_escalated`; after a refused privileged request → `escalate_first`
- [ ] Zammad user and ticket owner: a requester with no Zammad user → `no_helpdesk_account`
- [x] note time: the "link sent" note shows `expires_at` in UTC as `YYYY-MM-DD HH:MM UTC`
- [x] limit count: `RESET_LINK_LIMIT=1` → the second link in the window is `rate_limited`
- [x] stored and model text: the stored `tool_result` is the success text plus JSON without `reset_url`
- [x] `reset_link` post: its minutes and URL come from the tool result held in the turn
- [x] GET account name: the page shows the row's `directory_account`
- [ ] rules values: the page's length matches the domain's `minPwdLength`; with LDAP down and nothing cached it shows `rules_generic`
- [x] POST account checks: disable the account after issuing → `blocked` (403), audit `account disabled`; the link stays live
- [x] protected groups: set `RESET_DENIED_GROUPS=SG-Finance` → a Finance member is refused `protected`; set it to a name that does not exist → every account is refused and the tool server logs `denied_group_unresolved`
- [x] POST audit fields: the `password_reset` rows carry the link row's Slack ids, thread, ticket, and account
- [ ] ambiguous write: a timeout after the modify, with `pwdLastSet` newer than the claim, counts as success
- [x] `ticket_closed`: with Zammad down at the POST → `done`, `ticket_closed` false, ticket still open

## Acceptance-criteria coverage
- AC-1: the forgot password DM, the locked and forgot DM, the locked only DM
- AC-2: the tools and dispatcher tests; `invalid_arguments`; the kill switch
- AC-3: the rate limit step, the `ruben.mulder` and `SYNC-Entra-Users` steps, the protected groups value step, and the refusal tests
- AC-4: the second link voids the first; the `reset_links` and audit queries
- AC-5: the stored message and log checks; the agent tests for the post failure texts
- AC-6: the dispatcher test with an unmatched `_meta` email
- AC-7: the two GETs; the 410 checks; the generic rules value step
- AC-8: the field error step
- AC-9: the kill switch step
- AC-10: the POST account check step and the `SG-IT-Admins` step
- AC-11: the happy path, `directory-login`, and the Zammad notes
- AC-12: the policy refusal step; the ambiguous write step
- AC-13: the used link 410; the concurrent claim test
- AC-14: the Zammad down step
- AC-15: the least privilege live tests
- AC-16: the curl header and route checks
- AC-17: the config start up checks
- AC-18: the cleanup log line and its DB test
- AC-19: the audit, message, and log checks, and the two canary tests
- AC-20: the first manual step plus `directory-login`
