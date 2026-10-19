# 0007. Tool server: an audited MCP dispatcher with risk tiers, an OU fence, and a kill switch

**Date**: 2026-10-18
**Status**: Accepted

## Summary

This spec turns the empty tool server into the only path from the agent into Noord's systems. Every tool call passes one dispatcher (the single piece of code every call goes through) that checks, in a fixed order: whether the tool is blocked, whether the arguments are valid, who is asking, whether account changes are paused, and whether the requester has hit a limit. Only then does the call run, inside the audit recorder, so every call leaves an audit row. #9 ships five tools: `user_lookup`, `account_unlock`, `ticket_create`, `ticket_update`, and one always refused `privileged_access_change`. Account tools can only ever act on the requester's own account, and the agent learns who the requester is from agent code, never from the model.

## Rationale

Reasoning and the options weighed: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As Noord's IT person, I want the agent to reach AD and Zammad only through code that refuses anything outside a fixed fence, so that no prompt, however clever, can change an admin account or a privileged group.
- As Noord's IT person, I want one switch that pauses every account change at once while tickets keep flowing, so that I can stop the agent acting without losing the requests.
- As a locked out Noord employee, I want the agent to unlock my own account from Slack, and only mine, so that I'm back to work in under a minute and nobody can unlock me by pretending to be me.
- As the engineer, I want every call, allowed or refused, recorded with who asked and why it was refused, so that the eval and the video can show exactly what the tool layer stopped.

