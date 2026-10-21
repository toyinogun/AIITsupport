# Review, feat/access-requests, 2026-10-21

**Reviewed by**: Sonnet 5.5 (author model: Opus)
**Scope**: 101 files (about 5,900 added lines, much of it tests and generated sqlc output), branch vs main, including the uncommitted test additions in `internal/agent/access_test.go` and `internal/tools/group_access_test.go`
**Verdict**: Approve with nits

## Summary

The change adds feature 15 (spec 0013): a department scoped `group_search`, a medium `group_access_request` that records an approval addressed to the group's AD owner, a DM card posted by agent code, a kind aware decide that proves the clicker twice (stored Slack id, then confirmed email mapped to an AD account), and an audited add with a full recheck and read back. I traced the security critical paths (identity from `_meta`, owner and self checks in SQL, kill switch before and after the recheck, audit pair around the write, credential boundary) and found no blocker or major defect. `go build`, `go vet`, `golangci-lint` (0 issues), `sqlc diff` (no drift) and `go test -race ./...` pass, and the Postgres backed tests in `db`, `internal/db`, `internal/audit` and `internal/agent` pass against the local database. The findings below are resilience, cost, and doc drift items.

## Minor

### 🟡 Card post bookkeeping reuses an exhausted context, `internal/agent/access.go:139`
**Problem**: `postOwnerCard` creates one `AGENT_TOOL_TIMEOUT` (20s) context at line 96 and uses it for the attempt claim, the approval view read (which now does up to four live LDAP sessions on the tool server), `users.lookupByEmail`, and, after the post, `SetCardPosted`. The IT card path (`postCard`) deliberately opens a fresh context for the store write after the post; this path does not.
**Why it matters**: if the view read or the lookup is slow, the DM can be sent and then `SetCardPosted` fails on an expired context. The card has no `card_ts`, so the tick reposts a second DM 60s later. Both cards would work (the second post stores the owner's Slack id), but the first is never updated at settle and the owner sees a stale duplicate.
**Suggested fix**: open a fresh detached `ToolTimeout` context for `SetCardPosted`, as `postCard` does. Add a test where the store write fails after a successful post.

### 🟡 An LDAP hiccup consumes a post attempt and can cancel a valid request, `internal/agent/access.go:98` and `:114`
**Problem**: the attempt is claimed (`RecordCardPostAttempt`) before the view is read. When the tool server's live owner read fails, the view comes back with empty owner fields, the agent logs `errOwnerUnread`, and the attempt is already spent. Three such ticks (about three minutes at the default 30s poll with 2x spacing) end in `cancelOwnerCard`, which settles as owner unreachable and escalates `no_group_owner`. The same happens permanently for an enabled owner whose `displayName` and `mail` are both empty.
**Why it matters**: a short directory outage cancels requests whose owner is perfectly reachable and sends them to IT. Spec AC-10 says the failed LDAP read is "tried again on the next tick" and counts only Slack errors as attempts.
**Suggested fix**: do not count a failed directory read against the three attempts (read the view before claiming, or give the claim back), or make the cancel require three attempts that actually reached Slack.

### 🟡 `group_search` opens dozens of LDAP sessions per call, `internal/tools/group_access.go:105`
**Problem**: every session is a fresh LDAPS dial plus bind. For each of up to 10 hits the loop runs `CheckGroup` (two sessions, and each re-resolves every denied group), `GetUser` for the owner, and `UserInChainMemberOf`: about 40 sessions, sequential, plus `ListGroups`, all under `TOOL_CALL_TIMEOUT` (15s).
**Why it matters**: it works on the dev Samba, but latency grows linearly with TLS handshake cost and a larger directory or a slower DC can run into the call timeout, which surfaces as `unavailable` for a plain search.
**Suggested fix**: resolve the denied group DNs once per search and test nesting for all hits in one chain query, or reuse one connection for the loop. Not needed for this slice if Noord's volume is the target; record it as a follow-up.

### 🟡 Every unsettled owner card costs 3 to 4 LDAP sessions per tick for the whole window, `internal/approvalapi/approvalapi.go:343`
**Problem**: `viewOf` always runs `ownerFields` for a group owner approval, and the agent tick calls `GET /approvals/{id}` for every unsettled card every 30s. With the 8h window that is about 960 reads of `FindUserByAccount`, `GroupByName` and `GroupNestedInto` per pending request. The spec acknowledges the cost, but the live fields are only needed before the card posts and at settle (for the owner's name).
**Why it matters**: the cost scales with pending requests times 8 hours, and every tick also depends on LDAP being up for a view whose only needed fields are already in Postgres.
**Suggested fix**: skip the live owner fields for the tick's read of a card that already has a `card_ts` (a query flag, or read the owner name once at settle), or cache them for the window.

### 🟡 `group_search` `count` includes groups that are then dropped, `internal/tools/group_access.go:103`
**Problem**: `count` is taken before `Fence.CheckGroup` runs, so a group nested in a denied group is dropped from `groups` but still counted. The model can see `count` greater than `len(groups)` with `truncated` false.
**Why it matters**: spec invariant "A user never sees a privileged or denied group, by name or by count" is not met for nested denied groups, and the model gets an inconsistent signal. AC-2 defines it this way, so this is a spec level conflict as much as a code one.
**Suggested fix**: either run the fence over every hit before counting (cost: see the session count above) or amend AC-2 and the invariant to say `count` is an upper bound, and tell the model (prompt) not to quote it.

### 🟡 `Also gives` line is unbounded, `internal/agent/access.go:42`
**Problem**: `nested_into` is joined into one Slack section. A section text over 3000 characters is rejected by `chat.postMessage` (`invalid_blocks`).
**Why it matters**: an owner of a group nested into very many groups would never get a card; three failed posts then cancel the request as owner unreachable. Unlikely at Noord's size, but the failure is silent and misleading.
**Suggested fix**: cap the list (for example the first 10 names plus a count) when building the line.

### 🟡 Tests miss two agent failure edges, `internal/agent/access_test.go`
**Problem**: no test covers `SetCardPosted` failing after a successful `PostCard`, or the "owner fields empty" retry path ending in a cancel (only the Slack rate limit path is tested).
**Why it matters**: these are the two paths behind the first two findings, so the behaviour is currently unspecified by tests.
**Suggested fix**: add both cases to the harness with the failing store and view fakes.

## Nits

- ⚪ `internal/ldap/groups.go:179`, `AddMember` also maps LDAP 68 (`entryAlreadyExists`) to `ErrAlreadyMember`; the spec says only code 20. Harmless because the read back confirms membership, and it matches what Windows AD returns, but say so in the spec or the comment.
- ⚪ `db/migrations/00006_access_requests.sql:43`, the Down recreates `approvals_one_pending_idx` on `(requester_account, tool_name)`, which fails if several pending `group_access_request` rows exist for one account. Delete or expire those rows first in the Down.
- ⚪ `internal/tools/AGENTS.md:35`, still says a medium tool needs a `Precheck` and an `MFAHandler`; it can now be `GroupPrecheck` with a `GroupHandler`. The spec lists this under the pending `/sync` item, as are root `AGENTS.md` (new settings, cancel route) and `internal/approvalapi/AGENTS.md` ("exactly `{clicker, approve}`", four routes).
- ⚪ `docs/specs/0013-access-requests-group-owner/index.md:4`, status is still "In Progress" while the scope marks build, verify and test done.
- ⚪ `internal/tools/group_access.go:191`, `res.refusal` for `protected` carries a resolved group whose canonical name becomes the audit target; fine per AC-5, just worth knowing that a protected refusal discloses nothing extra because the audit log is not model facing.

## Strengths

- The trust model is enforced in code at every layer: the decide SQL is kind aware and rejects an empty or unlisted decider and a self approval; the click is proven by the stored Slack id and then by a confirmed email mapped to an AD account by the tool server; `Execute` rechecks the group, department, owner and both accounts between two kill switch reads and reads the add back before closing the ticket.
- The audit and logging discipline is thorough: audit allow lists exclude the reason and the search terms, the user supplied reason is folded to one line and escaped on the card under an "unverified" label, and `TestCanaryAccessRequest` drives search, request, view, approve and cancel through the real logger and redacting span exporter.
- Failure handling is designed, not bolted on: guarded SQL for cancel and settle, adoption of lost results by the tick, owner unreachable distinguished from a timeout through the audit row, and an escalation "at least once" with Slack posts "at most once".
- Test depth is high (group tool, dispatcher, approval routes, Postgres level decide and cancel, column grants, canary, seed tag validation), and the table keyed settle policy keeps the MFA and access flows from drifting apart.

## Test coverage

Configured (Go stdlib, `-race`). The new logic is well covered: search filters and truncation, every refusal in the AC-5 order, reuse and the three limits, pause at request and at execute, recheck failures, race and read back, close guard, owner view, decider resolution, body validation for the decision and cancel routes, kind aware decide and cancel against Postgres, column grants, seed tag validation, and a canary over the full pipe. The live Samba, Zammad and Slack runs (AC-23) are documented in the journal and are not re-run here. Gaps are limited to the two agent edges noted above.
