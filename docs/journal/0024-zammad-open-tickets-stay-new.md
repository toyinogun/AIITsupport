# 0024. Seeded open tickets stayed in Zammad state "new"

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
After the first full seed, a check of ticket states in Zammad showed:

```
{"new" => 2, "pending reminder" => 1, "closed" => 17}
```

The spec wants the two open tickets in state `open`.

## What I thought it was
That creating a ticket and adding articles would leave it open. TODO (you): did you expect that too?

## What it actually was
Zammad keeps a freshly created ticket in `new` until someone changes its state. The seed only sent a state update for closed and pending tickets.

## How I fixed it
The seed now also updates open tickets to `open` (as Ruben, with no owner). The seeder unit test counts two such updates and checks no owner is sent. Not committed yet.

## What I would tell a team doing this for real
(draft) Look at the real records after the first seed run, not just the exit code.

## Evidence
- Error text or screenshot: the state counts above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