**Acceptance criteria**:
- **AC-1**: The tool server serves MCP (Model Context Protocol) over streamable HTTP in stateless mode at `/mcp`, behind a bearer token check that compares the SHA-256 digests of the presented token and `TOOLSERVER_TOKEN` with `subtle.ConstantTimeCompare` (so the token's length does not leak). Request bodies over `TOOLSERVER_MAX_REQUEST_BYTES` are cut off. A missing or wrong token gets HTTP 401, runs no tool, and writes no audit row. `GET /healthz` stays open and touches no database, LDAP, or Zammad. `tools/list` returns exactly `user_lookup`, `account_unlock`, `ticket_create`, `ticket_update`, and `privileged_access_change`, each with the description in the tool registry below and an input schema that allows no extra keys, and never anything that approves or decides an approval.
  - *Amended by [spec 0009](../0009-refusal-escalation/index.md) AC-1:* `tools/list` returns six names, adding `escalate` (tier `low`).
  - *Amended by [spec 0011](../0011-password-reset-links/index.md) AC-2 and AC-17:* when the `RESET_*` settings are set, `tools/list` adds a seventh name, `password_reset_link` (tier `low`); with them unset it is the six of spec 0009.
- **AC-2**: Every `tools/call` must carry these `_meta` keys, set by agent code, each an exact string with no trimming: `noord.example/requester_slack_id` (matches `^[UW][A-Z0-9]{2,20}$`), `noord.example/requester_email` (`net/mail.ParseAddress` yields an address equal to the input, at most 254 characters, no control characters), `noord.example/channel_id` (`^[CDG][A-Z0-9]{2,20}$`), `noord.example/thread_ts` (`^\d{10}\.\d{6}$`), and optionally `noord.example/ticket_id` (decoded as a `json.Number`, an integer from 1 to 2^53 − 1). Other `_meta` keys (such as `progressToken`) are ignored, and identity is never read from anywhere else. A call missing a required key, or with a malformed value, gets a JSON RPC invalid params error (code −32602) from SDK receiving middleware, runs no tool, and logs `tool_call_rejected` at warn level with the tool name only. No tool takes the requester, a target account, or a ticket id as an argument.
- **AC-3**: Every call that passes AC-1 and AC-2 writes exactly one intent row and one outcome row in `audit_log` (spec 0003), whether it runs, is refused, or fails, with two exceptions from spec 0003: when the intent cannot be written (`ErrAuditUnavailable`) nothing runs and no row exists, and when the outcome cannot be written (`ErrAuditIncomplete`) only the intent exists. Each row carries the tool's tier from the registry, the requester and thread from `_meta`, `requester_account` from AC-4 (null when there is no match, and null on rows refused at the blocked or validation step, which run before identity), and `target`. Only the keys in the Store table below are kept, and only after the arguments validated: a call refused as `invalid arguments` stores no arguments. The free text arguments (`title`, `body`, `note`, `summary`) never reach `audit_log`.
- **AC-4**: The requester's directory account is the one user found by the identity filter in the design below whose `mail` equals `noord.example/requester_email` case insensitively (`strings.EqualFold` after Samba's match), that sits under an allowed user OU, and is enabled. With zero or more than one such user, every tool except `privileged_access_change` is refused as `denied_identity` with reason `requester not matched` or `requester ambiguous`.
- **AC-5**: Arguments are checked against the tool's schema and its `Check` rules before identity or any other system is touched. An unknown key, a wrong type, a value outside an enum, a string length outside its bounds (counted in runes), a string that is only whitespace, a control character other than `\n` and `\t` (and any `\n` in `title`), a time that does not parse as RFC 3339, or a broken cross field rule is refused as `denied_policy` with reason `invalid arguments`; the handler never runs.
- **AC-6**: `user_lookup` (tier `read`) takes no arguments and returns the requester's own `username`, `display_name`, `department` (empty string when unset), `enabled`, `locked`, and `lock_ends_at` (RFC 3339 in UTC; null when not locked, or when the domain lockout lasts until an admin clears it).
- **AC-7**: `account_unlock` (tier `low`, an account change) takes no arguments and acts only on the requester's account. If the account is not locked, it writes nothing and returns `was_locked: false, locked: false` with outcome `ok`. If it is locked, it writes `lockoutTime` 0, reads the lock flag again, and returns `was_locked: true, locked: false` with outcome `ok`; if the flag is still set, the outcome is `error` with reason `unlock not confirmed`.
- **AC-8**: An account change runs only when the kill switch file holds exactly `read_write` after trimming whitespace, read twice during the call: at the dispatcher check, and again inside the handler immediately before the directory write. Any other content, an empty file, a missing file, or a read error refuses the call as `read_only` with reason `account changes are paused`. When `KILL_SWITCH_FILE` is unset, `READ_ONLY` decides, and startup logs `kill_switch_file_unset` at warn level. Flipping the file takes effect on the next call with no restart. `user_lookup` and the ticket tools are never blocked by it.
- **AC-9**: Before an account change, the dispatcher counts this requester's `ok` outcomes for that tool in the last `TOOL_WRITE_WINDOW` (a no change unlock with `was_locked: false` counts too). At `TOOL_WRITE_LIMIT` or more, the call is refused as `denied_policy` with reason `rate limited`. `ticket_create` has the same check with `TICKET_CREATE_LIMIT`.
- **AC-10**: `ticket_create` (tier `low`) is refused as `denied_policy` with reason `thread already has a ticket` when `_meta` carries a ticket id, or when `audit_log` already holds an `ok` `ticket_create` outcome for this channel and thread. Otherwise it creates a ticket in `ZAMMAD_DEFAULT_GROUP` with the tag `noord-assistant`, the given `title`, and `body` as the first article (type `note`, sender `Customer`, not internal, `text/plain`, subject = `title`), whose customer is the active Zammad user whose login equals `requester_account` exactly. No such Zammad user means `denied_identity` with reason `no helpdesk account`. It returns `ticket_id` and `ticket_number`.
- **AC-11**: `ticket_update` (tier `low`) changes only the ticket named in `_meta`. No ticket id, or a 404 from Zammad, means `denied_policy` with reason `no ticket for this thread`. No Zammad user for the requester means `denied_identity` with reason `no helpdesk account`. A ticket whose customer is not the requester's Zammad user means `denied_policy` with reason `ticket belongs to someone else`. A ticket in a closed or merged state means `denied_policy` with reason `ticket is closed`. It takes `state` (`open`, `pending`, or `closed`, sent to Zammad as `open`, `pending reminder`, or `closed`), `pending_until` (required exactly when `state` is `pending`, in the future, at most `TICKET_MAX_PENDING` ahead, sent as `pending_time`), and `note` (an internal article, type `note`, sender `Agent`, prefixed with `Added by the Noord assistant:`); at least one of `state` or `note` is required. State and note go in one Zammad update request, so one cannot succeed without the other. It returns `ticket_id` and `state`.
- **AC-12**: `privileged_access_change` (tier `blocked`) is listed with a truthful description. It is the first check after `_meta`: its handler never runs and the call never touches LDAP or Zammad. Every call is refused as `denied_policy` with reason `tool is blocked`, with `risk_tier` `blocked` and `requester_account` null in both audit rows, whatever the arguments say and whether AD is up or down.
- **AC-13**: `Fence.CheckGroup(ctx, name)` takes a sAMAccountName and refuses, as `denied_policy` with reason `group is protected`, a group that does not exist, is outside the allowed group OUs, is named in the code floor or in `TOOL_DENIED_GROUPS`, or is nested (at any depth) into a group on either list. A code floor name that does not resolve in this directory is skipped; a `TOOL_DENIED_GROUPS` name that does not resolve makes every `CheckGroup` call refuse and logs `denied_group_unresolved`. Against the live seeded directory it refuses `SG-IT-Admins`, `Domain Admins`, `APP-Helpdesk-Agents`, `SYNC-Entra-Users`, a group that does not exist, and a test group nested into `SG-IT-Admins`, and allows `SG-Finance`. No setting can remove a code floor group.
- **AC-14**: A tool of tier `medium` goes to `audit.Recorder.Defer` with the approval request its registry entry's `Approval` hook builds, never runs its handler, and returns a result with `isError` false, code `pending_approval`, and the text "This request is waiting for IT approval." An error from the hook is outcome `error`, code `failed`. Proven with a test only medium tool; #9 registers no real one, and the test registry builder cannot add tools to the production `tools/list`.
- **AC-15**: Every refusal and failure reaches the model as a normal tool result with `isError` true, text `<code>: <sentence>` from the refusal table below, and the same `code` in the structured content. It never echoes an argument value, an account name, or a directory or Zammad error message; the `invalid arguments` sentence may name the schema field that failed, never its value. `ErrAuditUnavailable` gives `failed` and logs `audit_unavailable`. `ErrAuditIncomplete` after a successful action gives the model the real result (so it does not retry) and logs `audit_outcome_incomplete`.
- **AC-16**: `LoadToolServer` requires `LDAP_URL`, `LDAP_BASE_DN`, `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD`, `LDAP_CA_CERT`, `ZAMMAD_URL`, `ZAMMAD_TOKEN`, and `ZAMMAD_DEFAULT_GROUP`. `LDAP_URL` must be an `ldaps://` URL with a host, the only kind the dialer opens. It refuses to start when an allowed OU list is empty, or an entry is empty, fails `ldap.ParseDN`, or contains a `DC=` component (entries are relative to `LDAP_BASE_DN`). `TOOLSERVER_TOKEN` must be at least 32 characters, on both binaries. Startup checks configuration only, never reachability. `.env.example` matches `internal/config`.
- **AC-17**: `internal/mcpclient` calls a tool only with a `Requester` built by agent code, and sets the token (through its HTTP transport) and every `_meta` key from it; a call with an empty Slack id, email, channel, or thread fails with `ErrIncompleteRequester` before any network request. Each `Call` connects, initializes, calls, and closes, bounded by the caller's context, and returns `Result{Data, Text, IsError, Code}`. A test drives each #9 tool through it against a running tool server.
- **AC-18**: `internal/ldap`, `internal/tools`, and `internal/mcpserver` are on both agent deny lists (`.golangci.yml` and `cmd/agent/boundary_test.go`); `cmd/toolserver` never imports `internal/seed` (checked by a test); `internal/seed/directory` uses `internal/ldap` for dialing, binding, and lock state.
- **AC-19**: Every call runs under a `TOOL_CALL_TIMEOUT` deadline. An LDAP network error, timeout, or deadline gives outcome `error` with reason `directory unavailable`; a Zammad 5xx, network error, or timeout gives `helpdesk unavailable`; any other error (LDAP insufficient access, a Zammad 4xx not named in an AC) gives `action failed`. The model gets the matching refusal table text. No write is retried.
- **AC-20**: Each call opens one span `tool.call` with attributes `tool`, `tier`, and `outcome`, and logs one `tool_call` line at info level with only `tool`, `tier`, `outcome`, and `trace_id`. A call that fails (outcome `error`, after its audit pair is written) also logs one `tool_call_failed` line at warn level with only `tool`, `code`, `trace_id`, and `cause`, a fixed token naming where the error came from (`ldap_<result code>`, `zammad_<status>`, `deadline`, `canceled`, `network`, `audit`, or `other`), never error text. No account name, email, Slack id, or argument value appears in a log line or span attribute.
- **AC-21**: Handlers are typed by what they may touch. An account change handler receives `AccountDeps` (the only holder of the directory writer); every other handler receives `Deps` (directory reads and the helpdesk client). A tool is an account change exactly when its handler takes `AccountDeps`, so the kill switch and rate limit cannot be skipped by forgetting a flag; registry construction fails when a medium tool has no `Approval` hook.

