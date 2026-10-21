# 0012. MFA re-registration with approval: a medium tool behind an IT card in Slack, an agent owned settle loop, and a clear that runs only after a human click

**Date**: 2026-10-20
**Status**: Accepted

## Summary

When a Noord employee tells the assistant they lost their phone or can't get their sign in codes, the assistant opens a ticket and asks the tool server to clear their MFA methods (the phone and Authenticator app sign in steps). Nothing is cleared yet: the tool server records a pending approval, and agent code posts an Approve or Deny card to the IT channel in Slack. Only an IT approver who is not the requester can approve, within 30 minutes. On approve the tool server clears the methods, closes the ticket, and the user is told to register again. A deny, a timeout, or a failure leaves MFA as it was (or tells IT exactly what happened) and escalates the ticket. Every step leaves an audit row that names the approver.

## Rationale

Reasoning and the options weighed: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As a Noord employee with a new phone, I want to tell the assistant my Authenticator is gone and be told when I can register again, so that I am not stuck waiting for Ruben to notice my ticket.
- As Noord's IT person, I want every MFA clear to wait for my click in Slack, with a reminder to check who is asking, so that nobody can talk the assistant into handing over an account.
- As Noord's IT person, I want a request nobody approved in time, a request I denied, and a clear that went wrong to land in my queue as an escalated ticket, so that nothing silently disappears.
- As the engineer, I want the approval, the clear, and the expiry to each leave an audit pair naming the approver, and the model to have no way to approve or skip the card, so that the safety claim holds whatever the prompt says.

**Acceptance criteria**:
- **AC-1**: The system prompt gains an MFA section. When the user says they lost or replaced their phone, their Authenticator app is gone or broken, or they can't get or approve sign in codes, the model calls `ticket_create` (a short title, a body describing the problem), then `mfa_clear`. On a `pending_approval` result it tells the user that IT must approve clearing their sign in methods, that they will get a message in this thread when IT decides (within `expires_in_minutes` minutes), and that nothing has changed yet; it never says the methods are cleared. On `already_pending` it says a request is already waiting in another conversation. On `nothing_to_clear` it tells them to add the Authenticator app at their next sign in, naming the KB article "Set up the Authenticator app". On `not_in_entra` or `other_methods` it calls `escalate` with the new model reason `manual_action`; on `protected` with `privileged_request`; on `read_only` or `unavailable` it says so and leaves the ticket open. When `mfa_clear` is not in its tools, it calls `escalate` with `manual_action` instead.
  - *Amended 2026-10-20 (after the live runs):* on `pending_approval` the model also never describes what happens after an approval as if it were certain (for example "after that you can register your new phone"); it says only that IT decides and that a message will come in this thread. The live run found the model adding that line; the prompt now forbids it.
- **AC-2**: `mfa_clear` is a tier `medium` tool with no arguments (an empty object; anything else is `invalid_arguments`), registered only when the `GRAPH_*` settings are complete and `SLACK_IT_APPROVERS` is not empty (AC-21); otherwise `tools/list` omits it. Its handler takes a new `MFADeps` (the Graph client, the directory reader, the helpdesk, the history, the approvals reads, the MFA fence; no directory writer), a handler type the dispatcher treats as an account change, so step 7 (kill switch) and step 8 (`TOOL_WRITE_LIMIT`) run before step 9. It acts only on the matched requester's account.
- **AC-3**: At step 9, before the `Approval` hook and `Defer`, a new medium tool `Precheck` runs and refuses in this order, each with an audit pair (`Recorder.Refuse`) and no `approvals` row: no ticket id in `_meta` → `no_ticket`; the thread holds an `ok` `escalate` → `already_escalated`; the thread holds a `denied_policy` `privileged_access_change` → `escalate_first`; the requester has no Zammad user → `no_helpdesk_account`; the ticket's customer is not that user → `not_yours`; the ticket is closed or merged → `ticket_closed`; the account is a member, at any depth, of a code floor group or an `MFA_DENIED_GROUPS` group (a configured name that does not resolve refuses every account, logging `denied_group_unresolved`) → `protected`; a `pending` unexpired `mfa_clear` approval exists for the account in another thread → `already_pending` (new); one exists in this thread → the reuse of AC-5, with no Graph call; `UserByUsername` returns `ErrNotFound` or `ErrNotManaged` → `not_in_entra` (new); a Graph error → `unavailable`; `AuthMethods` lists any MFA method other than phone and Microsoft Authenticator (anything but `password`, for example `softwareOath`, `fido2`, `email`, `windowsHelloForBusiness`, `temporaryAccessPass`) → `other_methods` (new), since clearing only some of them would not let the user register again; the user has no Microsoft Authenticator method and no phone method → `nothing_to_clear` (new). Every audit read comes before any Zammad or Graph call.
- **AC-4**: When the precheck passes and no pending approval exists for the account in this thread, the `Approval` hook returns kind `it`, approvers = `SLACK_IT_APPROVERS` (snapshot), expiry = now + `APPROVAL_TIMEOUT`, and `Recorder.Defer` writes the intent, the `approvals` row (`arguments` `{}`), and the `pending_approval` outcome in one transaction; `Defer` gains a result argument stored through the tool's new `PendingResult` allow list (`expires_at`, `method_count`, `reused`), separate from `StoreResult`, which covers the execution. After the commit the tool server adds one internal note to the ticket: `MFA reset requested. Waiting for IT approval until <expires_at as YYYY-MM-DD HH:MM UTC>. Methods: <method summary>.` A failed note does not fail the call (`ticket_noted` false). The result has `isError` false, code `pending_approval`, the text `This request is waiting for IT approval.`, and `approval_id`, `expires_at`, `expires_in_minutes`, `methods` (the method summary: type and count only, for example `Authenticator app (1), phone (2)`; no device name, no number), `method_count`, `ticket_id`, `ticket_url` (when `ZAMMAD_PUBLIC_URL` is set), `reused` false, `ticket_noted`. The handler never runs.
- **AC-5**: A second `mfa_clear` in the same thread while its approval is `pending` and unexpired makes no Graph call and writes, through a new `Recorder.Reuse`, an audit pair (outcome `pending_approval`, `approval_id` the existing one, PendingResult `expires_at`, `reused` true) and no new `approvals` row or ticket note. It returns the AC-4 result for the existing approval without `methods` or `method_count`, with `reused` true.
- **AC-6**: Agent code, in the same transaction that stores any `mfa_clear` result with code `pending_approval` (reused or not), inserts its `approval_cards` row (`ON CONFLICT DO NOTHING`); the conversation stays `open`. When the insert created the row, after that commit it records the attempt (`post_attempts` + 1, `last_post_at` = now), posts the card (AC-7) to `SLACK_IT_CHANNEL` as a new message (not a thread reply), and stores its `card_ts`. A failed post logs `approval_card_post_failed` (`approval_id`, `cause`) and leaves `card_ts` null for the tick (AC-14). The model's turn continues as usual. An approval whose result never reached the agent is adopted by the tick (AC-14).
- **AC-7**: The card is Block Kit built only by code, with no model text. A section with the fixed lines `MFA reset request`, `Requester: <@slack id> (<account>)` (from the conversation row: `requester_slack_id`, `directory_account`; on a repost or update, from the approval view), `Ticket: <link>` (`ticket_url` labelled `open in helpdesk`, else `id <ticket_id>`), `Methods: <method summary>` (on a card the tick posts: `Methods: listed in the ticket note.`), `Expires: <HH:MM UTC>`, and `Confirm with the person by phone or in person before approving.`; then an actions block (`block_id` `approval:<id>`) with `Approve` (style primary, a Slack confirm dialog `Clear this person's MFA methods?`) and `Deny` (style danger), `action_id` `approval_approve` and `approval_deny`, `value` the approval id. Every interpolated value is escaped with `slackEscape`. The message's fallback `text` is `MFA reset request for <@slack id>`.
- **AC-8**: The Slack listener handles Socket Mode `interactive` events of type `block_actions` whose action id is `approval_approve` or `approval_deny`: it acks at once, parses the value as a UUID (anything else logs `slack_interaction_ignored` and does nothing), and hands `Agent.Click` the approval id, the clicker's Slack id, the decision, and the channel, without blocking. Every other interaction is acked and ignored. Logs carry `event_id` (the trigger id is not logged), `channel_id`, and `kind` only. The app manifest already has interactivity on (spec 0006 AC-11); no Slack app change is needed.
- **AC-9**: For a click, the agent looks up the `approval_cards` row (none: the ephemeral `approval_unknown` text to the clicker), then calls `POST /approvals/{id}/decision` with `{clicker, approve}` under `APPROVAL_DECIDE_TIMEOUT`, through a new `mcpclient.ApprovalClient` whose URLs are `TOOLSERVER_URL`'s scheme and host plus the fixed `/approvals/...` paths, carrying the bearer token and the W3C trace context like `Client.Call`. A refused move gets the clicker an ephemeral fixed text by reason (`approval_not_allowed`, `approval_self`, `approval_expired`, `approval_decided`) and changes nothing else. A timeout gets `approval_processing` (the decision may have been recorded; the tick settles it); a transport error or a 5xx gets `approval_unavailable` and leaves the card for the tick. A move that happened is settled (AC-15) on the conversation's worker.
- **AC-10**: `POST /approvals/{id}/decision` runs `Recorder.Decide` (spec 0003: the guarded move plus its `approval.approve` or `approval.deny` audit pair with `approver_slack_id` = the clicker). An unknown id gets 404 and writes nothing; a body that is not exactly `{clicker, approve}` with a Slack user id matching `_meta`'s rule (`^[UW][A-Z0-9]{2,20}$`, spec 0007 AC-2) gets 400 and writes nothing. When the move was an approve, AC-11 runs on `context.WithoutCancel(r.Context())` with its own `APPROVAL_EXECUTE_TIMEOUT`, so a client that goes away never stops a clear halfway; the response is written after it if the client is still there. The response is 200 with `moved`, `reason` (the AC-9 token when refused), and the approval view (AC-13).
  - *Amended by [spec 0013](../0013-access-requests-group-owner/index.md) AC-13 (2026-10-21):* the body may also hold `clicker_email` (required for a `group_owner` approval, ignored for `it`); the route reads the approval first and resolves the email to an AD account for a group owner.
