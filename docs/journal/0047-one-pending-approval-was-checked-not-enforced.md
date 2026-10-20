# 0047. "One pending approval per account" was checked, not enforced

**Date:** 2026-10-20 · **Week:** 1 · **Feature:** #14 MFA re-registration with approval
**Time lost:** ~30 min (estimate) · **Story worthy:** post

## What I saw
The fresh model review (`docs/reviews/2026-10-20-docs-spec-0012.md`, on a different model than wrote the code) flagged it: spec 0012 lists "at most one pending `mfa_clear` approval per account" as a key invariant, but only the precheck read it. The precheck reads the pending approval and `Defer` inserts a new one later, so two requests from two Slack threads for the same person could both pass and put two cards in the IT channel. Approving the second one after the first had cleared the methods would end `failed` with "nothing to clear" and an escalation IT has to read.

## What I thought it was
TODO (you): what did you first think it was? (The per thread worker serialises one thread, which made it look safe; a user can have two threads.)

## What it actually was
A check then act race. The rule lived in Go, between a read and a later insert, and nothing in Postgres stopped a second pending row.

## How I fixed it
- Migration `00005_one_pending_approval.sql`: a partial unique index `approvals_one_pending_idx` on `(requester_account, tool_name) WHERE status = 'pending'`.
- `Defer` first expires that account's overdue pending rows (new query `ExpireDueApprovalsFor`, with the usual `approval.expire` audit pair), so a row the 30 second tick has not reached yet never blocks a new request.
- A unique violation on that index becomes `audit.ErrApprovalPending`; the dispatcher answers it with the existing `already_pending` refusal and its audit pair. The rolled back transaction leaves no rows.
- Tests: a Postgres race test (two `Defer`s at once, exactly one wins), a lost race test through the dispatcher, and a two goroutine request test.

The same review pass fixed eight smaller findings: a click on a row the tick already expired answered "already decided" instead of "expired"; the user was told "Your ticket is closed" when the close had failed (new text `mfa_cleared_ticket_open`); Graph failures logged `cause=other` (now `graph_<status>`); the live probe could mint a real Temporary Access Pass if the app ever had the wider permission (now it deletes it and fails loudly); two Authenticator registrations failed on whichever was the default (a refused one is now deferred to the end once); the list route did one query per row and the tick retried every adopt insert; no test made the execute timeout fire mid clear; and the CI fork check failed open (journal 0048). Not committed yet.

## What I would tell a team doing this for real
(draft) If the spec calls something an invariant, put it in the database; a check in code is a hope, and a second model reading the diff is how you find out.

## Evidence
- Error text or screenshot: the review finding "At most one pending approval per account is checked, not enforced, `internal/tools/mfa_clear.go:64`"
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
- Small snags on the way: `db/migrations_test.go` hardcodes the latest migration version (4 to 5), and `make gen-check` fails while the branch's sqlc output is still uncommitted (`go tool sqlc diff` is clean).
