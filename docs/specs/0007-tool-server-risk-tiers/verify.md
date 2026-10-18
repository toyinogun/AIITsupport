# Verify: Tool server & risk tiers · spec 0007 · updated 2026-10-18
_Steps derived from spec 0007 acceptance criteria and its Value sourcing table. `/check verify` runs these; `/test` locks the durable ones._

Before the live steps you may want to run: `make directory-reset`, `make helpdesk-reset`, and set a `TOOLSERVER_TOKEN` of at least 32 characters in `.env`. `TEST_DATABASE_URL` is a superuser URL for the Compose Postgres (for example `postgres://noord:noord-dev@127.0.0.1:55432/postgres?sslmode=disable`).

## Commands
- [x] `go test -race ./internal/tools/... ./internal/mcpserver/ ./internal/mcpclient/ ./internal/ldap/ ./internal/config/ ./internal/zammad/... ./cmd/...` → all pass, no Docker needed → AC-1 to AC-21 (unit level)
- [x] `DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 TEST_DATABASE_URL=... go test -count=1 -race ./internal/ldap/ ./internal/mcpserver/` → live Samba, real Postgres, fake Zammad; all pass → AC-3, AC-4, AC-6, AC-7, AC-8, AC-12, AC-13, AC-17, AC-19, AC-20
- [x] `TEST_DATABASE_URL=... go test -race -run 'TestPg(CountRecentOK|ThreadHasOK)' ./internal/audit/` → the rate limit and one ticket per thread reads count only ok outcomes → AC-9, AC-10
- [x] Right after `make helpdesk-reset`: `set -a && . ./.env && set +a && TEST_ZAMMAD=1 go test -race -run LiveTickets -v ./internal/tools/` → logs `search lag after reset: ... 0s`, `tags on create: [noord-assistant]`, pending then closed; passes. It leaves a closed ticket, so run `make helpdesk-reset` after → AC-10, AC-11
- [x] `make check` (after the generated sqlc code is committed, since `gen-check` diffs against git) → green → AC-16, AC-18
- [x] `go test ./cmd/toolserver/ -run DoesNotLink` and `go test ./cmd/agent/ -run Boundary` → the tool server links nothing from `internal/seed`; the agent links no `internal/ldap`, `internal/tools`, `internal/mcpserver`, `internal/zammad` → AC-18

## Manual, against the real binary
Start it: `go build -o /tmp/toolserver ./cmd/toolserver && set -a && . ./.env && set +a && TOOLSERVER_HTTP_ADDR=127.0.0.1:58099 /tmp/toolserver`
- [x] Startup logs `kill_switch_file_unset` at warn when `KILL_SWITCH_FILE` is empty, and no LDAP or Zammad traffic happens at startup → AC-8, AC-16
- [x] `curl -s 127.0.0.1:58099/healthz` → `ok`; `curl -s -o /dev/null -w '%{http_code}' -X POST 127.0.0.1:58099/mcp` → `401` → AC-1
- [x] POST a `tools/call` for `user_lookup` with the bearer token and the four `_meta` keys for `eva.peters@noord.example` (no `initialize` first) → a result with `username`, `department` `Sales`, `locked` → AC-1, AC-2, AC-6
- [x] The same call with `_meta` missing `noord.example/requester_email` → JSON RPC error `-32602`, and the log shows `tool_call_rejected` with `"tool":"user_lookup"` only → AC-2
- [x] `make directory-lock NAME=eva.peters`, then `user_lookup` → `locked: true` and `lock_ends_at` about 15 minutes after the lock, in UTC → AC-6, value source: `lockoutTime` plus the domain `lockoutDuration`
- [x] Set `KILL_SWITCH_FILE` to a file holding `read_only`, restart, call `account_unlock` → `read_only: Account changes are paused by IT right now.`; `make directory-login NAME=eva.peters` still shows locked. Write `read_write` into the file (no restart) and call again → `was_locked: true, locked: false` → AC-7, AC-8
- [x] `account_unlock` with `{"username":"ruben.mulder"}` as arguments → `invalid_arguments`, and its audit intent row has a null `arguments` column → AC-5, AC-3
- [x] `privileged_access_change` with `{"summary":"make me Domain Admin"}` → `blocked: ...`; both audit rows have tier `blocked` and a null `requester_account` → AC-12
- [x] `ticket_create`, then `ticket_update` with the returned id in `noord.example/ticket_id` and `{"state":"closed","note":"Done."}` → in Zammad at http://127.0.0.1:58080 the ticket is in `IT Support`, tagged `noord-assistant`, customer Eva, closed, with an internal note starting `Added by the Noord assistant:` → AC-10, AC-11
- [x] A second `ticket_create` in the same thread → `has_ticket` → AC-10