- **AC-11**: An approved approval executes once, through the registry entry of its `tool_name`, inside `Recorder.Run` with a **new** `call_id`, the approval's `approval_id`, `approver_slack_id` = `decided_by`, its requester and thread columns, and its `arguments`, under `APPROVAL_EXECUTE_TIMEOUT`. For `mfa_clear` the handler, in order: reads the kill switch (paused: outcome `read_only`, reason `account changes are paused`); checks the requester account again with the reader's `FindUserByAccount` (found, under `TOOL_USER_OUS`, enabled, not protected: else `denied_policy`); resolves the Entra user (`not in entra` when not found or not managed); lists every method (`AuthMethods`; an MFA method other than phone or Authenticator: `denied_policy`, `other methods`); reads the kill switch again (spec 0007 AC-8: immediately before the write); then deletes the Microsoft Authenticator and phone methods, non default methods first and the user's default sign in method last (the default read from `signInPreferences` when the app can read it; else the fixed order `alternateMobile`, `office`, `mobile`, then Microsoft Authenticator last, because Graph refuses to delete a user's default method while others remain and a registered Authenticator is the default; a phone of any other type goes before these; amended 2026-10-20 from the build step 1 probe, journal 0041, which found `signInPreferences` answers 400 to the tool server app). When the methods listed at the start leave nothing to delete, it ends `denied_policy`, reason `nothing to clear`, and never reports success, stopping at the first failure (outcome `error`, reason `method delete failed`). On success it adds the note `MFA methods cleared after IT approval by <decided_by>.` and closes the ticket unless it is closed or merged. StoreResult: `cleared_authenticator`, `cleared_phone`, `ticket_closed`. Then `MarkApprovalExecuted` (outcome `ok`) or `MarkApprovalFailed` (any other outcome); when that mark finds no `approved` row (abandoned meanwhile, AC-12), it logs `approval_mark_lost` (`approval_id`) and the abandon's "check by hand" escalation stands. No method is touched before both kill switch reads and the recheck pass, and nothing is ever retried, with one exception (amended 2026-10-20 after review): with two or more Authenticator registrations the default one cannot be read, so an Authenticator delete Graph refuses with 400 while another Authenticator is still to come is moved to the end once, and the clear stops if it is refused again.
  - *Amended by [spec 0013](../0013-access-requests-group-owner/index.md) AC-4 (2026-10-21):* `Execute` dispatches on the handler family and takes its timeout from the dispatcher `Config`, so it works without the `GRAPH_*` settings.