## Decision

**Chosen option**: Option 1: one audited dispatcher in the tool server, with tiers in a code registry, a code floor plus config for the fence, and identity matched by email.

The tool server owns a registry of tools in Go; each entry names its tier, description, schema, `Check` rules, Store keys, and a typed handler. One dispatcher runs every call through the same ordered checks and then through `audit.Recorder`. The agent side gets a thin MCP client that can only send identity from agent code.

**Implementation skills**: `golang-security` · `golang-error-handling` · `golang-concurrency` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `testcontainers-go` (`testcontainers/claude-skills`, `.agents/skills/testcontainers-go/`) · `mcp-server-patterns`, `golang-testing` (`everything-claude-code` plugin) · `opentelemetry` (`grafana/skills`, `.claude/skills/opentelemetry/`, spans only). Open each only when building in its area.

## Feature design

**Data model sketch**

No new table and no migration. #9 writes only `audit_log` and, for medium tools, `approvals`, through `internal/audit` exactly as spec 0003 defines them. It adds two read queries in `db/queries/audit.sql`, reached through new `audit.Recorder` methods so the audit package stays the single owner of `audit_log` access:

| Query | Kind | Role | Inputs | Output | Recorder method |
|---|---|---|---|---|---|
| `CountRecentOKOutcomes` | `:one` | toolserver | `tool_name`, `requester_slack_id`, `since` | bigint: outcome rows with `outcome = 'ok'` for that tool and requester with `occurred_at > since` | `CountRecentOK(ctx, tool, requesterSlackID, since)` |
| `ThreadHasOKOutcome` | `:one` | toolserver | `tool_name`, `slack_channel_id`, `slack_thread_ts` | bool: an `ok` outcome exists for that tool in that thread | `ThreadHasOK(ctx, tool, channelID, threadTS)` |

The existing `audit_log (occurred_at)` and `audit_log (slack_channel_id, slack_thread_ts, id)` indexes serve them; at Noord's volume (tens of calls a day) no new index is needed.

**The dispatcher pipeline** (the order is part of the contract)

| Step | Check | On failure |
|---|---|---|
| 1 | Body cap and bearer token (HTTP middleware) | 401, no audit row (AC-1) |
| 2 | `_meta` present and well formed (SDK receiving middleware on `tools/call`) | JSON RPC −32602, no audit row (AC-2) |
| 3 | Tier `blocked` | `Refuse` `denied_policy`, `tool is blocked`, no arguments, no requester account (AC-12) |
| 4 | Schema validation plus the entry's `Check` | `Refuse` `denied_policy`, `invalid arguments`, no arguments stored (AC-5) |
| 5 | Resolve identity (the filter below) | lookup error: `Run` with an action returning `WithReason("directory unavailable", err)` (AC-19). No match or many: decided at step 6 |
| 6 | Identity required | `Refuse` `denied_identity`, `requester not matched` or `requester ambiguous` (AC-4) |
| 7 | Account change (handler takes `AccountDeps`): kill switch | `Refuse` `read_only`, `account changes are paused` (AC-8) |
| 8 | Account change, or `ticket_create`: rate limit | `Refuse` `denied_policy`, `rate limited` (AC-9) |
| 9 | Tier `medium` | `Defer` (AC-14) |
| 10 | Run the handler inside `Recorder.Run` | handler refusals return `audit.Result{Outcome: OutcomeDeniedPolicy or OutcomeDeniedIdentity, Reason: <fixed>}`; errors are classified per AC-19 and wrapped with `audit.WithReason`; an account change handler reads the kill switch again just before the write and returns `read_only` if it changed |

The SDK itself answers a call to an unknown tool name; that call has no registry entry, tier, or handler, so it writes no audit row. Schema validation (step 4) runs in our dispatcher, not only in the SDK, so a rejected call is audited; if the MCP Go SDK validates typed inputs first and answers with a protocol error, register tools with raw handlers and our own validator. Our validator parses RFC 3339 times itself rather than relying on a schema `format`.

**Tool registry**

| Tool | Tier | Handler deps | Arguments (schema) | `Check` rules | Target | Store (args) | StoreResult |
|---|---|---|---|---|---|---|---|
| `user_lookup` | `read` | `Deps` | none | none | requester account | none | none (read tier) |
| `account_unlock` | `low` | `AccountDeps` | none | none | requester account | none | `was_locked`, `locked` |
| `ticket_create` | `low` | `Deps` | `title` string (required, 1 to 200 runes), `body` string (required, 1 to 10000 runes) | `title` has no `\n` | requester account | none | `ticket_id`, `ticket_number` |
| `ticket_update` | `low` | `Deps` | `state` enum (optional), `pending_until` string (optional), `note` string (optional, 1 to 10000 runes) | at least one of `state` or `note`; `pending_until` present exactly when `state` is `pending`, RFC 3339, in the future, at most `TICKET_MAX_PENDING` ahead | the `_meta` ticket id, as text | `state`, `pending_until` | `ticket_id`, `state` |
| `privileged_access_change` | `blocked` | none (never runs) | `summary` string (required, 1 to 1000 runes) | none | none | none | none |

Every string also passes the whitespace only and control character rules of AC-5. The string bounds and the allowed `state` values are named constants in the registry file: they are part of the schema the model sees, so they change through code review, not config. Each entry may carry an `Approval func(ctx, audit.Call) (audit.ApprovalRequest, error)`, required for tier `medium`. `tools.NewRegistry()` builds the production set; tests use `tools.NewTestRegistry(extra ...Tool)`, which lives in a `_test.go` helper package so it cannot reach the binary.

Descriptions (what the model reads in `tools/list`) and the one line success text:

