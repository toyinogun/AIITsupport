# Verify: MFA re-registration with approval · spec 0012 · updated 2026-10-20 (build tasks 2 to 4)
_Steps derived from spec 0012 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones. Build task 1 (the tracer thread) landed first; steps for later tasks are marked._

## UI / manual
- [x] In Slack, DM the assistant as a demo user: "I got a new phone and my Authenticator is gone" → calls `user_lookup` (code), `ticket_create`, `mfa_clear`; the user is told IT must approve, within the minutes given, and that nothing has changed yet; the conversation stays `open` → AC-1, AC-4
- [ ] The IT channel gets one new message (not a thread reply) with the lines `MFA reset request`, `Requester: <@id> (account)`, `Ticket: open in helpdesk` (or `id N` without `ZAMMAD_PUBLIC_URL`), `Methods: Authenticator app (n), phone (m)`, `Expires: HH:MM UTC`, the confirm line, and Approve (with a confirm dialog) and Deny buttons → AC-6, AC-7
- [x] The ticket has the internal note `MFA reset requested. Waiting for IT approval until ... Methods: ...` → AC-4
- [x] Click Approve as a Slack user who is not in `SLACK_IT_APPROVERS` → a private `Only IT approvers can decide this request.`; nothing changes → AC-9, AC-18
- [ ] As the requester (when also listed as an approver), click Approve → a private `You can't decide your own request.` → AC-18
- [x] Click Approve as a listed approver → `make entra-mfa NAME=<user>` lists no Authenticator or phone method; the ticket is closed with both notes; the card shows `Approved by <@id>. Cleared n Authenticator and m phone methods. Ticket closed.` with no buttons; the user gets the `mfa_cleared` text; the conversation is `resolved` → AC-11, AC-15 to AC-17, AC-23
- [ ] Click again on the decided card → a private `This request was already decided.` → AC-9, AC-18
- [x] A second live run where IT clicks Deny → methods unchanged; the ticket is escalated with `approval_denied`; the card says `Denied by <@id>. Escalated.`; the user gets `mfa_denied`; IT gets the escalation post → AC-15 to AC-17, AC-23 (task 2)
- [x] With account changes paused, the request is refused `read_only` and no card is posted → AC-19

## Commands
- [x] `TEST_DATABASE_URL=postgres://noord:noord-dev@127.0.0.1:55432/noord?sslmode=disable go test -race ./internal/tools/ ./internal/approvalapi/ ./internal/agent/ ./internal/audit/ ./internal/slack/ ./internal/config/ ./internal/db/ ./db/` → all pass → AC-2 to AC-11, AC-13, AC-15 to AC-18, AC-21
- [ ] `set -a && . ./.env && set +a && TEST_ENTRA=1 TEST_ENTRA_MFA_USER=<username> go test -run LiveAuthenticatorProbe -v ./internal/graph/` → lists the Authenticator method, reports whether `signInPreferences` is readable, and the Temporary Access Pass POST is 403; results go in the journal and `docs/runbooks/entra-tenant.md` → AC-20
- [x] `psql` as `noord`: `\dp approval_cards` → `noord_agent` has SELECT, INSERT and UPDATE on `card_ts, post_attempts, last_post_at, settled_at, outcome` only; the tool server and eval roles have nothing → data model grants
- [x] After the live approve: `SELECT tool_name, outcome, approver_slack_id FROM audit_log WHERE approval_id = '<id>' ORDER BY id` → the `mfa_clear` pending pair, the `approval.approve` pair, and the execution pair, the last two with `approver_slack_id` set → AC-11, AC-23
- [x] Without `SLACK_IT_APPROVERS` or with any `GRAPH_*` unset, the model's `tools/list` has no `mfa_clear` → AC-2, AC-21
- [ ] `make lint` and `go test -race ./...` → clean; `make check` after the first commit (its `gen-check` diffs against git) → AC-21, AC-22