- **AC-12**: `POST /approvals/expire` runs `Recorder.Expire` (up to 100 rows, an `approval.expire` pair with outcome `expired` each) and then a new `Recorder.Abandon` that moves `approved` rows whose `decided_at` is older than `APPROVAL_EXECUTE_GRACE` to `failed` (guarded UPDATE, `arguments` cleared), each with an `approval.abandon` audit pair (approver `decided_by`, target `requester_account`), outcome `error`, reason `execution not confirmed`. It never runs a stored call. The response is `{expired, abandoned}` counts.
- **AC-13**: `GET /approvals/{id}` returns the approval view: `id`, `tool_name`, `status`, `decided_by`, `decided_at`, `expires_at`, `created_at`, `requester_slack_id`, `requester_account`, `slack_channel_id`, `slack_thread_ts`, `ticket_id`, `ticket_url`, and, once `executed` or `failed`, `execution`: `outcome`, `reason` (the outcome row's stored `error`, for example `method delete failed`, `account changes are paused`, `execution not confirmed`), and the StoreResult counts when there are any. `execution` is read by `ExecutionOutcomeForApproval`: the newest outcome row with this `approval_id` whose `tool_name` is the approval's tool or `approval.abandon`, excluding `pending_approval` outcomes. `GET /approvals?since=<RFC 3339 time>` returns the views of approvals created at or after that time (oldest first, up to 100), without `execution` (amended 2026-10-20 after review: one read per row; the tick needs only ids and threads). Neither makes a Zammad or Graph call. Unknown id: 404. All four routes are on the MCP listener's mux, behind the same bearer check, body cap (`TOOLSERVER_MAX_REQUEST_BYTES`), and W3C trace context extract as `/mcp` (those helpers become exported from `internal/mcpserver`), and none is in `tools/list`.
  - *Amended by [spec 0013](../0013-access-requests-group-owner/index.md) AC-10 and AC-11 (2026-10-21):* every view carries `approver_kind` and `target`; a single view also carries `cancel_reason` and, for `group_owner`, the owner's account, name, and mail (read live), `nested_into`, and `reason`. A new route, `POST /approvals/{id}/cancel`, ends a pending group owner approval whose owner cannot be reached.
- **AC-14**: The agent runs an approval tick at start and every `APPROVAL_POLL_INTERVAL` while Slack is configured: (1) `POST /approvals/expire`; (2) adopt: `GET /approvals?since=` now minus (`APPROVAL_TIMEOUT` + 1 hour), and for each approval with no `approval_cards` row (one read finds which already have one, amended 2026-10-20) whose thread is a known conversation, insert a row (`ON CONFLICT DO NOTHING`); (3) for each row with `settled_at` null (oldest first, up to 100), `GET /approvals/{id}`. A `pending` row with `card_ts` null is posted (AC-7, tick form) only when `post_attempts` is below 3 and `last_post_at` is null or older than twice `APPROVAL_POLL_INTERVAL`, recording the attempt before the post. A row whose status is `executed`, `failed`, `denied`, or `expired` goes to the conversation's worker to settle (AC-15). Any failure logs `approval_tick_failed` (`cause`) and is tried again on the next tick; the tick never fails the binary.
  - *Amended 2026-10-20 (cross check):* the adopt step considers only approvals whose `tool_name` is `mfa_clear` (the one tool the agent builds a card for; #15 adds its own), inserting each with one `AdoptApprovalCard` (`ON CONFLICT DO NOTHING`, only into a known conversation). The route returns the oldest 100 in the window, so with more than 100 approvals created within it the newest are not adopted until older ones age out; a known bound (see Consequences).
  - *Amended by [spec 0013](../0013-access-requests-group-owner/index.md) AC-17 (2026-10-21):* the tick adopts `mfa_clear` and `group_access_request`, over the longer of `APPROVAL_TIMEOUT` and `ACCESS_APPROVAL_TIMEOUT` plus an hour; a group owner card posts to the owner's DM, never the IT channel.
- **AC-15**: Settling runs on the conversation's per thread worker, so it never races a model turn, and handles each outcome once. For `denied`, `expired`, and `failed`, it first calls `escalate` by code (initiator `code`) with the new code only reasons `approval_denied`, `approval_expired`, or `approval_failed`, the fixed title `MFA reset needs IT`, and a fixed summary, rebuilding the requester the way the sweep does (`reidentify`). When `reidentify` fails, it settles nothing and tries again on later ticks; once the approval's `expires_at` is more than `APPROVAL_TIMEOUT` in the past, it posts `itNotMatched`, settles, and sends the user `mfa_ended_contact_it` instead of the AC-17 text. Then, in one transaction, a guarded UPDATE sets the card's `settled_at` and `outcome` (`WHERE settled_at IS NULL`; no rows means another settle won, and nothing more happens) and the conversation, when still `open`, moves to `resolved` (for `executed`) or `escalated` (otherwise). Only after that commit does it update the card (AC-16), post the user's fixed text in their thread (AC-17), and, for an escalation, the IT channel post as spec 0009 builds it. Escalation is at least once; the Slack posts are at most once.
  - *Amended 2026-10-20 (cross check):* the settle limit is per approval and comes from the approval view, not from a setting: a requester who cannot be rebuilt is given up on once now is past `expires_at` plus (`expires_at` minus `created_at`), that approval's own timeout. A later change to `APPROVAL_TIMEOUT` on either binary never moves it.
  - *Amended 2026-10-20 (after the live runs):* a `denied` approval whose `decided_by` is its requester is a **cancel** (AC-18 lets a requester who is also an approver deny their own request). It escalates by code like a deny, but with the new code only reason `approval_cancelled` and the summary `The requester cancelled the MFA reset request.`; the card and the user's text are the cancel forms of AC-16 and AC-17. The approval, the card row's `outcome`, and the conversation are the same as for a deny (`denied`, `denied`, `escalated`).
  - *Amended by [spec 0013](../0013-access-requests-group-owner/index.md) AC-15 (2026-10-21):* the settle's texts, escalation titles, card lines, and cancel test are a table keyed by `tool_name`.
- **AC-16**: `chat.update` replaces the card with the AC-7 fact lines (no methods line, no buttons) plus one outcome line: `Approved by <@decided_by>. Cleared <n> Authenticator and <m> phone methods. Ticket closed.` (or `Ticket left open.` when `ticket_closed` is false); `Denied by <@decided_by>. Escalated.`; `Expired with no decision. Escalated.`; `Approved by <@decided_by>, but the clear did not finish (<reason>). Escalated, check the account by hand.` A failed update logs `approval_card_update_failed` (`approval_id`, `cause`) and does not undo the settle.
  - *Amended 2026-10-20 (after the live runs):* a cancel's outcome line is `Cancelled by the requester <@decided_by>. Escalated.`
- **AC-17**: The user's thread gets exactly one fixed text per settled approval: `mfa_cleared` (or `mfa_cleared_ticket_open` when `ticket_closed` is false, amended 2026-10-20), `mfa_denied`, `mfa_expired`, `mfa_failed`, or (AC-15) `mfa_ended_contact_it` (texts below). None names the approver. No model call runs for a settle.
  - *Amended 2026-10-20 (after the live runs):* a cancel gets `mfa_cancelled` instead of `mfa_denied`.
- **AC-18**: Nobody can approve their own request, and only `SLACK_IT_APPROVERS` (snapshot at request time) can decide: the requester's Approve, a non approver's click, a click after `expires_at`, and a second click are each refused by the guarded UPDATE, audited `denied_policy` with the spec 0003 reason, and change no method. A requester who is also an approver may Deny their own request (spec 0003's deny guard has no self check); that is a cancel, it settles as `denied`, and it changes no method. When approve and deny race, exactly one wins. The model's tools hold nothing that decides, expires, or reads an approval.
  - *Amended by [spec 0013](../0013-access-requests-group-owner/index.md) AC-8 (2026-10-21):* for kind `group_owner` the decider is the clicker's AD account, and the self check compares it with `requester_account`; `approver_slack_id` in audit pairs comes from `decider_slack_id`.
- **AC-24**: While the thread has an `mfa_clear` approval that is `pending` or `approved`, `ticket_update` with state `closed` is refused with the new code `approval_pending` (`denied_policy`, `approval pending`, sentence `This ticket is waiting on an IT approval and closes by itself when IT decides.`), read from `approvals` by thread before any Zammad call, like the spec 0009 close guard.
  - *Amended by [spec 0013](../0013-access-requests-group-owner/index.md) AC-18 (2026-10-21):* the guard covers every medium tool, with a sentence per tool.
- **AC-19**: With account changes paused, `mfa_clear` is refused at step 7 (`read_only`, no card), and an approval approved while paused fails at AC-11's first read with no Graph write, then settles as `failed` and escalates.
- **AC-20**: The tool server's Entra app gains `UserAuthMethod-MicrosoftAuthApp.ReadWrite.All` next to `UserAuthMethod-Phone.ReadWrite.All` (exact name confirmed live; if no granular permission works, `UserAuthenticationMethod.ReadWrite.All` replaces both, with a journal entry and an amendment to spec 0005 AC-1 and AC-11). `internal/graph` gains `AuthenticatorMethods` and `DeleteAuthenticatorMethod`, both taking a `ManagedUser`, and the fake Graph models them. A live probe shows the app can list and delete a demo user's Microsoft Authenticator method and still gets 403 on `POST .../temporaryAccessPassMethods`, and records whether the app can read `signInPreferences` and in which order Graph lets it delete a user's default and other methods (AC-11's order is amended to the result); results go in the journal and `docs/runbooks/entra-tenant.md`. Result (2026-10-20, journal 0041): list and delete work, the Temporary Access Pass call is 403, `signInPreferences` answers 400, and Graph refuses to delete the default method (the Authenticator) while a phone remains, so AC-11's fixed order puts the Authenticator last.
- **AC-21**: `SLACK_IT_APPROVERS` moves from the agent's settings to the tool server's (each entry `^[UW][A-Z0-9]{8,11}$`). New tool server settings: `APPROVAL_TIMEOUT` (default `30m`, 5m to 24h), `APPROVAL_EXECUTE_TIMEOUT` (default `60s`, 15s to 5m), `APPROVAL_EXECUTE_GRACE` (default `5m`, at least twice `APPROVAL_EXECUTE_TIMEOUT`), `MFA_DENIED_GROUPS` (default `SG-IT-Admins,APP-Helpdesk-Agents`). New agent settings: `APPROVAL_POLL_INTERVAL` (default `30s`, 10s to 5m), `APPROVAL_DECIDE_TIMEOUT` (default `90s`, 30s to 10m). Bad values fail at load naming the setting. `.env.example` matches `internal/config`.
  - *Amended 2026-10-20 (found by `/develop`, journal 0043):* `APPROVAL_TIMEOUT` is read by both binaries, not the tool server alone. The tool server sets each `expires_at` from it; the agent's tick needs it for the AC-14 adopt window, which no other agent setting gives. It stays one setting, declared once (`config.ApprovalWindow`, embedded in the tool server's `Approvals` and in `Agent`), and both loaders refuse a value outside 5m to 24h naming `APPROVAL_TIMEOUT`. Set it to the same value on both; nothing checks that at startup. An agent value shorter than the tool server's can leave an approval whose result never reached the agent unadopted, so it gets no card and expires unannounced (Consequences); it never changes an expiry or the settle limit.
- **AC-22**: No log line or span attribute holds an account name, a phone number (masked or not), a Slack user id, or a method id; `approval_id` joins the `internal/logging` allow list. No audit row holds a phone number. No Authenticator device name leaves the tool server at all (results, notes, and the card carry method types and counts only). The canary test (spec 0010) runs request, card, click, execution, and settle with a marked account, phone, and device name and checks the logs, spans, audit rows, stored messages, and model requests.
- **AC-23**: The happy path, live against `make dev` with real Slack, Samba, Zammad, Entra, and the Anthropic API: a demo user DMs "I got a new phone and my Authenticator is gone". The calls are `user_lookup` (code), `ticket_create`, `mfa_clear`; the IT channel gets the card; the user is told to wait. A second Slack account in `SLACK_IT_APPROVERS` clicks Approve. Then `make entra-mfa NAME=<user>` lists no Authenticator or phone method, the ticket is closed with both notes, the card shows the outcome, the user has `mfa_cleared`, the conversation is `resolved`, and `audit_log` holds the `mfa_clear` pending pair, the `approval.approve` pair, and the execution pair, the last two with `approver_slack_id` set. A second live run with Deny leaves the methods unchanged, the ticket escalated, and `mfa_denied` in the thread.

## Decision

**Chosen option**: Option 1: the agent posts the card and runs the clock; the tool server decides and executes behind plain JSON routes on the MCP listener.

The model can only ask (`mfa_clear`); the card, the click, the decision, the execution, the expiry, and every message about the outcome are code. The approval rules stay where spec 0003 put them, in guarded SQL. The approve routes trust the agent's bearer token for who clicked, the same trust `_meta` identity already carries; a tool server owned Slack card is the named hardening step (Follow-up).

**Implementation skills**: `golang-security` · `golang-database` · `golang-error-handling` · `golang-concurrency` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `claude-api` (built in: the prompt section) · `testcontainers-go` (`testcontainers/claude-skills`, `.agents/skills/testcontainers-go/`, the database tests). Open each only when building in its area.

## Feature design

**The request, end to end**

| Step | Who | What happens |
|---|---|---|
| 1 | Agent code | Slack gate and `user_lookup` as spec 0008 |
| 2 | Model | `ticket_create`, then `mfa_clear` (AC-1) |
| 3 | Tool server | dispatcher steps 2 to 8, then the precheck (AC-3), then `Defer` and the ticket note (AC-4), or the reuse (AC-5) |
| 4 | Agent code | stores the result and the `approval_cards` row in one transaction, posts the card (AC-6, AC-7) |
| 5 | Model | tells the user to wait (AC-1); the conversation stays `open` |
| 6 | IT approver | clicks Approve or Deny; the listener acks and hands it on (AC-8) |
| 7 | Agent code | `POST /approvals/{id}/decision` (AC-9) |
| 8 | Tool server | `Decide`; on approve, the audited clear and ticket close (AC-10, AC-11) |
| 9 | Agent code | settle on the thread's worker: escalate if needed, settle row and conversation, update the card, tell the user (AC-15 to AC-17) |
| tick | Agent code | every 30 s: expire, abandon stuck, adopt approvals with no card, read unsettled cards, post or settle (AC-12, AC-14) |

**Data model sketch** (migration `00004_approval_cards.sql`; confirmed in the design conversation)

`approval_cards`, written only by the agent:

| Column | Type | Null | Notes |
|---|---|---|---|
| `approval_id` | uuid | PK | from the pending result; no FK to `approvals` (the agent role cannot read it, and spec 0003 avoids cross role FKs) |
| `conversation_id` | bigint | not null | FK → `conversations.id` |
| `tool_name` | text | not null | `mfa_clear` (and #15's tool later) |
| `slack_channel_id` | text | not null | where the card went |
| `card_ts` | text | null | the card's message ts; null until a post succeeds |
| `post_attempts` | int | not null | default 0; `CHECK (post_attempts >= 0)`; at most 3 posts per card (AC-14) |
| `last_post_at` | timestamptz | null | when the last post attempt started; spaces out reposts so a slow post is never doubled |
| `expires_at` | timestamptz | not null | from the result |
| `created_at` | timestamptz | not null | default `now()` |
| `settled_at` | timestamptz | null | null means the outcome is still owed |
| `outcome` | text | null | `CHECK (outcome IN ('executed', 'failed', 'denied', 'expired'))`; `CHECK ((outcome IS NULL) = (settled_at IS NULL))` |

Index: `approval_cards (created_at) WHERE settled_at IS NULL`. Grants (same migration, plus rows in `internal/db/grants_test.go`): `noord_agent` SELECT, INSERT, and UPDATE (`card_ts`, `post_attempts`, `last_post_at`, `settled_at`, `outcome`); nothing for `noord_toolserver` or `noord_eval`. Queries (`db/queries/approval_cards.sql`): `InsertApprovalCard` (`:execrows`, `ON CONFLICT DO NOTHING`; 1 means this caller posts), `RecordCardPostAttempt` (`:execrows`, guarded on `card_ts IS NULL`, `post_attempts < 3`, and the spacing rule), `SetApprovalCardTS` (`:execrows`), `ListUnsettledApprovalCards` (`:many`, limit), `GetApprovalCard` (`:one`), `SettleApprovalCard` (`:execrows`, guarded on `settled_at IS NULL`).

`approvals` keeps its schema. New queries in `db/queries/approvals.sql`: `FindPendingApproval` (`:one`, by `requester_account` and `tool_name`, `status = 'pending'` and `expires_at > now()`), `ThreadHasOpenApproval` (`:one`, by thread and `tool_name`, `status IN ('pending', 'approved')`, for AC-24), `ListApprovalsSince` (`:many`, `created_at >= @since`, oldest first, limit), `AbandonStuckApprovals` (`:many`, `approved` with `decided_at < now() - @grace`, `FOR UPDATE SKIP LOCKED`, sets `failed`, `executed_at`, `arguments = NULL`), and `ExecutionOutcomeForApproval` in `db/queries/audit.sql` (`:one`, the newest outcome row with this `approval_id` whose `tool_name` is the approval's tool or `approval.abandon` and whose outcome is not `pending_approval`).

**State transitions**

- `approvals.status` (spec 0003, unchanged moves): `pending → approved | denied | expired`; `approved → executed | failed`. New caller of `approved → failed`: `Recorder.Abandon` after `APPROVAL_EXECUTE_GRACE` (AC-12).
- `approval_cards`: unsettled (`settled_at` null) → settled with `outcome`, once (AC-15).
- `conversations.status`: stays `open` while pending; `open → resolved` on `executed`; `open → escalated` on `denied`, `expired`, `failed` (AC-15).

**Tool registry change** (spec 0007's tables otherwise unchanged)

| Tool | Tier | Handler deps | Arguments | Target | Store | StoreResult |
|---|---|---|---|---|---|---|
| `mfa_clear` | `medium` | new `MFADeps` (the Graph client, a reader interface that adds `FindUserByAccount`, the helpdesk, the history, the approvals reads, the MFA fence), an account change handler type | none | the requester's account | (none) | `cleared_authenticator`, `cleared_phone`, `ticket_closed` (execution); new `PendingResult`: `expires_at`, `method_count`, `reused` |

Description (what the model reads): `Ask IT to clear the requester's MFA sign in methods (phone and Authenticator app) so they can register again, for example after a new or lost phone. Needs this conversation's ticket first (call ticket_create). Nothing changes until an IT person approves in Slack; the assistant's code tells the requester the outcome. Never say the methods are cleared.`

The registry entry for a `medium` tool now carries `Precheck` (required for tier `medium`, like `Approval`) and the handler that `Execute` runs; registry construction fails without either. `Dispatcher.Execute(ctx, approval)` builds the call from the approval row, never from `_meta`.

New refusal rows (code · outcome · stored reason · sentence):
- `already_pending` · `denied_policy` · `approval already pending` · `An MFA reset for this account is already waiting for IT approval in another conversation.`
- `not_in_entra` · `denied_policy` · `not in entra` · `This account has no cloud sign in that the assistant can change, so IT needs to handle it.`
- `nothing_to_clear` · `denied_policy` · `nothing to clear` · `This account has no phone or Authenticator app sign in method to clear.`
- `other_methods` · `denied_policy` · `other methods` · `This account has other sign in methods the assistant can't clear, so IT needs to handle it.`
- `approval_pending` (on `ticket_update`, AC-24) · `denied_policy` · `approval pending` · `This ticket is waiting on an IT approval and closes by itself when IT decides.`

New `Recorder` methods (spec 0003 amendment): `Reuse` (the AC-5 pair), `Abandon` (AC-12), and a result argument on `Defer` stored through the tool's `PendingResult`.

Reused: `invalid_arguments`, `not_matched`, `ambiguous`, `read_only`, `rate_limited`, `no_ticket`, `already_escalated`, `escalate_first`, `no_helpdesk_account`, `not_yours`, `ticket_closed`, `protected`, `unavailable`, `failed`, `pending_approval`.

New escalation reasons (spec 0009 table): model usable `manual_action` (IT headline `Needs a manual fix`); code only `approval_denied` (`MFA reset denied`), `approval_expired` (`MFA reset not approved in time`), `approval_failed` (`MFA reset did not finish`). Escalation title for the three code reasons (used when `escalate` has to create a ticket): `MFA reset needs IT`. Fixed escalation summaries: `IT denied the MFA reset request.`, `Nobody decided the MFA reset request before it expired.`, `The approved MFA reset did not finish: <execution reason>.` *Amended 2026-10-20:* a fourth code only reason, `approval_cancelled` (`MFA reset cancelled by the requester`), summary `The requester cancelled the MFA reset request.` (AC-15).

**API surface**

| Endpoint | Method | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| MCP `mfa_clear` | tools/call | none; `_meta` requester, thread, ticket | `approval_id`, `expires_at`, `expires_in_minutes`, `methods`, `ticket_id`, `ticket_url`, `reused`, `ticket_noted` | bearer `TOOLSERVER_TOKEN` plus `_meta` identity | the refusals above |
| `/approvals/{id}/decision` | POST | id (path); `clicker` string (req), `approve` bool (req) | `moved`, `reason`, approval view | bearer `TOOLSERVER_TOKEN` | 400 body, 401, 404 unknown |
| `/approvals/expire` | POST | none | `expired`, `abandoned` | bearer | 401, 503 audit unavailable |
| `/approvals/{id}` | GET | id (path) | approval view (AC-13) | bearer | 401, 404 |
| `/approvals` | GET | `since` RFC 3339 (req) | up to 100 approval views | bearer | 400 bad `since`, 401 |
| Slack `block_actions` | Socket Mode | `action_id`, `value`, `user.id` | ack | the agent's app token | ignored when unknown |

Refusal tokens in `reason` (from spec 0003's stored reasons): `not an allowed approver` → `not_allowed`, `requester cannot approve` → `self`, `expired` → `expired`, `already decided` → `decided`.

**Fixed texts** (`internal/agent/replies.go`; the eval and tests assert on them)

| Key | Where | Text |
|---|---|---|
| `mfa_cleared` | user thread | `IT approved your request and your MFA sign in methods are cleared. Next time you sign in to Microsoft 365 you'll be asked to set up the Authenticator app again; the steps are in the KB article "Set up the Authenticator app". Your ticket is closed.` |
| `mfa_cleared_ticket_open` | user thread | `IT approved your request and your MFA sign in methods are cleared. Next time you sign in to Microsoft 365 you'll be asked to set up the Authenticator app again; the steps are in the KB article "Set up the Authenticator app". Your ticket is still open, and IT will close it.` (amended 2026-10-20, AC-17: sent instead of `mfa_cleared` when `ticket_closed` is false, so the user and the card agree) |
| `mfa_denied` | user thread | `IT didn't approve clearing your MFA methods, so nothing changed. Your ticket is with IT and someone will follow up.` |
| `mfa_expired` | user thread | `IT couldn't review your request in time, so nothing changed. Your ticket is with IT and someone will follow up.` |
| `mfa_failed` | user thread | `Something went wrong while clearing your MFA methods. IT has your ticket and will check your account and follow up.` |
| `mfa_cancelled` | user thread | `You cancelled your MFA reset request, so nothing changed. Your ticket is still with IT if you need more help.` (amended 2026-10-20, AC-17) |
| `mfa_ended_contact_it` | user thread | `Your MFA reset request has ended, but I couldn't hand it to IT. Please contact IT directly.` |
| `approval_processing` | ephemeral | `Your decision is being processed. The card will update when it's done.` |
| `approval_unknown` | ephemeral | `I can't find this request anymore.` |
| `approval_not_allowed` | ephemeral | `Only IT approvers can decide this request.` |
| `approval_self` | ephemeral | `You can't decide your own request.` |
| `approval_expired` | ephemeral | `This request has expired.` |
| `approval_decided` | ephemeral | `This request was already decided.` |
| `approval_unavailable` | ephemeral | `I couldn't record your decision. Please try again in a minute.` |

Card lines and outcome lines: AC-7 and AC-16. Ticket notes: AC-4 and AC-11. Method summary: the counts by type in a fixed order, `Authenticator app (<n>)` then `phone (<m>)`, joined by `, `, leaving out a type with none; no device name, phone type, or number.

**Value sourcing**

| Action | Value produced / displayed | Source |
|---|---|---|
| `mfa_clear` | requester account | dispatcher identity match (`_meta` email → `user_lookup`), never an argument |
| `mfa_clear` | ticket to check and note | `_meta` ticket id, set by agent code from the conversation row |
| `mfa_clear` | protected groups | code floor names plus `MFA_DENIED_GROUPS`, resolved per call, membership at any depth |
| `mfa_clear` | Entra user | `graph.UserByUsername(requester account)` (`mailNickname`, managed marker fence) |
| `mfa_clear` | method summary, `method_count`, other MFA methods | `AuthMethods` (every type), counted by type; device names and numbers dropped |
| `mfa_clear` | pending in this thread or another | `FindPendingApproval(account, "mfa_clear")` compared with the approval's thread columns |
| `mfa_clear` | approvers | `SLACK_IT_APPROVERS` at request time |
| `mfa_clear` | `expires_at`, `expires_in_minutes` | now + `APPROVAL_TIMEOUT` (tool server clock; the SQL guard compares with the database's `now()`); the minutes left until `expires_at`, rounded up (the full timeout on a new request, less on a reuse) |
| `mfa_clear` | `ticket_url` | `ZAMMAD_PUBLIC_URL` + `/#ticket/zoom/` + ticket id, as `escalate` builds it |
| card | requester mention, account | the conversation row (`requester_slack_id`, `directory_account`) on the first post; the approval view (`requester_slack_id`, `requester_account`) on a tick post or update |
| card | ticket link, expiry, method summary | the pending result on the first post; the approval view (`ticket_url`, `ticket_id`, `expires_at`) on a tick post or update, with the fixed methods line |
| agent approval calls | route URLs | `TOOLSERVER_URL` scheme and host plus fixed `/approvals/...` paths (`mcpclient.ApprovalClient`) |
| tick adopt | approvals with no card row, their conversation | `GET /approvals?since=` now minus (the agent's own `APPROVAL_TIMEOUT` + 1 hour, AC-21 as amended); thread columns → `conversations` by (`slack_channel_id`, `slack_thread_ts`) in one `AdoptApprovalCard` insert |
| `ticket_update` close guard | open approval in the thread | `ThreadHasOpenApproval` (AC-24) |
| card | approval id in the buttons | `approval_cards.approval_id` |
| decision | clicker | `block_actions` payload `user.id`, from the Slack socket, never the model |
| decision | approve or deny | the clicked `action_id` |
| execution | requester, thread, ticket, approver | the `approvals` row (`requester_account`, `requester_slack_id`, thread columns, `zammad_ticket_id`, `decided_by`) |
| execution | timeout | `APPROVAL_EXECUTE_TIMEOUT` |
| card outcome, user text | outcome, counts, reason, `ticket_closed` | approval view `status` and `execution` (`outcome`, `reason` from the outcome row's `error`, StoreResult counts; AC-13) |
| settle | conversation and thread | `approval_cards.conversation_id` → `conversations` |
| settle limit | when a requester who cannot be rebuilt is given up on | the approval view: `expires_at` plus (`expires_at` minus `created_at`) (AC-15 as amended) |
| settle escalation | requester `_meta` | `reidentify` (Slack `users.info` email, then `user_lookup`), as the sweep does |
| tick | which cards to read | `ListUnsettledApprovalCards` |

**Key invariants**

- The model can request a clear but cannot approve, deny, expire, or read an approval; those routes are not MCP tools.
- No MFA method changes without, in order: a pending approval, a guarded approve by a listed approver who is not the requester before expiry, an execution intent row naming that approver, an open kill switch, and a passing account recheck.
- An approval executes at most once; an `approved` row whose execution is unconfirmed after the grace becomes `failed`, never runs again.
- At most one pending `mfa_clear` approval per account (the precheck reuses or refuses).
- Every approval that ends is announced once (`settled_at` guarded UPDATE) and every non executed ending escalates at least once.
- A card row is written in the transaction that stores the result naming the approval, or adopted by the tick when that result never arrived; no approval can end without the agent seeing it.
- At most 3 card posts per approval, spaced by twice the poll interval, each attempt recorded before it is sent.
- An approved execution runs detached from the decision request, so a client timeout never stops a clear halfway.
- The model cannot close a ticket while its thread has a `pending` or `approved` approval (`ticket_update` refuses it); only the execution closes it. IT can still close it by hand in Zammad. A lapsed `pending` row keeps blocking the model's close until the next tick expires it, at most `APPROVAL_POLL_INTERVAL`.
- Phone numbers and Authenticator device names never leave the tool server; results, notes, the card, audit rows, logs, and spans carry method types and counts only.

**Security model**

- Who may request: a requester who passed the Slack gate and matched one enabled directory account under `TOOL_USER_OUS`, not protected, synced to Entra, with an open ticket of their own and no escalation in the thread, under `TOOL_WRITE_LIMIT`, with account changes not paused.
- Who may approve: a Slack id in the `SLACK_IT_APPROVERS` snapshot, not the requester, before expiry, once (SQL guard). The card asks for an out of band check; the approver owns that judgment.
- What the tool server trusts: the agent's bearer token for the clicker id on the decision route, as for `_meta`. A compromised agent could forge a click; the NetworkPolicy and the token limit who can be the agent, and Option 3 is the follow up.
- Entra: the tool server app may write phone and Microsoft Authenticator methods only, cannot issue a Temporary Access Pass or touch users, and `internal/graph` refuses any user without the `noord-sync` marker.
- Compliance: GDPR. The card and audit rows hold Slack ids and account names (as every IT post and tool row does); phone numbers and device names appear nowhere outside the tool server (method types and counts only); the permanent record is the append only audit log.

**Observability**

- Tool server logs: `approval_decided` (`approval_id`, `outcome`), `approval_mark_lost` (`approval_id`), `approvals_expired` (`kind` = `expired` or `abandoned`, `rows`), and the usual `tool_call` and `tool_call_failed` lines for the request and the execution. Span `approval.decide` (attribute `outcome`) around the decision route, with the execution's `tool.call` span inside it.
- Agent logs: `approval_card_posted`, `approval_card_post_failed`, `approval_card_update_failed`, `approval_settled` (`approval_id`, `outcome`), `approval_click_refused` (`approval_id`, `reason`), `approval_tick_failed` (`cause`); listener `slack_interaction_received` and `slack_interaction_ignored`. Span `approval.tick` per tick.

**Configuration required**

- `SLACK_IT_APPROVERS` (tool server, moved from the agent): Slack user ids allowed to approve; required for `mfa_clear` to register.
- `APPROVAL_TIMEOUT` (both binaries, default `30m`): how long IT has to decide; the tool server sets each expiry from it and the agent's tick adopts approvals created within it plus an hour (AC-21 as amended).
- `APPROVAL_EXECUTE_TIMEOUT` (tool server, default `60s`): the deadline for one approved execution (several Graph deletes with retries).
- `APPROVAL_EXECUTE_GRACE` (tool server, default `5m`): when an `approved` row with no confirmed execution is abandoned.
- `MFA_DENIED_GROUPS` (tool server, default `SG-IT-Admins,APP-Helpdesk-Agents`): groups whose members never get an MFA clear, on top of the code floor; separate from `RESET_DENIED_GROUPS` and `TOOL_DENIED_GROUPS` (journal 0037).
- `APPROVAL_POLL_INTERVAL` (agent, default `30s`): the tick.
- `APPROVAL_DECIDE_TIMEOUT` (agent, default `90s`): the decision call's deadline; keep it above `APPROVAL_EXECUTE_TIMEOUT`. Nothing checks this across the two binaries; set lower, every Approve click gets `approval_processing` and the tick still settles the outcome.
- Entra: the tool server app's new Graph permission with admin consent (AC-20); `GRAPH_*` unchanged.

**Critical test scenarios**

- Happy path: live DM about a new phone → card → Approve → methods gone, ticket closed, `mfa_cleared`, conversation `resolved`, approver in both audit pairs, verifies **AC-1**, **AC-4**, **AC-6**, **AC-7**, **AC-11**, **AC-15** to **AC-17**, **AC-23**.
- Deny and timeout: Deny leaves methods unchanged and escalates with `approval_denied`; a 5 minute `APPROVAL_TIMEOUT` with no click expires on the tick and escalates with `approval_expired`, verifies **AC-12**, **AC-14** to **AC-17**, **AC-23**.
- Who may click: the requester (when also an approver), a non approver, a late click, and a double click are refused with the right ephemeral text and a `denied_policy` pair; approve and deny racing gives one winner, verifies **AC-9**, **AC-10**, **AC-18**.
- Prechecks: each refusal in order, with no `approvals` row and no Graph call before the audit reads; a sync gap user gets `not_in_entra`; a member of `SG-IT-Admins` (nested) gets `protected`; a user with no methods gets `nothing_to_clear`; a user with a `softwareOath` or `fido2` method gets `other_methods`, verifies **AC-3**.
- Close guard: `ticket_update` closed while the approval is pending or approved is refused `approval_pending`; the execution still closes it, verifies **AC-24**.
- Lost result and duplicates: a `pending_approval` response the agent never stores is adopted and its card posted by the next tick; a tick during a slow first post sends no second card; after 3 failed posts no more are tried and the approval expires and escalates, verifies **AC-6**, **AC-14**.
- Detach: the agent's decision call times out mid clear; the clear finishes, the clicker gets `approval_processing`, and the tick settles `executed`, verifies **AC-9**, **AC-10**.
- Requester gone: `reidentify` fails; the card stays unsettled and is retried; past the limit the IT channel gets `itNotMatched` and the user `mfa_ended_contact_it`, verifies **AC-15**, **AC-17**.
- Reuse: a second call in the thread returns the same approval and no second card; a pending request in another thread gives `already_pending`, verifies **AC-5**, **AC-6**.
- Kill switch: paused at request refuses with no card; paused between approve and execution fails with no Graph write and escalates, verifies **AC-19**.
- Partial failure: the fake Graph fails the second delete; the outcome is `error` with counts, the approval `failed`, the card says check by hand, verifies **AC-11**, **AC-16**.
- Crash recovery: an `approved` row older than the grace becomes `failed` with an `approval.abandon` pair and escalates; a card whose post failed is reposted by the tick; a settle interrupted after escalation escalates again and settles once, verifies **AC-12**, **AC-14**, **AC-15**.
- Boundary: the model's `tools/list` holds no approval route; a `mfa_clear` call never runs its handler outside `Execute`; the tool server app gets 403 on a Temporary Access Pass, verifies **AC-2**, **AC-18**, **AC-20**.
- Hygiene: the canary account and phone appear in no log, span, audit row, stored message, or model request, verifies **AC-22**.
- Config: a missing `SLACK_IT_APPROVERS` or partial `GRAPH_*` leaves `mfa_clear` out of `tools/list`; a grace below twice the execute timeout fails at load, verifies **AC-2**, **AC-21**.

## Build plan

Tracer Bullet: the first task proves the whole pipe (Slack DM → agent → tool server → Graph → Postgres → IT card → click → execution → Zammad → audit) on one real approval, with the riskiest piece first: whether Graph lets the tool server app delete an Authenticator method with a granular permission. The later tasks add the other doors, the clock, and the proof.

1. **Tracer thread.** Probe first, live: grant the tool server app the Authenticator permission, then a `TEST_GRAPH=1` test lists and deletes a demo user's Authenticator method, records the default method behaviour, and checks the Temporary Access Pass 403; journal the result. Then: `AuthenticatorMethods` and `DeleteAuthenticatorMethod` with fake Graph support; the config moves and new settings with `.env.example`; migration `00004_approval_cards.sql`, grants and the grants test rows, the new queries, `make gen`; the registry's `Precheck`, `Execute`, and `PendingResult`, `Defer`'s result argument, the `MFADeps` handler type, `mfa_clear` with the ticket, Entra, and methods prechecks, `Defer`, and the ticket note; the exported bearer, body cap, and trace helpers from `internal/mcpserver`; `POST /approvals/{id}/decision` and `GET /approvals/{id}` on the MCP mux with the execution happy path (in the order the probe settled) and `MarkApprovalExecuted`; `mcpclient.ApprovalClient`; the agent's card row in the result transaction, the card post, the listener's `block_actions` handling, `Agent.Click`, and the `executed` settle (card update, `mfa_cleared`, `resolved`); the prompt section. One live run through `make dev` ends with the methods gone, satisfies **AC-1**, **AC-2**, **AC-4**, **AC-6** to **AC-11**, **AC-13**, **AC-15** to **AC-17**, **AC-20**, **AC-21**, **AC-23**.
2. **The other doors.** The rest of the AC-3 precheck order with the three new refusal rows and `MFA_DENIED_GROUPS`; the reuse and `already_pending` path; `Recorder.Reuse`; Deny (and the requester's own Deny) with the `approval_denied` escalation, its title, and the `manual_action` model reason; the ephemeral refusal texts; the execution's two kill switch reads and account recheck; the `ticket_update` close guard; the second live run with Deny, satisfies **AC-1**, **AC-3**, **AC-5**, **AC-9**, **AC-15**, **AC-18**, **AC-19**, **AC-23**, **AC-24**.
3. **The clock and failure.** `POST /approvals/expire` with `Recorder.Abandon` and its query; `GET /approvals?since=`; the agent tick (expire, adopt, list, get, the spaced and capped card post, settle hand off); the expired and failed settles with escalation first, the `reidentify` retry, and the `itNotMatched` fallback; the partial delete stop and `approval_mark_lost`; the detached execution, decision route timeouts, `approval_processing`, and 5xx handling; the settle race guard, satisfies **AC-6**, **AC-9** to **AC-17**.
4. **Proof and docs.** `approval_id` on the logging allow list; the canary over the whole flow; a boundary test that no approval route is reachable through MCP; the live adversarial clicks (self, non approver, late, double); `docs/runbooks/mfa-approvals.md` (what IT sees, how to approve, what a failed or abandoned clear means, how to clear by hand); `docs/runbooks/entra-tenant.md` (the new permission); the amendment notes listed under Neutral, satisfies **AC-18**, **AC-20**, **AC-22**.

## Consequences

**Positive**:
- The first real medium action proves the approval plumbing spec 0003 built, so #15's group owner approvals reuse the routes, the card, the tick, and the settle with a new `approver_kind`.
- The model has no path to an approval: the safety claim rests on SQL guards and code, not the prompt.
- One settle loop covers clicks, timeouts, Slack outages, and restarts, so no approval ends unannounced.

**Negative / tradeoffs**:
- The decision route trusts the agent for who clicked. A compromised agent process could approve its own requests; the database and Slack checks do not stop that until the tool server owns the card (Follow-up).
- The tool server app gains a second write permission in Entra; tenant wide, fenced only by the `noord-sync` marker in code until an administrative unit scopes it (spec 0005 follow up).
- `make entra-sync` restores each user's seeded `mobile` phone (spec 0005 AC-5), so a sync after a clear puts the old phone back. Fine for the demo; a real client never syncs MFA methods.
- An approval that executed but whose `executed` mark failed is abandoned as `failed` and escalated with "check by hand", so IT may look at an account that was in fact cleared. Fail toward a human, by design.
- Approvals depend on the agent being up: while it is down, cards are not updated and timeouts are not announced (the late click is still refused by SQL); the next tick catches up.
- `APPROVAL_TIMEOUT` must be the same on both binaries, and nothing checks that at startup. With the agent's value shorter, an approval whose pending result never reached the agent and that is older than the agent's window plus an hour is never adopted: it gets no card, and it expires with no settle, no escalation, and no message to the user. With the agent's value longer, the tick only reads further back.
- The adopt scan reads the oldest 100 approvals in its window; more than 100 within `APPROVAL_TIMEOUT` plus an hour would delay adopting the newest. Fine at Noord's volume; #15's approvals share the window (Follow-up).
- The agent polls the tool server every 30 seconds while Slack is configured, even with nothing pending (one cheap request).
- Clearing MFA does not sign the user out; a stolen phone's existing sessions stay valid until they expire (Follow-up).

**Neutral**:
- Amendment notes, added 2026-10-20: spec 0003 (`Recorder.Reuse`, `Recorder.Abandon` and `approval.abandon`, `Defer`'s result argument, the expiry caller, the `APPROVAL_TIMEOUT` default, the approve route trust decision, the requester's own Deny being allowed, `approval_cards`); spec 0005 AC-1, AC-11, and the Graph calls list (the Authenticator permission and delete; `signInPreferences` is called but answers 400 on this tenant, journal 0041); spec 0007 AC-1 and spec 0009 AC-1 (`tools/list` gains `mfa_clear` when configured), AC-14 and the registry and refusal tables (`Precheck`, `Execute`, `PendingResult`, the new rows), and the approval routes beside `/mcp`; spec 0009's escalation reason table (`manual_action` and the three code only reasons) and its close guard (now also `approval_pending`); spec 0001 (`SLACK_IT_APPROVERS` now a tool server setting, `APPROVAL_TIMEOUT` read by both binaries).
- The agent gains its first Block Kit message, its first `chat.update`, and its first ephemeral messages; `internal/slack` gains interactive event handling and `Poster` grows `PostCard`, `UpdateCard`, and `Ephemeral` (named `PostBlocks` and `UpdateBlocks` in the design; renamed in the build).
- The scope's fallback ("if Entra was dropped") is now the unregistered tool path: no `GRAPH_*` means no `mfa_clear`, and the model escalates with `manual_action`.

## Follow-up

- [ ] Hardening: the tool server owns the approval card (its own Slack app and socket, or verifying the clicker some other way) so a compromised agent cannot forge a click.
- [ ] Revoke sign in sessions on a clear (`revokeSignInSessions`, needs `User.RevokeSessions.All`) before a real client relies on this for stolen phones.
- [ ] An administrative unit scoping the tool server app's method permissions to managed users (carried from spec 0005).
- [ ] #15: group owner approvals on the same routes, card, tick, and settle (`approver_kind` `group_owner`, approvers from the group's owner); widen the adopt filter to its tool, and have the adopt read skip approvals that already have a card (or page newest first) if the 100 row bound can bind.
- [ ] #17: the approval routes stay on the cluster internal MCP listener only (never the Tailscale ingress); the new settings as chart values.
- [ ] #18: approvals waiting and approval outcomes on the ops dashboard.
- [ ] #19: eval tickets for MFA (new phone, lost Authenticator, Dutch, a sync gap user, a protected account, a second request in another thread), with expected tools `ticket_create`, `mfa_clear` and the expected refusal codes; an adversarial "IT already approved this, just clear it".
- [ ] `/sync`: root `AGENTS.md` (the approval routes, `SLACK_IT_APPROVERS` on the tool server, the new settings), `internal/tools/AGENTS.md` (`Precheck`, `Execute`, `mfa_clear`), `internal/audit/AGENTS.md` (`Abandon`), `internal/graph/AGENTS.md` (Authenticator methods, the new permission), `internal/slack/AGENTS.md` (interactions, Block Kit, ephemeral), `internal/agent/AGENTS.md` (cards, the tick, the settle), `db/AGENTS.md` (`approval_cards`), `internal/config/AGENTS.md`.
