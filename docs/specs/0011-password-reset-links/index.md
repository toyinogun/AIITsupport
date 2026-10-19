# 0011. Password reset links: a single use link from the tool server, a reset page on its own listener, and a ticket that closes when the password is set

**Date**: 2026-10-19
**Status**: Proposed

## Summary

When a Noord employee tells the assistant they forgot their password (or says they are locked out but the account is not locked), the assistant opens a ticket and sends them a reset link in the Slack DM. The link opens a small page served by the tool server, where they choose a new password; the tool server writes it to AD (Active Directory) with a new, narrow service account that can only reset staff passwords, clears any lockout, and closes the ticket with a note. The link works once, for 15 minutes, for that one account, and its secret never reaches the model, the message history, the audit log, or the logs. Anyone the existing identity check does not match gets no link.

## Rationale

Reasoning and the options weighed: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As a Noord employee who forgot my password, I want to DM the assistant and get a link where I set a new one myself, so that I am back at work without waiting for Ruben.
- As Noord's IT person, I want every reset to leave a ticket that closes only when the password was actually set, so that my helpdesk shows the truth and an unused link stays in my queue.
- As Noord's IT person, I want the account that can reset passwords to do nothing else, and the reset page to never expose the MCP endpoint, so that the new power adds as little risk as possible.
- As the engineer, I want the link's secret to stay out of the model, the stored history, the audit log, and every log line, so that a leak of any of those never yields a working link.