| Tool | Description | Success text |
|---|---|---|
| `user_lookup` | Look up the requester's own Noord account: whether it is enabled, whether it is locked, and when the lock ends. Takes no arguments and can only return the requester's own account. | Looked up the requester's account. |
| `account_unlock` | Unlock the requester's own Noord account if it is locked. Takes no arguments and only ever acts on the requester's own account; it cannot unlock anyone else. Reports whether the account was locked. | Account unlocked and confirmed. · Account was not locked; nothing changed. |
| `ticket_create` | Open a helpdesk ticket for the requester for this conversation, with a short title and a plain text description of the problem and what was done. One ticket per conversation; use ticket_update after that. | Ticket created. |
| `ticket_update` | Update this conversation's ticket: add an internal note, set the state (open, pending with pending_until, or closed), or both. Only this conversation's ticket can be changed. | Ticket updated. |
| `privileged_access_change` | Use for any request for admin rights, privileged group membership, or changes to admin accounts. It is always refused; tell the user, and escalate to IT. | (never succeeds) |

*Amended by [spec 0011](../0011-password-reset-links/index.md):* `password_reset_link` joins both tables when configured: tier `low`, a new `ResetDeps` handler (the directory reader, the helpdesk, the history, the reset link store; no directory writer) that the dispatcher treats as an account change, so the kill switch and `TOOL_WRITE_LIMIT` apply; no arguments; target the requester account; StoreResult `link_id`, `expires_at`, `expires_in_minutes`, `voided_previous`, `ticket_noted`. Its description and success text (`Reset link sent to the requester.`) are in spec 0011, Tool registry change.

**Refusal table** (the text the model sees, AC-15; the audit `error` column stores the reason)

| Code | Audit outcome | Reason (stored) | Sentence the model sees |
|---|---|---|---|
| `blocked` | `denied_policy` | `tool is blocked` | Privileged changes are never made by the assistant. Escalate to IT. |
| `invalid_arguments` | `denied_policy` | `invalid arguments` | The arguments did not match the tool's schema (field: `<schema field name>`). |
| `not_matched` | `denied_identity` | `requester not matched` | No single active Noord account matches this Slack user's email. |
| `ambiguous` | `denied_identity` | `requester ambiguous` | More than one Noord account matches this Slack user's email. |
| `no_helpdesk_account` | `denied_identity` | `no helpdesk account` | This account has no helpdesk profile. |
| `read_only` | `read_only` | `account changes are paused` | Account changes are paused by IT right now. |
| `protected` | `denied_policy` | `group is protected` | That group is outside what the assistant may change. |
| `rate_limited` | `denied_policy` | `rate limited` | Too many recent requests of this kind for this user. |
| `has_ticket` | `denied_policy` | `thread already has a ticket` | This conversation already has a ticket; update it instead. |
| `no_ticket` | `denied_policy` | `no ticket for this thread` | This conversation has no ticket yet. |
| `not_yours` | `denied_policy` | `ticket belongs to someone else` | That ticket belongs to another user. |
| `ticket_closed` | `denied_policy` | `ticket is closed` | That ticket is already closed; open a new conversation for a new problem. |
| `unavailable` | `error` | `directory unavailable` or `helpdesk unavailable` | The directory (or helpdesk) cannot be reached right now. |
| `not_confirmed` | `error` | `unlock not confirmed` | The unlock was sent but the account still shows as locked. |
| `failed` | `error` | `action failed` (or none, when the audit write failed) | The action failed. |
| `pending_approval` | `pending_approval` | none | This request is waiting for IT approval. (`isError` false) |

*Amended by [spec 0011](../0011-password-reset-links/index.md):* a new row, `account_disabled` · `denied_policy` · `account disabled` · This account is disabled, so no reset link can be sent. It is a second layer only: step 6 already refuses a disabled requester as `not_matched` (AC-4). `password_reset_link` also returns `protected` (stored reason `group is protected`) for an account in a protected group, judged by the code floor plus `RESET_DENIED_GROUPS`, not `TOOL_DENIED_GROUPS`.