## Value sourcing
- [ ] Requester account: send the request from a Slack user whose email matches a different directory account than the conversation's → the approval names only the matched account; no tool argument can change it
- [ ] Ticket: `mfa_clear` with no ticket in the thread → `no_ticket`; with another user's ticket id → `not_yours`
- [ ] Protected groups: a member of `SG-IT-Admins` through a nested group → `protected`; a name in `MFA_DENIED_GROUPS` that does not resolve → every request `protected` and a `denied_group_unresolved` log
- [ ] Entra user: a user missing from Entra or without the `noord-sync` marker → `not_in_entra`
- [ ] Method summary and count: a user with a `softwareOath` or `fido2` method → `other_methods`; a user with none → `nothing_to_clear`; the card and note show counts only, never a device name or number
- [ ] Pending in this thread or another: repeat the request in the same thread → the same `approval_id`, `reused` true, no second card or note; from another thread → `already_pending` (task 2 live)
- [ ] Approvers: change `SLACK_IT_APPROVERS` after a request → that approval keeps the old list (snapshot)
- [ ] Expiry: `expires_at` is the tool server clock plus `APPROVAL_TIMEOUT`; with `APPROVAL_TIMEOUT=5m` a late click is refused `This request has expired.` (the tick that announces it is task 3)
- [ ] Ticket URL: unset `ZAMMAD_PUBLIC_URL` → the card shows `Ticket: id N`
- [ ] Clicker and decision: the clicker is the Slack payload's user, and Approve or Deny comes from the button clicked; the model's tools hold no route that decides
- [ ] Execution: requester, thread, ticket, and approver come from the `approvals` row; disable the account between approve and execution → `denied_policy` `account not eligible`, no method touched
- [ ] Settle requester: delete or deactivate the requester's Slack user before a Deny → the card stays unsettled; past the limit IT gets the not matched post and the user `mfa_ended_contact_it` (retry needs the task 3 tick)

## Build tasks 2 to 4 (added 2026-10-20)
### UI / manual
- [x] With `APPROVAL_TIMEOUT=5m` on both binaries, request a clear and click nothing → within about `APPROVAL_POLL_INTERVAL` after the expiry the card says `Expired with no decision. Escalated.`, the user gets `mfa_expired`, IT gets the `MFA reset not approved in time` post, the conversation is `escalated` → AC-12, AC-14 to AC-17
- [x] While the request is pending, ask the assistant to close the ticket → it says the ticket closes by itself when IT decides; the ticket stays open → AC-24
- [x] Stop the agent, request a clear through MCP by hand (or let a post fail), start the agent → its first tick posts the card once, with `Methods: listed in the ticket note.` → AC-6, AC-14
- [ ] Approve, then kill the tool server mid clear (or block Graph) and wait `APPROVAL_EXECUTE_GRACE` → the approval ends `failed`, the card says `... did not finish (execution not confirmed). Escalated, check the account by hand.` → AC-12, AC-16
- [ ] Live adversarial clicks from real Slack accounts: the requester (also an approver) clicks Approve, a non approver clicks, a click after expiry, a double click → each gets its private text and a `denied_policy` pair; nothing changes → AC-18
- [x] Live Deny from Slack → methods unchanged, ticket escalated with `approval_denied`, `mfa_denied` in the thread → AC-23

- [ ] With the requester in `SLACK_IT_APPROVERS`, the requester clicks Deny on their own card → the card says `Cancelled by the requester <@id>. Escalated.`, the requester gets `mfa_cancelled` (never `mfa_denied`), IT gets the `MFA reset cancelled by the requester` post; the audit `escalate` outcome has reason `approval_cancelled` → AC-15 to AC-17 (amended)
- [x] Ask for an MFA reset live → the pending reply says IT decides and a message will come, and never describes registering a new phone or setting up the Authenticator as if approval were certain → AC-1 (amended)