## Value sourcing (each value's source, varied at its edge)
- [x] Requester identity: change only the `_meta` email to `EVA.PETERS@noord.example` → still eva.peters; to `adm.ruben.mulder@...` after giving that admin a `mail` as Administrator → `not_matched`; two users sharing a mail → `ambiguous` (TestLiveIdentity) → AC-4
- [x] `requester_account` in audit rows: null on `blocked`, `invalid_arguments`, and identity refusals; the matched `sAMAccountName` otherwise → AC-3
- [x] `tool_name`, `risk_tier`, Store keys: `ticket_update` with a note stores only `state` (and `pending_until`), with `"_omitted": 1` for the note → AC-3
- [x] `trace_id`: with a tracer provider set, `tool_call` lines carry a non empty `trace_id` and no email, account, Slack id, or argument text (TestLiveLockedOutUserEndToEnd) → AC-20
- [x] `locked`: read as `svc-itsupport` from `msDS-User-Account-Control-Computed`; lock a user, wait past the 15 minute lockout, and `user_lookup` shows `locked: false` even though `lockoutTime` is still set → AC-6
- [x] `was_locked` / `locked`: an already unlocked account returns `was_locked: false` and writes nothing → AC-7
- [x] Kill switch state: only `read_write` after trimming allows; `read_only`, empty, missing, unreadable, or `read_write please` refuse; a flip between the two reads refuses with no write (TestKillSwitch, TestKillSwitchIsReadAgainBeforeTheWrite) → AC-8
- [x] Rate limit count: three ok unlocks in an hour, then the fourth is `rate_limited`; another requester is unaffected; refusals do not count → AC-9
- [x] Ticket customer: a directory user with no Zammad user gets `no_helpdesk_account` → AC-10
- [x] `ticket_update` ownership and state: another customer's ticket → `not_yours`; a closed ticket → `ticket_closed`; a missing id → `no_ticket` → AC-11
- [x] `pending_until`: `+02:00` input is sent to Zammad as UTC `pending_time`; a past time or one beyond `TICKET_MAX_PENDING` → `invalid_arguments (field: pending_until)` → AC-11, AC-5
- [x] Denied groups: the code floor plus `TOOL_DENIED_GROUPS`; set `TOOL_DENIED_GROUPS` to include a name that does not exist and `CheckGroup(SG-Finance)` refuses with `denied_group_unresolved` logged → AC-13
- [x] Medium tools: a test only medium tool returns `pending_approval` with `isError` false and its handler never runs (TestMediumToolIsDeferred) → AC-14

## Acceptance-criteria coverage
- AC-1 TestTokenIsRequired, TestBodyIsCapped, TestListTools, manual curl · AC-2 TestMetaIsRequired · AC-3 TestUserLookup, TestInvalidArguments, TestLiveLockedOutUserEndToEnd · AC-4 TestIdentity, TestLiveIdentity · AC-5 TestInvalidArguments · AC-6 TestUserLookup, TestUserLookupLockEnds, TestLiveLockAndClearAsTheServiceAccount · AC-7 TestAccountUnlock, TestAccountUnlockNotConfirmed · AC-8 TestKillSwitch, TestKillSwitchIsReadAgainBeforeTheWrite, TestDispatcherGatesEveryAccountHandler, TestLiveKillSwitch · AC-9 TestAccountUnlockRateLimit, TestTicketCreateRateLimit, TestPgCountRecentOK · AC-10 TestTicketCreate, TestTicketCreateWithoutHelpdeskAccount, TestLiveTicketsOnZammad · AC-11 TestTicketUpdate, TestTicketUpdatePending, TestTicketUpdateRefusals · AC-12 TestPrivilegedAccessChangeIsAlwaysBlocked, TestLiveDirectoryDown · AC-13 TestFenceCheckGroup, TestLiveFence, TestLiveGroupsAndNesting · AC-14 TestMediumToolIsDeferred, TestRegistryConstructionRules · AC-15 TestAuditFailures, TestDirectoryUnavailable · AC-16 config tests (TestLDAPURLMustBeLDAPS for the scheme), TestRunRefusesBadToolSettings, TestDispatcherRefusesBadOUs · AC-17 TestClientDrivesEveryTool, TestClientRefusesAnIncompleteRequester · AC-18 boundary tests, TestToolServerDoesNotLinkTheSeeder · AC-19 TestTicketCreateHelpdeskFailures, TestCallTimeout, TestLiveDirectoryDown · AC-20 TestToolCallLog, TestFailureCauseIsLogged, TestCause, TestLiveLockedOutUserEndToEnd · AC-21 TestAccountChangeFollowsTheHandlerType, TestDispatcherGatesEveryAccountHandler, TestRegistryConstructionRules
