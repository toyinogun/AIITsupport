# 0035. Zammad gives merged tickets no close time, and a reclosed ticket keeps its first one

**Date:** 2026-10-19 · **Week:** 1 · **Feature:** #12 PII redaction & retention
**Time lost:** ~20 min (estimate) · **Story worthy:** both

## What I saw
Spec 0010 planned one Zammad scheduler, `noord-retention`, that deletes the assistant's tickets when "state is closed or merged and close time is more than N days ago". The spec asked for probes before building it. A probe inside a rolled back transaction printed:

```
"closed":{"close_at":"2026-10-19T17:33:37.092Z","last_close_at":"2026-10-19T17:33:37.092Z"},
"merged":{"state":"merged","close_at":null,"last_close_at":null},
"reclosed_close_at_unchanged":true
```

Getting the probe to run at all hit two smaller snags first: the tag condition raised `undefined method 'strip' for an instance of Array (NoMethodError)` in `lib/selector/sql.rb`, and saving the `Job` from `rails r` failed with `null value in column "updated_by_id" of relation "jobs" violates not-null constraint`.

## What I thought it was
That every ended ticket has a close time, so one condition on `close_at` would cover both states.

## What it actually was
`Ticket::SetsCloseTime` sets `close_at` and `last_close_at` only when the state type is `closed`. Merging sets the state type `merged`, so a merged ticket never gets either, and the planned condition would have kept merged assistant tickets forever. Also, `close_at` is set once and never moves: a ticket IT reopens and closes again keeps its first close time, so it would be deleted at the next hourly run. (The snags: the selector wants a tag value as a string, not an array, and `UserInfo.current_user_id` does not fill the job's audit columns, so `created_by_id` and `updated_by_id` must be set.)

## How I fixed it
`seed/helpdesk/rails/retention.rb` uses one nested OR condition: tag `noord-assistant`, then either state `closed` with `last_close_at` older than N days, or state `merged` with `updated_at` (the merge time) older than N days. You chose this option in the session; spec 0010 AC-9 and AC-10 still describe close time and need an `/architect` update. A second probe of the real step over six tickets deleted only the old closed one and the old merged one, and the live test `TestLiveRetentionScheduler` passes. Not committed yet.

## What I would tell a team doing this for real
(draft) Probe the helpdesk's own data model before you write a delete rule: "close time" meant less than the spec assumed.

## Evidence
- Error text or screenshot: the probe output above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