### Commands
- [x] `TEST_DATABASE_URL=... go test -race -run 'TestPgAbandonAndOpenApprovals|TestPgApprovalCards' ./internal/audit/ ./internal/agent/` → pass: abandon fails only approved rows past the grace with an `approval.abandon` pair; the close guard sees pending and approved only; card adopt, capped attempts, and the once only settle hold as `noord_agent` → AC-12, AC-14, AC-15, AC-24
- [x] `go test -race -run 'TestExpireAndAbandon|TestMFACloseGuard|TestTick|TestRunTicksAtStart' ./internal/approvalapi/ ./internal/tools/ ./internal/agent/` → pass → AC-12, AC-14, AC-15, AC-24
- [x] `go test -race -run 'TestCanaryMFA|TestApprovalRoutesAreNotTools' ./internal/mcpserver/ ./internal/agent/` → pass: no account, seed name, Slack id, phone, method id, or device name in logs or spans; no phone, method id, or device name in audit rows, MCP results, route answers, or ticket notes; `tools/list` gains only `mfa_clear` → AC-2, AC-18, AC-22
- [x] `curl -X POST -H "Authorization: Bearer $TOOLSERVER_TOKEN" http://localhost:18081/approvals/expire` → `{"expired":n,"abandoned":m}`; without the token → 401 → AC-12
- [x] Load the agent with `APPROVAL_TIMEOUT=4m` → it refuses to start naming `APPROVAL_TIMEOUT` → AC-21 (as amended: both binaries read it)

### Value sourcing
- [ ] Adopt window: the agent's `GET /approvals?since=` is now minus (`APPROVAL_TIMEOUT` + 1 hour), read from the agent's own `APPROVAL_TIMEOUT`; set it differently on the two binaries and an approval older than the agent's window is never adopted (keep them equal)
- [ ] Close guard: an `mfa_clear` approval in another thread does not block this thread's close; one in this thread, pending or approved, does

## Acceptance criteria coverage
- AC-1 prompt and live DM · AC-2 tools/list and registry tests · AC-3 `TestMFAClearRefusals`, `TestMFAClearChecksBeforeGraph` · AC-4 `TestMFAClearRequestDefers` · AC-5 `TestMFAClearReuse` · AC-6 `TestMFARequestPostsTheCard`, `TestMFACardPostFails` · AC-7 card tests and `TestCardBlocks` · AC-8 `TestApprovalClicks` · AC-9 `TestMFARefusedClicks` · AC-10, AC-13 `internal/approvalapi` tests · AC-11 `TestMFAClearExecute`, `TestMFAClearDeletesTheDefaultLast`, `TestMFAClearStopsAtTheFirstFailedDelete`, `TestMFAClearKillSwitchReads`, `TestMFAClearRechecksTheAccount`, `TestMFAExecuteRefusesABadApprovedRow` · AC-15 to AC-17 `TestMFAApproveSettles`, `TestMFADenyEscalates`, `TestMFAFailedOutcomeLine`, `TestMFASettleRequesterGone`, `TestMFASettleRunsOnTheThreadWorker` · AC-18 `TestRefusedClicks` · AC-19 `TestMFAClearPausedAtRequest` · AC-20 the live probe · AC-21 config tests · AC-22 `TestCanaryMFAClear`, `TestCanaryMFAApproval` · AC-23 the two live runs (Deny still owed) · AC-12 `TestExpireAndAbandon`, `TestPgAbandonAndOpenApprovals` · AC-14 `TestTick*`, `TestRunTicksAtStart`, `TestPgApprovalCards` · AC-24 `TestMFACloseGuard`, `TestMFACloseGuardEndsWithTheApproval` · AC-2, AC-18 boundary `TestApprovalRoutesAreNotTools`

## Verify run 2026-10-20
- `/check verify` ran four live rounds (paused, approve, timeout, deny with `already_pending`), the close guard and the tick adopt by hand over MCP, and the commands above; no failures. Ticked steps are the ones that run covered.
- Confirmed live during the build by the engineer, not rerun in this verify: the requester's own Approve and cancel (AC-18, amended AC-15 to AC-17), the Authenticator delete order (AC-11, AC-20, journal 0041), and the adversarial clicks. A second or late click on a settled card can't be made from Slack (AC-16 removes the buttons); the SQL guard tests cover it.