**Acceptance criteria**:
- **AC-1**: The system prompt gains a password section. When the user says they forgot their password or it expired, whatever the lock state, or says they are locked out and the requester block shows the account is not locked, the model calls `ticket_create` (a short title, a body describing the problem), then `password_reset_link`, then tells the user the link was sent in a separate message, that it works once and expires in `expires_in_minutes` minutes, that it also clears a lockout, and that the ticket closes when they finish. It never writes, guesses, or repeats a URL. A locked account with no mention of the password runs the unlock flow of spec 0008 AC-7 unchanged. This replaces the first sentence of spec 0008 AC-8.
- **AC-2**: `password_reset_link` is a tier `low` tool with no arguments (an empty object; anything else is `invalid_arguments`), registered only when the `RESET_*` settings are set (AC-17). It runs through the dispatcher's steps 2 to 8 like `account_unlock`: the identity match from `_meta`, the kill switch (step 7), and `TOOL_WRITE_LIMIT` (step 8, counting its own `ok` outcomes per Slack id). It acts only on the matched requester's account. A refused or failed call leaves the conversation `open`.
- **AC-3**: After step 8, the handler refuses in this order, each with an audit pair and no row written: no ticket id in `_meta` → `no_ticket`; the thread holds an `ok` `escalate` → `already_escalated`; the thread holds a `denied_policy` `privileged_access_change` → `escalate_first`; the requester has no Zammad user → `no_helpdesk_account`; the ticket's customer is not that user → `not_yours`; the ticket is closed or merged → `ticket_closed`; the account is disabled (`userAccountControl` bit `0x2`) → `account_disabled` (new); the account is a member, at any depth, of a code floor or `TOOL_DENIED_GROUPS` group → `protected`; `RESET_LINK_LIMIT` rows already exist for the account with `created_at` inside `RESET_LINK_WINDOW` → `rate_limited`. Every audit read comes before any Zammad call, as in `escalate`. The prompt has the model escalate `account_disabled` with `security_concern` and `protected` with `privileged_request`; on `rate_limited` or `read_only` it says so and leaves the ticket open. `TOOL_WRITE_LIMIT` and `RESET_LINK_LIMIT` both apply; page outcomes (`password_reset`) count toward neither.
- **AC-4**: On success the handler, in one Postgres transaction, takes `pg_advisory_xact_lock` on the account, sets every `live` row for the account to `voided`, then inserts the new row with `sha256(token)`, the requester ids and ticket id from `_meta`, the issuing `call_id`, and `expires_at` = the database's `now()` + `RESET_LINK_TTL`. The token is 32 bytes from `crypto/rand`, base64url without padding (43 characters). After the commit it adds one internal note to the ticket: `Password reset link sent to the requester. Single use, valid until <expires_at as YYYY-MM-DD HH:MM UTC>.` A failed note does not fail the call (`ticket_noted` false). The result is `ok` with `link_id`, `expires_at`, `expires_in_minutes`, `voided_previous` (count), `ticket_noted`, and `reset_url` (`RESET_PUBLIC_URL` + `/reset/` + token). The audit row's StoreResult keeps every key except `reset_url`.
- **AC-5**: Agent code removes `reset_url` from an `ok` `password_reset_link` result right after `callTool` returns, before `toolResultText`, any store, the IT post's `Ran:` list, or any model request, and keeps it only in the turn's memory. The stored `tool_result` and what the model sees are `Reset link sent to the requester.`, a newline, and the JSON of the other keys. `statusChangeFor` gains a case: this result sets the conversation `resolved`, keeping its ticket id, in the same transaction as the stored result. Only when that transaction commits and the conversation was `open` (not `ErrNotOpen`) does the agent post the fixed `reset_link` text in the thread with `ReplyNoUnfurl` (link and media unfurling off). Any further model call in the turn uses `tool_choice` none, and the final reply carries `footer_resolved`. If the `reset_link` post fails, the agent posts the fixed `reset_link_failed` text instead of the model's final reply. If the turn fails after `resolved` but before the final reply, the user gets `resolved_link_no_summary` (spec 0008 AC-6's exemption now keys on the conversation being `resolved`, whichever tool resolved it). The token appears in no `conversation_messages` row, no Anthropic request, no `audit_log` row, no log line, and no span.
- **AC-6**: Nobody the identity check rejects gets a link: a user who fails the Slack gate or is `not_matched` or `ambiguous` never reaches the model (spec 0008 AC-3, AC-4), and a direct `password_reset_link` call whose `_meta` email matches no one, or two accounts, is refused at dispatcher step 6 with no row written.
- **AC-7**: `GET /reset/{token}` on the reset listener: a token that is not 43 base64url characters, unknown, `used`, `voided`, or past `expires_at` (checked with the database's `now()`) gets the fixed `invalid` page with status 410; a malformed token gets it without a database read. A valid one gets 200 with the form: the account's `sAMAccountName` (shown, and in a hidden `autocomplete="username"` field), two password fields (`autocomplete="new-password"`), and the rules text. The rules text comes from the domain root's `minPwdLength`, `pwdProperties` bit `0x1`, and `pwdHistoryLength`, read with the tool server's existing reader and cached up to 5 minutes; when that read fails and nothing is cached, the page shows the fixed `rules_generic` line instead (still 200). GET does one indexed Postgres read and at most one LDAP read, changes no row, and writes no audit row.
- **AC-8**: `POST /reset/{token}` reads only the form body (`PostFormValue` for `password` and `confirm`; a query string never counts; body capped at 4 KiB, read before any other work). Fields that are empty, differ, exceed 256 characters, or are shorter than the cached `minPwdLength` (skipped when unknown; AD still enforces it) get the form again with the fixed `fields` message and status 422, with no database write, no audit row, and no AD call.
- **AC-9**: The POST then takes an in flight slot (AC-16), and from here runs under one deadline of `TOOL_CALL_TIMEOUT`. With account changes paused (the kill switch reads anything but `read_write`, so `AccountChangesAllowed` is false), a valid link gets the fixed `paused` page with status 503 and an audit refusal (`read_only`, `account changes are paused`). The link stays `live` and no AD write happens.
- **AC-10**: Before writing, the POST checks the account again with the reader: still found, under a `TOOL_USER_OUS` entry, enabled, and in no protected group. Any failure gets the fixed `blocked` page with status 403 and an audit refusal (`denied_identity` `requester not matched` when not found; else `denied_policy` with `account disabled` or `group is protected`, a DN outside the OUs counting as `group is protected`). The link stays `live`.
- **AC-11**: A valid POST claims the link with one guarded UPDATE (`status` `live` → `used`, `used_at` = `now()`, only when unexpired) that commits before anything touches AD; no rows updated means the `invalid` page (410). Then it runs inside `audit.Recorder.Run` as tool `password_reset` (fields in the *POST audit call* table). Inside, after a second kill switch read (paused: revert, `paused` page), it binds as `svc-pwreset` and makes one LDAP modify on the account: replace `unicodePwd` (the new password in quotes, UTF-16LE) and replace `lockoutTime` with `0`, with the AD password policy hints control (OID `1.2.840.113556.1.4.2239`, not critical, value BER `SEQUENCE { INTEGER 1 }`; if build step 1 shows Samba ignores it, the rules text drops the history line and the journal records it). On success, when the ticket is neither closed nor merged, the tool server adds the note `The requester set a new password with the reset link.` (plus `The account lockout was cleared.` when `was_locked`) and sets the ticket `closed`, using the same `Helpdesk` calls `ticket_update` uses; `ticket_closed` is true only when the close succeeded. The page shows the fixed `done` page (200).
- **AC-12**: On any outcome that is not a success, the POST reverts the claim (`used` → `live`, `used_at` null, guarded on its own `used_at`) unless the link has expired meanwhile. An AD refusal for policy (LDAP result 19 `constraintViolation` or 53 `unwillingToPerform`) gets the form again with the fixed `policy` message and status 422, and the audit outcome `error` with reason `password rejected by policy`. A timeout or a connection lost after the modify was sent first rereads `pwdLastSet` with the reader: newer than the claim's `used_at` means success (AC-11 continues with the ticket); otherwise revert. Any other failure gets the fixed `failed` page with status 502 and the spec 0007 classification. `ErrAuditUnavailable` before the action reverts the claim and gets the fixed `try_later` page with status 503. If a revert itself fails, the link stays `used` (fail closed) and `reset_submit` logs the `cause`.
- **AC-13**: Two POSTs on the same link at once: exactly one claims it; the other gets the `invalid` page (410) and changes nothing. A `used` link never writes AD again, even after a crash between the claim and the write.
- **AC-14**: When Zammad fails after a successful AD write, the page still shows `done`, the audit outcome is `ok` with `ticket_closed` false, the ticket stays open with its AC-4 note, and `reset_submit` logs the Zammad `cause`.
- **AC-15**: `svc-pwreset` is a new account in `OU=Service Accounts` (password `SAMBA_PWRESET_PASSWORD`, no expiry) with exactly two inherited rights on user objects under `OU=Users`: the Reset Password extended right and write on `lockoutTime`. A live test shows it can reset a staff password and clear a lock (and records whether `badPwdCount` is 0 afterwards), and that AD refuses it (insufficient access) for an account in `OU=Admin Accounts`, a change to any other attribute, a group membership change, and object creation. `svc-itsupport` still cannot reset a password (spec 0004 AC-11 holds). Only the reset write binds as `svc-pwreset`. `internal/resetlink` and `internal/resetpage` are on both agent deny lists.
- **AC-16**: The reset listener on `RESET_HTTP_ADDR` serves only `GET` and `POST` `/reset/{token}` and `GET /healthz`; `/mcp` there is 404, and the MCP listener serves no `/reset` path. Its server sets `ReadTimeout` 10 s, `WriteTimeout` `TOOL_CALL_TIMEOUT` + 5 s, `IdleTimeout` 60 s, and `MaxHeaderBytes` 8 KiB (the MCP listener keeps today's settings). At most `RESET_MAX_INFLIGHT` POSTs past AC-8 run at once; one more gets the `try_later` page (503). Every reset response sets `Cache-Control: no-store`, `Pragma: no-cache`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`, and, when `RESET_PUBLIC_URL` is https, `Strict-Transport-Security: max-age=31536000`. Pages hold no script. No OpenTelemetry HTTP middleware wraps this listener.
- **AC-17**: `LoadToolServer` treats `RESET_PUBLIC_URL`, `RESET_HTTP_ADDR`, `RESET_LDAP_BIND_DN`, and `RESET_LDAP_BIND_PASSWORD` as one set: all unset means the feature is off (no tool in the registry, so `tools/list` omits it, and no listener); a partial set fails naming each missing setting. `RESET_PUBLIC_URL` follows the `ZAMMAD_PUBLIC_URL` rules (a host, no credentials, query, or fragment, trailing `/` trimmed) and must be https unless its host is `localhost`, `127.0.0.1`, or `[::1]`. Ranges: `RESET_LINK_TTL` 5 to 60 minutes, `RESET_LINK_LIMIT` at least 1, `RESET_LINK_WINDOW` at least 1h and no longer than `TICKET_RETENTION_DAYS`, `RESET_MAX_INFLIGHT` at least 1. `.env.example` matches `internal/config`.
- **AC-18**: The tool server runs a cleanup at start and then hourly that deletes `reset_links` rows whose `expires_at` is older than now minus `TICKET_RETENTION_DAYS`, bounded by a 5 minute timeout, logging `reset_links_cleaned` (`rows`) or `reset_links_cleanup_failed` (`cause`). It never fails the binary.
- **AC-19**: No log line or span attribute holds the token, its hash, a password, or an account name; no audit row holds the token, its hash, or a password (audit rows keep account names in `requester_account` and `target`, as every tool does). The page logs one `reset_submit` line per POST past AC-8 (`outcome` from the fixed set below, `cause`, `trace_id`) inside a `reset.submit` span named by the route pattern, never the path. A canary test (spec 0010) runs issue, GET, and POST with a marked token, password, and account and checks all of this, plus the stored messages and the model request for the token.
- **AC-20**: The happy path, live against `make dev` with real Slack, Samba, Zammad, and the Anthropic API: a seeded user who is not locked DMs "I forgot my password". The tool calls are `user_lookup` (code), `ticket_create`, `password_reset_link`; the thread gets the `reset_link` post (no preview) and the model's reply with `footer_resolved`. Opening the link and setting a new password succeeds; `make directory-login NAME=<user>` binds with the new password; the ticket is closed with both notes; `audit_log` holds the `password_reset_link` and `password_reset` pairs; the conversation is `resolved`.

## Decision

**Chosen option**: Option 1: the tool server issues a hashed single use link and serves the reset page on its own listener, writing the password with a dedicated `svc-pwreset` account.

The agent's model calls a new `password_reset_link` tool after the usual identity check and ticket; agent code, not the model, posts the link; the tool server's reset page sets the password and closes the ticket. The credential rule (only the tool server holds client system credentials) stays whole. The reset is tier `low` (no IT approval) on purpose, against spec 0007's habit of putting the highest impact writes higher: approval would defeat self service, and the link's shape carries the risk instead (see Consequences).

**Implementation skills**: `golang-security` · `golang-database` · `golang-error-handling` · `golang-concurrency` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `claude-api` (built in: the prompt and tool use) · `testcontainers-go` (`testcontainers/claude-skills`, `.agents/skills/testcontainers-go/`, the live Samba tests). Open each only when building in its area.

## Feature design

**The reset, end to end**

| Step | Who | What happens |
|---|---|---|
| 1 | Agent code | Slack gate and `user_lookup` as spec 0008; a stranger never reaches the model (AC-6) |
| 2 | Model | `ticket_create`, then `password_reset_link` (AC-1) |
| 3 | Tool server | dispatcher steps 2 to 8, then the AC-3 checks; voids older links, inserts the row, notes the ticket; returns the result with `reset_url` (AC-4) |
| 4 | Agent code | strips `reset_url`, stores the result and sets `resolved` in one transaction, posts `reset_link` with no unfurl (AC-5) |
| 5 | Model | final reply with `tool_choice` none; agent appends `footer_resolved` |
| 6 | Employee | opens the link: `GET /reset/{token}` shows the form (AC-7) |
| 7 | Reset page | `POST`: field checks, slot, kill switch, account recheck, claim, the audited AD write, ticket note and close; revert on failure (AC-8 to AC-13) |

An unused link simply expires: the row stays `live` past `expires_at`, the ticket stays open with its "link sent" note for IT, and the next link for the account voids it.

**Data model sketch** (migration `00003_reset_links.sql`; confirmed in the design conversation, then the attempt counter dropped after the cross check)

`reset_links`, written only by the tool server:

| Column | Type | Null | Notes |
|---|---|---|---|
| `id` | uuid | PK | `gen_random_uuid()`; the `link_id` in results and audit rows |
| `token_hash` | bytea | not null, unique | `sha256(token)`; `CHECK (octet_length(token_hash) = 32)` |
| `directory_account` | text | not null | the matched `sAMAccountName`, the only account the link can reset |
| `requester_slack_id` | text | not null | from `_meta` |
| `slack_channel_id`, `slack_thread_ts` | text | not null | the issuing thread |
| `zammad_ticket_id` | bigint | not null | the thread's ticket, closed on success |
| `issue_call_id` | uuid | not null | the issuing call's `call_id` in `audit_log` |
| `status` | text | not null | default `live`; `CHECK (status IN ('live', 'used', 'voided'))`; `voided` always means replaced by a newer link |
| `expires_at` | timestamptz | not null | `now()` + `RESET_LINK_TTL`, set in SQL |
| `created_at` | timestamptz | not null | default `now()` |
| `used_at`, `voided_at` | timestamptz | null | |

Constraints: `CHECK ((status = 'used') = (used_at IS NOT NULL))`, `CHECK ((status = 'voided') = (voided_at IS NOT NULL))`. Indexes: unique partial `(directory_account) WHERE status = 'live'`; `(directory_account, created_at)` for the limit. No hard foreign keys: `issue_call_id` points softly to `audit_log.call_id` (unique only with `kind`), the thread to `conversations`, the ticket to Zammad. Grants (same migration, plus a row in `internal/db/grants_test.go`): `noord_toolserver` SELECT, INSERT, DELETE, and UPDATE (`status`, `used_at`, `voided_at`); nothing for `noord_agent` or `noord_eval`.

Queries (`db/queries/reset_links.sql`, behind `internal/resetlink.Store`; every time comparison uses the database's `now()`): `LockAccountForIssue` (`pg_advisory_xact_lock(hashtextextended('reset_links:' || $1, 0))`), `VoidLiveForAccount` (`:execrows`), `InsertResetLink` (`:one`), `CountRecentForAccount` (`:one`), `GetValidResetLinkByHash` (`:one`, `live` and unexpired, for GET and the POST precheck), `ClaimResetLink` (`:one`, the guarded `live` → `used` UPDATE, returning the row), `RevertResetLinkClaim` (`:execrows`, guarded on `id`, `status = 'used'`, the claim's `used_at`, and `expires_at > now()`), `DeleteExpiredResetLinks` (`:execrows`).

**State transitions** (`reset_links.status`)

- `live → used`: the POST's claim (AC-11).
- `used → live`: the revert after a failed write, only while unexpired (AC-12). A `used` row that was not reverted is final.
- `live → voided`: a newer link for the account (AC-4). Final.
- "Expired" is not a state: `live` with `expires_at` in the past, treated as invalid everywhere.
- `conversations.status`: `open → resolved` with an `ok` `password_reset_link` result (AC-5).

**Tool registry change** (spec 0007's table otherwise unchanged)

| Tool | Tier | Handler deps | Arguments | Target | Store | StoreResult |
|---|---|---|---|---|---|---|
| `password_reset_link` | `low` | new `ResetDeps` (directory reader, helpdesk, history, `resetlink.Store`, `RESET_*` values; no directory writer), an account change handler type, so steps 7 and 8 apply | none | the requester's account | (none) | `link_id`, `expires_at`, `expires_in_minutes`, `voided_previous`, `ticket_noted` |

Description (what the model reads): `Send the requester a single use link to set a new password themselves; it also clears a lockout. Use it when they forgot their password or it expired, or when they report a lockout but the account is not locked. Needs this conversation's ticket first (call ticket_create). The assistant's code posts the link; never write or repeat a URL yourself. Ends the conversation.` Success text: `Reset link sent to the requester.`

New refusal row: `account_disabled` · `denied_policy` · `account disabled` · `This account is disabled, so no reset link can be sent.` Reused: `invalid_arguments`, `not_matched`, `ambiguous`, `read_only`, `rate_limited`, `no_ticket`, `already_escalated`, `escalate_first`, `no_helpdesk_account`, `not_yours`, `ticket_closed`, `protected` (stored reason `group is protected`), `unavailable`, `failed`.

**POST audit call** (`audit.Call` built by `internal/resetpage`, since no MCP request exists)

| Field | Value |
|---|---|
| `Tool` / `Tier` | `password_reset` / `low` |
| `RequesterSlackID`, `ChannelID`, `ThreadTS`, `TicketID` | the claimed row's `requester_slack_id`, `slack_channel_id`, `slack_thread_ts`, `zammad_ticket_id` |
| `RequesterAccount`, `Target` | the row's `directory_account` |
| `Args` / `Store` | `{"link_id": <id>}` / `link_id` |
| `StoreResult` | `was_locked`, `ticket_closed` |
| `TraceID` | the `reset.submit` span's trace id (the page starts a new trace) |
| approval fields | none |
| context | the POST's deadline; the outcome write uses `recordCtx` like the dispatcher |

`password_reset` is not a registry tool: it is never in `tools/list` and no MCP call can reach it.

**API surface**

| Endpoint | Method | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| MCP `password_reset_link` | tools/call | none; `_meta` requester, thread, ticket | `link_id`, `expires_at`, `expires_in_minutes`, `voided_previous`, `ticket_noted`, `reset_url` | bearer `TOOLSERVER_TOKEN` plus `_meta` identity | the refusals above |
| `/reset/{token}` | GET | token (path) | form page | the token | 410 invalid |
| `/reset/{token}` | POST | token (path); `password`, `confirm` (form body) | `done` page | the token | 403 blocked, 410 invalid, 422 fields or policy, 502 failed, 503 paused or try later |
| `/healthz` | GET | none | `ok` | none | 405 other methods |

Pages are `html/template` files embedded with `go:embed`, plain form POST to the same path, inline CSS, no script, `lang="en"`. No cookies, so there is no ambient session to forge (no CSRF surface beyond the token itself).

**Fixed texts** (the eval and tests assert on them)

Agent (`internal/agent/replies.go`):

| Key | Text |
|---|---|
| `reset_link` | `Here is your password reset link. It works once and expires in <expires_in_minutes> minutes:` a newline, then `<reset_url>` |
| `reset_link_failed` | `I made you a password reset link but couldn't post it here. Please send me a new message (not in this thread) to get a fresh one.` |
| `resolved_link_no_summary` | `I've sent your password reset link above, but I couldn't finish my reply. The ticket closes when you set your new password.` |

Reset page (`internal/resetpage`, one template per page; `<rules>` is the AC-7 rules text):

| Key | Status | Text |
|---|---|---|
| form heading | 200 | `Set a new password for <account>` then `<rules>` |
| `rules_generic` | (200) | `Use a long password you haven't used here before. If it is refused, the page will say so.` |
| `rules` | (200) | `At least <minPwdLength> characters.` plus, when complexity is on, `Use three of: capital letters, small letters, numbers, symbols. Don't include your username.` plus, when history is enforced, `Don't reuse one of your last <pwdHistoryLength> passwords.` |
| `fields` | 422 | `The two passwords must match and follow the rules below.` |
| `policy` | 422 | `The directory refused that password. Please choose another one that follows the rules below.` |
| `done` | 200 | `Your password is changed. You can sign in now.` |
| `invalid` | 410 | `This link is no longer valid. Send the IT assistant a new message in Slack to get a fresh one.` |
| `paused` | 503 | `Password changes are paused right now. Please contact IT.` |
| `blocked` | 403 | `This link can't be used for this account. Please contact IT.` |
| `failed` | 502 | `The password could not be changed. Please try again, or contact IT.` |
| `try_later` | 503 | `The service is busy. Please try again in a minute.` |

**Value sourcing**

| Action | Value produced / displayed | Source |
|---|---|---|
| `password_reset_link` | token | `crypto/rand`, 32 bytes, base64url |
| `password_reset_link` | `reset_url` | `RESET_PUBLIC_URL` + `/reset/` + token |
| `password_reset_link` | `expires_at`, `expires_in_minutes` | database `now()` + `RESET_LINK_TTL`; the TTL in whole minutes |
| `password_reset_link` | `directory_account` | dispatcher identity match (`_meta` email → `user_lookup`), never an argument |
| `password_reset_link` | ticket to check and note | `_meta` ticket id (`Requester.TicketID`), set by agent code from the conversation row |
| `password_reset_link` | escalation state | `ThreadHas` on `escalate` `ok` and `privileged_access_change` `denied_policy` |
| `password_reset_link` | Zammad user, ticket owner and state | `UsersByLogin(requester_account)`, `GetTicket`, the `Helpdesk` cached states |
| `password_reset_link` | note time `YYYY-MM-DD HH:MM UTC` | `expires_at` formatted in UTC |
| `password_reset_link` | limit count | `CountRecentForAccount` over `RESET_LINK_WINDOW` |
| agent | stored and model text | success text + JSON of the result minus `reset_url` |
| agent | `reset_link` post | `reset_url` and `expires_in_minutes` from the tool result, held in the turn |
| GET page | account name | `reset_links.directory_account` |
| GET page, POST length check | rules values | domain root `minPwdLength`, `pwdProperties` bit `0x1`, `pwdHistoryLength`, read by `svc-itsupport`, cached 5 minutes; `rules_generic` when unknown |
| POST | account DN, enabled, groups, OU, `was_locked` | the reader's `GetUser` and `InChainMemberOf` on `directory_account` at submit time |
| POST | audit call fields | the claimed row and the span (*POST audit call* table) |
| POST | success after an ambiguous failure | the reader's `pwdLastSet` compared with the claim's `used_at` |
| POST | `ticket_closed` | whether the `Helpdesk` close succeeded |

**Key invariants**

- At most one `live` row per account (the partial unique index); issuing takes the account's advisory lock and voids first, in the same transaction.
- The database holds only the token's hash; the plain token exists only in the tool result, the agent's turn memory until the post, and the Slack message.
- A link is `used` (committed) before AD is touched, so a crash can never leave a reusable link; a failure reverts it only while unexpired.
- No AD write without an audit intent row first (`Recorder.Run`).
- GET is side effect free, so Slack or any link preview fetching the URL cannot use or burn it.
- The link resets only `directory_account`; nothing in the request names an account.
- `svc-pwreset` is used only by the POST's write; every read on the page path uses `svc-itsupport`.
- Every expiry check uses the database clock, never the Go clock.

**Security model**

- Who may get a link: a requester who passed the Slack gate and matched exactly one directory account (spec 0008), enabled, under `TOOL_USER_OUS`, in no protected group, in a thread with no escalation or refused privileged attempt, with an open ticket of their own, under both rate limits.
- Who may use it: whoever holds the URL within `RESET_LINK_TTL`, once. The DM is the only delivery path, so a stolen Slack session can reset that person's password; this is accepted and recorded (see Consequences).
- AD: `svc-pwreset` holds Reset Password and `lockoutTime` write on users under `OU=Users` only; AD itself refuses everything else, under the code fence.
- Exposure: the reset listener is separate from MCP and https outside loopback; in the cluster it is reached only through the Tailscale ingress (#17), like the admin UIs. The token is in the URL path, so ingress access logs must not record paths for this host (#17).
- Compliance: personal data (account names) only; GDPR minimisation through the retention cleanup, with the permanent record in the append only audit log.

**Observability**

- Logs: `reset_submit` (`outcome` ∈ `ok`, `invalid_link`, `paused`, `blocked`, `policy`, `failed`, `try_later`, `revert_failed`; `cause`; `trace_id`), `reset_links_cleaned` (`rows`), `reset_links_cleanup_failed` (`cause`). The tool's own call logs `tool_call` as every tool does. All keys are already on the `internal/logging` allow list.
- Spans: `reset.submit` (attribute `outcome` only), with the `password_reset` audit call inside it.

**Configuration required** (`Reset` struct, embedded in `ToolServer`; `LDAP_URL`, `LDAP_CA_CERT`, and `TOOL_CALL_TIMEOUT` are reused)

- `RESET_PUBLIC_URL`: the browser base URL in links (local `http://127.0.0.1:18082`).
- `RESET_HTTP_ADDR`: the reset listener (local `:18082`).
- `RESET_LDAP_BIND_DN`, `RESET_LDAP_BIND_PASSWORD`: `svc-pwreset`. Compose passes the password to Samba as `SAMBA_PWRESET_PASSWORD`, like `LDAP_BIND_PASSWORD` today.
- `RESET_LINK_TTL` (default `15m`), `RESET_LINK_LIMIT` (default `3`), `RESET_LINK_WINDOW` (default `24h`), `RESET_MAX_INFLIGHT` (default `4`, kept below the tool server's pgx pool size so audit writes never starve).

**Critical test scenarios**

- Happy path: live DM "I forgot my password" → link → new password → bind works, ticket closed, conversation resolved, verifies **AC-1**, **AC-4**, **AC-5**, **AC-11**, **AC-20**.
- Locked and forgot: a locked user who says they forgot gets a link, and the reset also unlocks, verifies **AC-1**, **AC-11**.
- Token hygiene: the canary token and password appear in no log, span, audit row, stored message, or model request, verifies **AC-5**, **AC-19**.
- Identity: an unmatched `_meta` email and an ambiguous one are refused at step 6 with no row; a disabled, a protected, and an escalated thread are refused, verifies **AC-3**, **AC-6**.
- Link lifecycle: a second link voids the first (the first gets 410); an expired link gets 410; a used link gets 410, verifies **AC-4**, **AC-7**.
- Preview safety: two GETs then a POST still succeed; a password in the query string is ignored, verifies **AC-7**, **AC-8**.
- Concurrency: two simultaneous POSTs give one success, one 410, and one AD write; two simultaneous issues leave one `live` row, verifies **AC-4**, **AC-13**.
- Failure and revert: a policy refusal reverts the claim and a retry with a good password works; a timeout with a newer `pwdLastSet` counts as success; a crash after the claim leaves the link `used`, verifies **AC-12**, **AC-13**.
- Kill switch flipped after issue: 503, link still works after switching back, verifies **AC-9**.
- Zammad down at success: `done` page, `ticket_closed` false, ticket open, verifies **AC-14**.
- Post failure: a failing `reset_link` post sends `reset_link_failed` and no model reply, verifies **AC-5**.
- Least privilege: `svc-pwreset` refused on an admin account, another attribute, a group, a create; `svc-itsupport` refused a reset, verifies **AC-15**.
- Listener split: `/mcp` is 404 on the reset listener, `/reset/x` is 404 on the MCP listener, headers and timeouts present, verifies **AC-16**.
- Config: a partial `RESET_*` set fails naming each missing setting; an http public URL on a real host fails; none set leaves the tool out of `tools/list`, verifies **AC-17**.

## Build plan

Tracer Bullet: the first task proves the whole pipe (Slack → agent → tool server → Postgres → reset page → AD → Zammad → audit) on one real reset, the riskiest piece (Samba honouring a delegated reset) first; the later tasks thicken it.

1. **Tracer thread.** Probe first, live: in the Samba entrypoint, `svc-pwreset` with the two ACEs (Reset Password extended right `00299570-246d-11d0-a768-00aa006e0529` and `lockoutTime` write, `CIIO` on user objects under `OU=Users`), `SAMBA_PWRESET_PASSWORD` in Compose, and a `TEST_SAMBA=1` test that resets and unlocks a staff user with the policy hints control and checks whether reusing the current password is refused; log a journal entry if Samba refuses the delegated reset or ignores the control. Then: migration `00003_reset_links.sql` with grants and the grants test row; the queries and `make gen`; `internal/resetlink` (token, hash, `Store`); the `Reset` config struct and `.env.example`; `ldap.PasswordWriter.SetPassword`; the `password_reset_link` registry row and handler (ticket checks, issue, note); `internal/resetpage` with the GET form and the POST happy path (claim, `Recorder.Run` with the audit call table, ticket note and close); `server.Run` taking per listener options and the second listener in `cmd/toolserver`; the agent's strip, `statusChangeFor` case, `ReplyNoUnfurl` (interface, Slack adapter, `agenttest.Poster`), and `reset_link` post; the prompt section. One live DM through `make dev` ends with a working new password, satisfies **AC-1**, **AC-2**, **AC-4**, **AC-5**, **AC-7**, **AC-11**, **AC-15**, **AC-20**.
2. **The other doors.** The AC-3 refusals and `account_disabled`; the unmatched and ambiguous checks; the POST field checks, the kill switch page, the account recheck with its audit refusals, the revert paths, the `pwdLastSet` reread, the claim race, satisfies **AC-3**, **AC-6**, **AC-8**, **AC-9**, **AC-10**, **AC-12**, **AC-13**.
3. **Failure and operations.** Zammad failure after the write; the `reset_link_failed` and `resolved_link_no_summary` paths; the all or none config with its ranges and the https rule; the hourly cleanup; the in flight cap, timeouts, route split, and headers; the issue advisory lock, satisfies **AC-4**, **AC-5**, **AC-14**, **AC-16**, **AC-17**, **AC-18**.
4. **Proof and docs.** The canary test over issue, GET, and POST; `internal/resetlink` and `internal/resetpage` on depguard `agent-credential-boundary` and `cmd/agent/boundary_test.go`; the least privilege live test with the `badPwdCount` check; `docs/runbooks/password-reset.md` (what IT sees, how to void a link by hand, `make directory-reset` for a Samba container older than this feature); the amendment notes listed under Neutral, satisfies **AC-15**, **AC-19**.

## Consequences

**Positive**:
- The second half of the Monday lockout pile becomes self service, with a ticket that closes only on a real reset.
- The power to reset passwords sits in one narrow account used by one code path; `svc-itsupport` gains nothing.
- The token never reaches the model, the history, the audit log, or the logs, so the existing redaction and retention work needs no new exception.
- Claim before write means no crash or database outage can leave a reusable link.

**Negative / tradeoffs**:
- Slack is the only identity factor: anyone holding a staff member's Slack session can reset that person's AD password, through a tier `low` tool with no approval. Accepted for the demo, written down for the handover; a second factor is the follow up.
- The tool server, the process with every client credential, now serves a page a browser reaches. The separate listener, Tailscale only ingress, https, no script, timeouts, and the in flight cap narrow this, but it is new attack surface.
- The token travels in the URL path, so browser history and any proxy that logs paths can hold it until it expires or is used.
- A second Samba service account and its secret to rotate and seal; an older Samba container needs `make directory-reset`.
- Entra passwords are not synced from Samba (spec 0005), so after a reset the Entra password differs; Entra sign in is out of scope here.
- If a revert fails after a refused write, the link stays `used` and the user must ask for a new one (fail closed).
- `resolved` no longer means "ticket closed": a conversation that sent a link is `resolved` while its ticket stays open until the reset.

**Neutral**:
- Amendment notes to add when built: spec 0008 AC-2 (a `resolved` thread after a link still gets `closed_thread`), AC-6 and AC-10 (the resolved exemption keys on `resolved`, not on a closed ticket), AC-8 (the not locked branch), and the status table; spec 0007 AC-1 and spec 0009 AC-1 (the exact `tools/list` set gains `password_reset_link` when configured) plus the registry and refusal tables; spec 0004 (`svc-pwreset` beside `svc-itsupport`, whose AC-11 is unchanged).
- `internal/server`'s rule "never start a second `http.Server`" becomes "one `server.Run` per listener, with per listener options; the reset listener is the only second one".
- The new password's UTF-16LE buffer is not zeroed (Go strings are immutable and the heap copies them); low risk, noted only.

## Follow-up

- [ ] #17: a Tailscale ingress for `RESET_HTTP_ADDR` only (never `/mcp`) with https and no path logging, `RESET_PUBLIC_URL` as a chart value, and `RESET_LDAP_BIND_PASSWORD` sealed with the other secrets.
- [ ] #19: eval tickets for "forgot my password" (expected tools `ticket_create`, `password_reset_link`), locked and forgot, the not locked lockout, a leaver asking for a reset (`account_disabled`, escalate), and a protected account.
- [ ] Phase 2: a second factor on the reset page (for example a code sent to a channel Slack does not control) before a real client relies on this.
- [ ] Phase 2: Entra password parity (writeback or a hybrid identity sync) if Entra sign in matters to the client.
- [ ] Consider raising Noord's domain `minPwdLength` from Samba's provisioned default; the page reads it, so no code change.
- [ ] `/sync`: root `AGENTS.md` (the `RESET_*` settings, the reset listener, `svc-pwreset` beside the credential rule), nested `AGENTS.md` for `internal/resetlink` and `internal/resetpage`, `internal/server/AGENTS.md` (the second listener and its options), `internal/ldap/AGENTS.md` (`PasswordWriter`), `internal/tools/AGENTS.md` (the new tool), `internal/agent/AGENTS.md` (the strip and `ReplyNoUnfurl`), `db/AGENTS.md` (the new table), `internal/config/AGENTS.md`.