**Kill switch** (spec 0001 left the semantics to #9)

- File content is the whole state: exactly `read_write` (after trimming) allows account changes; anything else, including `read_only`, means paused. The file is read with `os.ReadFile` each time (no cache): at dispatcher step 7 and again inside the account change handler just before the LDAP modify. A `kubectl edit configmap` takes effect on the next call.
- Only account change tools check it (AC-21). In #9 that is `account_unlock`; #14 and #15 add theirs. Ticket tools and reads never check it, so in read only mode every request still becomes a ticket.
- `KILL_SWITCH_FILE` unset is for local dev and tests: `READ_ONLY` decides (default `true`). The Helm chart (#17) always mounts the file and sets the path.
- After the second read, only the LDAP modify itself is left unguarded: a flip during that one round trip lets the call finish. Accepted.

**Identity**

- Filter: `(&(objectClass=user)(objectCategory=person)(mail=<ldap.EscapeFilter(email)>)(!(userAccountControl:1.2.840.113556.1.4.803:=2)))`, searched from `LDAP_BASE_DN` with a size limit of 50, then kept only where the DN is under an allowed user OU and `strings.EqualFold(mail, email)`. One entry is a match; none is `not_matched`; more than one is `ambiguous`.
- Attributes read: `sAMAccountName`, `displayName`, `department`, `mail`, `userAccountControl`, `lockoutTime`, and `msDS-User-Account-Control-Computed` (a constructed attribute, so requested by name).
- The match requires an enabled user under `TOOL_USER_OUS`, so admin accounts (`OU=Admin Accounts`) and service accounts can never be a requester or a target. This is why `account_unlock` needs no separate OU check.

**The fence** (code floor plus config)

- Allowed user OUs (`TOOL_USER_OUS`) and allowed group OUs (`TOOL_GROUP_OUS`) are DNs relative to `LDAP_BASE_DN`. "Under" means: parse both DNs with `ldap.ParseDN` and check the object's RDN sequence ends with the OU's RDNs followed by the base's, comparing attribute types and values case insensitively. Never a raw string suffix match. Users are fenced by OU alone, by your choice: with unlock limited to the requester, the OU check is enough for users.
- Denied groups = the code floor ∪ `TOOL_DENIED_GROUPS` (sAMAccountNames). The code floor, which no setting can remove: `Domain Admins`, `Enterprise Admins`, `Schema Admins`, `Administrators`, `Account Operators`, `Backup Operators`, `Server Operators`, `Print Operators`, `Group Policy Creator Owners`, `DnsAdmins`, `Cert Publishers`, `Key Admins`, `Enterprise Key Admins`. Floor names this directory lacks are skipped; configured names that do not resolve fail closed (AC-13).
- A group target is protected when it does not exist, is outside the allowed group OUs, is itself denied, or is a member at any depth of a denied group. Nesting is checked with one LDAP search per denied group DN using the AD matching rule for chains: `(&(objectClass=group)(sAMAccountName=<EscapeFilter(name)>)(memberOf:1.2.840.113556.1.4.1941:=<EscapeFilter(denied DN)>))`, resolved once per call. If Samba does not honor that rule (checked in build step 1), the fallback walks `memberOf` breadth first with a depth cap of 10 and treats hitting the cap as protected.
- Filter values are always escaped with `ldap.EscapeFilter`; DNs are built only through the DN helpers moved from `internal/seed/directory` (which escape values).
- #9 has no tool that takes a group, so the guard ships as `tools.Fence.CheckGroup` with its own live tests (AC-13). #15 calls it.

**Zammad specifics**

- `UsersByLogin(ctx, login)`: `SearchUsers` with the quoted query `login:"<login>"`, then keep only users whose `login` equals the value exactly and who are `active`. If the live test shows the `login:` syntax unsupported, fall back to a bare value query with the same client side filter. Zammad's search index lags a fresh LDAP import (spec 0006); a live test searches right after `make helpdesk-reset` to measure it, and a miss returns `no_helpdesk_account` (the agent escalates).
- `TicketStates(ctx)`: `GET /api/v1/ticket_states`, loaded once per process and cached by name to id, to map `open`, `pending reminder`, `closed`, and to recognise closed and merged states on `GetTicket`'s `StateID`.
- `UpdateTicket` gains an optional embedded `Article` so state and note are one `PUT /api/v1/tickets/:id`. No `From` is sent: the `noord-assistant` token acts as itself.
- Tags: `NewTicket.Tags` on create. If the live run shows the token cannot set tags on create, add `POST /api/v1/tags/add` after create; if that is forbidden too, drop the tag from AC-10 and record it in `rationale.md`.
- `TestEveryMethodIsCovered` gains every new method.

**Package layout**

| Package | Owns |
|---|---|
| `internal/ldap` | `Dialer` (LDAPS only, CA verified, moved from `internal/seed/directory`), `BaseDN`, the DN helpers, `FindUsersByMail`, `GetUser`, `LockState` (computed flag `0x10`, `lockoutTime`, and the domain `lockoutDuration` for `lock_ends_at`), `ClearLockout`, `GroupByName`, `InChainMemberOf`, and an `Under(dn, ou, base)` DN comparison |
| `internal/tools` | the registry, `Deps` and `AccountDeps`, the dispatcher pipeline, identity, fence, kill switch, rate limit, error classification, the refusal table, and one file per tool handler |
| `internal/mcpserver` | wiring `internal/tools` into the MCP Go SDK server (stateless streamable HTTP), the `_meta` receiving middleware, the body cap and bearer middleware, mapping results and refusals to MCP content |
| `internal/mcpclient` | `New(url, token)`, `Requester{SlackID, Email, ChannelID, ThreadTS, TicketID}`, `Call(ctx, Requester, tool, args) (Result, error)`, `ListTools(ctx)`, `ErrIncompleteRequester`; the token is set by an `http.RoundTripper`; each call connects and closes, since the server is stateless |
| `internal/zammad` | gains `UsersByLogin`, `TicketStates`, and the embedded article on `UpdateTicket` |

Each LDAP using call dials, binds as `svc-itsupport`, and closes (no shared connection). The dispatcher sets a `TOOL_CALL_TIMEOUT` deadline on the call's context; LDAP operations use `LDAP_TIMEOUT` each, Zammad its existing `ZAMMAD_TIMEOUT`. With the defaults (15s overall, 5s per LDAP step) a call ends before the agent's 20s `AGENT_TOOL_TIMEOUT`, so the agent never gives up on a call that is still writing.

**API surface** (MCP tools plus the HTTP routes; auth is the bearer token everywhere except `/healthz`)

| Endpoint / tool | Method | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `/healthz` | GET | none | 200 | none | none |
| `/mcp` | POST (streamable HTTP, stateless) | JSON RPC, `_meta` per AC-2 | MCP results | bearer `TOOLSERVER_TOKEN` | 401 bad token; −32602 on bad `_meta` |
| `user_lookup` | tools/call | none | `username`, `display_name`, `department`, `enabled`, `locked`, `lock_ends_at` | bearer + `_meta` | `not_matched`, `ambiguous`, `unavailable` |
| `account_unlock` | tools/call | none | `was_locked`, `locked` | bearer + `_meta` | `read_only`, `rate_limited`, `not_confirmed`, `unavailable` |
| `ticket_create` | tools/call | `title`, `body` | `ticket_id`, `ticket_number` | bearer + `_meta` | `has_ticket`, `rate_limited`, `no_helpdesk_account`, `invalid_arguments`, `unavailable` |
| `ticket_update` | tools/call | `state`, `pending_until`, `note` | `ticket_id`, `state` | bearer + `_meta` | `no_ticket`, `not_yours`, `ticket_closed`, `no_helpdesk_account`, `invalid_arguments`, `unavailable` |
| `privileged_access_change` | tools/call | `summary` | always refused | bearer + `_meta` | `blocked` |
| `mcpclient.Call` | Go | `Requester`, tool, args | `Result{Data, Text, IsError, Code}` | holds the token | `ErrIncompleteRequester` before any request |

Every result carries MCP structured content (the output keys above, plus `code` on refusals and failures) and the one line text from the tables above.

**Value sourcing**

| Action | Value | Source |
|---|---|---|
| every call | requester Slack id, email, channel, thread, ticket id | MCP `_meta`, set by `internal/mcpclient` from agent code's `Requester` (the agent reads the email from Slack `users.info` in #10) |
| every call | `requester_account` | the identity match: `sAMAccountName` of the single enabled user under `TOOL_USER_OUS` whose `mail` matches; null before step 5 |
| every call | `tool_name`, `risk_tier`, Store keys, description | the registry entry |
| every call | `call_id`, `trace_id` | `audit_log` default; the current span context (spec 0003) |
| `tool_call` log line | `trace_id` | the current span context (the `call_id` is not returned by the audit API and is not logged) |
| `tool_call_failed` log line | `cause` | the failure's wrapped error, classified by `cause()` in `internal/tools`: the LDAP result code, the Zammad HTTP status, or a class |
| `user_lookup` | `username`, `display_name`, `department`, `enabled` | AD `sAMAccountName`, `displayName`, `department` (empty when absent), `userAccountControl` bit `0x2` clear |
| `user_lookup` | `locked` | `msDS-User-Account-Control-Computed & 0x10`, read as `svc-itsupport` (fallback in build step 1 if Samba hides it from that account: `lockoutTime > 0` and `lockoutTime + lockoutDuration > now`, using the tool server clock, so clock skew with Samba shifts it) |
| `user_lookup` | `lock_ends_at` | `lockoutTime` (FILETIME) plus the domain object's `lockoutDuration`, as RFC 3339 UTC; null when not locked or when `lockoutDuration` means until an admin unlocks |
| `account_unlock` | `was_locked`, `locked` | `LockState` before and after the write |
| kill switch | state | `KILL_SWITCH_FILE` content at step 7 and before the write; `READ_ONLY` when the path is unset |
| rate limit | count | `CountRecentOK` with `since = now - TOOL_WRITE_WINDOW`; limit `TOOL_WRITE_LIMIT` (account changes) or `TICKET_CREATE_LIMIT` (`ticket_create`) |
| `ticket_create` | existing ticket in this thread | `_meta` ticket id, then `ThreadHasOK("ticket_create", channel, thread)` |
| `ticket_create` | customer id | `UsersByLogin(requester_account)`: exact login, active (LDAP imported, spec 0006) |
| `ticket_create` | group, tag, article type, sender | `ZAMMAD_DEFAULT_GROUP`; the constant tag `noord-assistant`; `note`, `Customer`, not internal, `text/plain` |
| `ticket_create` | title, first article body | the `title` and `body` arguments (written by the model, kept in Zammad only) |
| `ticket_create` | `ticket_id`, `ticket_number` | the Zammad create response |
| `ticket_update` | which ticket | `_meta` ticket id only |
| `ticket_update` | owner check | the ticket's `customer_id` from `GetTicket` against `UsersByLogin(requester_account)` |
| `ticket_update` | closed or merged | `GetTicket`'s `StateID` against `TicketStates` |
| `ticket_update` | Zammad state name, `pending_time` | the `state` argument mapped (`pending` → `pending reminder`); `pending_until` |
| `ticket_update` | note article | `note` argument, prefixed with `Added by the Noord assistant:`, type `note`, sender `Agent`, internal |
| `ticket_update` | pending time cap | `TICKET_MAX_PENDING` |
| group guard | denied DNs | the code floor names plus `TOOL_DENIED_GROUPS`, each resolved by `GroupByName` per call |
| medium tools | approvers, expiry | the registry entry's `Approval` hook (#14 supplies the real one) |

**Key invariants**

- No tool runs without passing the dispatcher; handlers are unexported and reachable only through the registry.
- Only an `AccountDeps` handler can write to the directory, and every `AccountDeps` handler is behind the kill switch and the rate limit.
- No account change runs unless the kill switch said `read_write` at the dispatcher and again just before the write.
- An account tool's target is always `requester_account`; there is no argument that can name another account.
- `ticket_update` only ever touches the ticket in `_meta`, only when its customer is the requester and it is not closed.
- A blocked tool's handler is never called and a blocked call never reaches LDAP or Zammad; a medium tool's handler is never called outside an approved execution (#14).
- Arguments reach `audit_log` only after they validated, and only the allow listed keys.
- Refusal text and audit reasons are fixed strings from the refusal table.
- The tool server binary links no code from `internal/seed`.

**Security model**

- **Two locks on the door:** the bearer token (digest compare in constant time, body cap) and, in the cluster, the NetworkPolicy allowing only the agent pod (#17). `TOOLSERVER_TOKEN` must be at least 32 characters.
- **Identity comes from code.** The model can only fill tool arguments, and no argument carries identity, a target account, or a ticket id. A compromised agent process could still put any email in `_meta`; that is the same trust the approve endpoint already places in the agent (spec 0003), and the NetworkPolicy plus the token limit who can be the agent.
- **Self only for account changes.** `account_unlock` acts on the requester's matched account, and the match requires an enabled user under `TOOL_USER_OUS`.
- **Two fences below the code.** AD already stops `svc-itsupport` writing anything but `lockoutTime` under `OU=Users` and `member` under `OU=Groups` (spec 0004). The tool layer adds the OU fence, the denied group list, and the floor, because AD does not stop `member` writes on `APP-Helpdesk-Agents` or `SYNC-Entra-Users`.
- **Injection surface.** Every directory filter value goes through `ldap.EscapeFilter`; every `_meta` value is checked against an exact format before use; free text arguments go only to Zammad, never into a filter, a log, or the audit log.
- **Slack email trust.** Identity rests on the email Slack reports for the user. #10 must accept only full workspace members (no guests, Slack Connect users, or bots) whose email is confirmed and ends in `@noord.example` (or an overlay address), and record the workspace setting that stops members changing their email.
- **Compliance scope: GDPR.** Tool results carry personal data (display name, department) to the model; the audit log keeps only Slack ids, account names, and allow listed keys (spec 0003). Logs and spans carry none (AC-20). Ticket text lives in Zammad, never in our tables.

**Configuration required** (all tool server only, in `internal/config` with `// feature #9`, and in `.env.example`)

- `LDAP_URL`, `LDAP_BASE_DN`, `LDAP_BIND_DN`, `LDAP_BIND_PASSWORD`, `LDAP_CA_CERT`: now required (closes spec 0004's follow up).
- `ZAMMAD_URL`, `ZAMMAD_TOKEN`, `ZAMMAD_DEFAULT_GROUP`: now required.
- `TOOL_CALL_TIMEOUT` (default `15s`): the whole call, below the agent's `AGENT_TOOL_TIMEOUT`.
- `LDAP_TIMEOUT` (default `5s`): dial plus each LDAP operation.
- `TOOLSERVER_MAX_REQUEST_BYTES` (default `65536`): the `/mcp` body cap.
- `TOOL_USER_OUS` (default `OU=Users,OU=Noord`) and `TOOL_GROUP_OUS` (default `OU=Groups,OU=Noord`): allowed OUs relative to `LDAP_BASE_DN`. Because DNs hold commas, these two use `;` as the separator: `internal/config` splits `[]string` on commas only today (`splitList`), so add a `sep:";"` struct tag that `splitList` honors, with a test.
- `TOOL_DENIED_GROUPS` (default `SG-IT-Admins,APP-Helpdesk-Agents,SYNC-Entra-Users`): denied groups added to the code floor.
- `TOOL_WRITE_LIMIT` (default `3`, at least 1) and `TOOL_WRITE_WINDOW` (default `1h`): the account change rate limit.
- `TICKET_CREATE_LIMIT` (default `5`, at least 1): `ticket_create` calls per requester in `TOOL_WRITE_WINDOW`.
- `TICKET_MAX_PENDING` (default `720h`): how far ahead `pending_until` may be.
- `KILL_SWITCH_FILE` and `READ_ONLY` keep their names; their meaning is now as above.

**Critical test scenarios**

- Happy path, end to end: `internal/mcpclient` → tool server on `httptest` → live Samba (`TEST_SAMBA=1`) + fake Zammad + real Postgres. A seeded locked user's email in `_meta`: `user_lookup` shows `locked: true` and a `lock_ends_at`, `account_unlock` returns `was_locked: true, locked: false`, `ticket_create` then `ticket_update` (note, `closed`) succeed with one Zammad request each for the update, and `ListAuditByThread` shows four intent and outcome pairs with the right tiers, verifies **AC-3**, **AC-6**, **AC-7**, **AC-10**, **AC-11**, **AC-17**.
- Kill switch: file holds `read_only`, then is missing, then is unreadable: `account_unlock` is refused `read_only` each time and AD is untouched; `ticket_create` still works; write `read_write` and the next call unlocks with no restart; a test hook that flips the file between the two reads gets `read_only` and no write, verifies **AC-8**.
- Fence and blocked: `privileged_access_change` with "make me Domain Admin" is refused `blocked` with Samba stopped (no `directory unavailable`); the group guard refuses the floor, the configured groups, a missing group, and a nested group the test creates as Administrator and removes after, and allows `SG-Finance`; an unresolvable configured name refuses everything, verifies **AC-12**, **AC-13**.
- Identity: an email with no match, an email shared by two users (overlay), and an `adm.*` account the test gives a `mail` as Administrator each get `denied_identity` and no write, verifies **AC-4**.
- Injection: an extra argument key (`"username": "ruben.mulder"`) is refused `invalid arguments` and its audit row has null `arguments`; `state: {"x": "free text"}` likewise; a `_meta` email with a filter character (`*)(mail=*`) matches nobody; `ticket_update` on a ticket owned by another customer is refused `not_yours`, and on a closed ticket `ticket_closed`, verifies **AC-2**, **AC-5**, **AC-11**.
- Transport: no token, wrong token, wrong length token → 401 and no audit row; an oversize body is cut off; missing or malformed `_meta` key → −32602 and no audit row; stateless `tools/call` works without `initialize`; `tools/list` has exactly five names with their descriptions (six since spec 0009), verifies **AC-1**, **AC-2**.
- Failure: Samba stopped → `directory unavailable`; fake Zammad timeout on create → `helpdesk unavailable`, not retried (the fake sees one request); a Zammad 403 → `action failed`; a store that fails the intent → `failed` and no handler call; a store that fails the outcome → the real result, verifies **AC-15**, **AC-19**.
- Limits: the fourth `account_unlock` in an hour is refused `rate limited`; the sixth `ticket_create` across threads likewise; a second `ticket_create` in a thread whose first id never reached `_meta` is refused `has_ticket`, verifies **AC-9**, **AC-10**.
- Registry: a medium test tool creates a pending approval with `isError` false and its handler counter stays 0; a medium tool with no hook fails registry construction; the production registry lists no test tool, verifies **AC-14**, **AC-21**.
- Config: an OU entry with `DC=`, an empty entry, a short token, or an `LDAP_URL` that is not `ldaps://` stops startup, verifies **AC-16**.
- Hygiene: a captured log handler and span recorder over the happy path hold no email, account name, Slack id, or argument value; on an LDAP, directory, and Zammad failure the one `tool_call_failed` line carries the expected `cause` and none of those values, nor the error's text, verifies **AC-20**.

## Build plan

Tracer Bullet inside the feature: first prove one real call goes agent client → tool server → AD → audit row, then thicken one tool and one guard at a time. Each step ends green on `make check`.

1. **`internal/ldap` and the Samba facts.** Move `Dialer`, `BaseDN`, the DN helpers, and `LockState` from `internal/seed/directory` into `internal/ldap`, and have the seeder import them (seed tests stay green). Add `FindUsersByMail`, `GetUser`, `ClearLockout`, `GroupByName`, `InChainMemberOf`, and `Under`. Live tests, bound as `svc-itsupport`, settle two facts the design leans on: it can read `msDS-User-Account-Control-Computed`, and Samba honors the chain matching rule. If either fails, build the fallback named above and record it in `rationale.md`. `internal/ldap` is already on both deny lists, satisfies **AC-4**, **AC-7**, **AC-13**, **AC-18**.
2. **Tracer thread: `user_lookup` through the whole pipe.** Add `github.com/modelcontextprotocol/go-sdk`; `internal/mcpserver` with stateless streamable HTTP at `/mcp`, the body cap, the bearer middleware, and the `_meta` receiving middleware (first confirm two SDK behaviors: a stateless `tools/call` needs no `initialize`, and middleware can return −32602); `internal/tools` with the registry and typed deps, dispatcher steps 2 to 6 and 10, error classification, the refusal table, and `user_lookup`; `internal/mcpclient`; wire `cmd/toolserver` (`NewRecorder`, registry, mux). Config: flip `LDAP_*` and `ZAMMAD_*` to required; add `TOOL_CALL_TIMEOUT`, `LDAP_TIMEOUT`, `TOOLSERVER_MAX_REQUEST_BYTES`, `TOOL_USER_OUS` (with the `sep` tag), the OU checks, and the token length check; update `.env.example` and both `main_test.go` helpers. One test drives `user_lookup` from `mcpclient` to live Samba and reads the audit pair, satisfies **AC-1** to **AC-6**, **AC-15** to **AC-17**, **AC-19** to **AC-21**.
3. **`account_unlock` with the kill switch and the rate limit.** `AccountDeps`, steps 7 and 8, `CountRecentOKOutcomes` plus `make gen` and `Recorder.CountRecentOK`, the handler with the before and after lock read and the second kill switch read, `TOOL_WRITE_LIMIT`, `TOOL_WRITE_WINDOW`, and the kill switch tests, satisfies **AC-7**, **AC-8**, **AC-9**, **AC-21**.
4. **Ticket tools.** `zammad.UsersByLogin`, `TicketStates`, and the embedded article on `UpdateTicket` (guard test updated); `ThreadHasOKOutcome` query plus `Recorder.ThreadHasOK`; `ticket_create` and `ticket_update` with their `Check` rules, `_meta` ticket rules, and closed state refusal; `TICKET_CREATE_LIMIT`, `TICKET_MAX_PENDING`; tests on the fake Zammad, then one live run after `make helpdesk-reset` that measures the search lag and answers spec 0006's open question (can the assistant close tickets and set tags on create), satisfies **AC-9**, **AC-10**, **AC-11**, **AC-19**.
5. **The rest of the fence.** `privileged_access_change` and step 3; the group guard with the code floor, `TOOL_GROUP_OUS`, `TOOL_DENIED_GROUPS`, unresolved name handling, and its live tests; medium routing (step 9, the `Approval` hook, the registry construction check, `NewTestRegistry`) with a test only medium tool, satisfies **AC-12**, **AC-13**, **AC-14**, **AC-21**.
6. **Boundaries and docs.** Add `internal/tools` and `internal/mcpserver` to both deny lists; a test that `cmd/toolserver` does not depend on `internal/seed`; the outage and audit failure tests; the hygiene test; nested `AGENTS.md` facts for `internal/tools` and `internal/ldap` (`/sync` reconciles the rest); spec 0001's Follow-up for #9 ticked, satisfies **AC-15**, **AC-18**, **AC-19**, **AC-20**.

## Consequences

**Positive**:
- The safety claim is structural: a model can't name another account, another ticket, or a privileged group, because no argument carries one, and every refusal is in the audit log for the eval and the video.
- The kill switch pauses account changes without silencing the helpdesk, so IT keeps every request while the agent stops acting.
- Adding a tool is one registry entry plus a typed handler; the dispatcher gives it identity, validation, audit, and the fence, and the handler's type decides whether the kill switch applies. That's the "add a tool" page of the runbook.
- `internal/ldap` gives the seeder and the tool server one tested LDAPS dialer instead of two.

**Negative / tradeoffs**:
- **Self only unlock is less helpful.** A manager asking for a colleague gets a ticket and an escalation, not an unlock. The eval's expected outcomes must say so.
- **Identity rests on Slack's email.** Anyone who can change their Slack email to a colleague's, or a compromised agent process, can act as that colleague. Mitigated by Slack's email verification, the member and domain checks (#10), and the network lock, not removed.
- **AD down means no tickets either.** Ticket tools need the identity match, so a directory outage stops ticket creation too; the agent tells the user and escalates in Slack (#11).
- **Zammad search lag can deny a new starter** a ticket (`no_helpdesk_account`) until the index catches up; the agent escalates instead.
- **Every call costs an LDAP bind and search,** and ticket calls add a Zammad user search. Fine at Noord's volume; it would need a short lived cache at real scale.
- **The rate limits are soft.** Concurrent calls can all read the same count and pass, and an unlock whose outcome row failed (`ErrAuditIncomplete`) is not counted. Only `ok` outcomes count, so a burst of refused attempts is not limited (the agent loop bounds still apply). The same is true of the per thread ticket check under parallel tool calls; #10 runs one turn's tool calls one after another.
- **Users are fenced by OU only.** A future tool that acts on someone other than the requester must add its own group membership check; the registry review is the place to catch it.
- **The LDAP modify itself is unguarded by the kill switch;** a flip during that one round trip lands on the next call.
- **Unknown tool names are not audited,** because the SDK answers them before the dispatcher.
- **Calls refused as blocked or invalid have no `requester_account`,** because those checks run before identity; the Slack id still names the requester.

**Neutral**:
- Adds `github.com/modelcontextprotocol/go-sdk` (named in spec 0001) and moves code between packages; `make tidy-check` and the boundary test cover both.
- The scope's done when line for #9 changes from "flipping the kill switch blocks all writes" to "blocks every account change", by your choice.
- `TOOL_USER_OUS` and `TOOL_GROUP_OUS` need a `;` separator in `internal/config`.
- Schema string bounds are code constants, not settings, because they are part of the contract the model sees.

## Follow-up

- [ ] #10: the agent fills `Requester.Email` from Slack `users.info`, accepting only full members (not guests, Slack Connect users, or bots) with a confirmed email ending in `@noord.example` or an overlay address; stores the `ticket_create` result's `ticket_id` with `SetConversationStatus` and sends it in later `_meta`; runs one turn's tool calls one after another; uses `AGENT_TOOL_TIMEOUT` above `TOOL_CALL_TIMEOUT`. Record the workspace setting that stops members changing their email in `docs/runbooks/slack-workspace.md`.
- [x] #11: escalation (`escalate` tool and the Slack post) for `blocked`, `not_matched`, `no_helpdesk_account`, and outage refusals; whether more blocked tools are needed. Done in [spec 0009](../0009-refusal-escalation/index.md): `escalate` and the IT channel post, with no new blocked tools.
- [ ] #14: the real medium tool, its `Approval` hook (approvers from `SLACK_IT_APPROVERS`, which today is an agent setting; decide its home in the tool server), the approve endpoint outside `tools/list`, and expiry.
- [ ] #15: group tools call `Fence.CheckGroup`; the grantable list (no distribution lists); a target other than the requester needs its own membership check.
- [ ] #17: the Helm chart mounts the kill switch ConfigMap and sets `KILL_SWITCH_FILE`, starting at `read_only`; the NetworkPolicy for `/mcp`; the runbook's "switch to read only" page is one `kubectl` command.
- [ ] #19: eval tickets for each refusal code, including a colleague unlock request (expected: escalate), an injected extra argument, and a closed ticket.
- [x] #12: confirm the hygiene test from AC-20 stays as the redaction rules land. It stays (`TestToolCallLog`). [Spec 0010](../0010-pii-redaction-retention/index.md) adds the allow listed log handler, the source scan, the redacting span exporter, and a tool server canary over the unlock and refusal flows (`internal/mcpserver`).
- [x] Scope #9 done when wording: "flipping the kill switch blocks every account change".
