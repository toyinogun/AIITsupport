# 0060. Zammad's KB search returns newest first, so an older conflicting article can drop out

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #16 Knowledge base answers
**Time lost:** ~30 min (estimate) · **Story worthy:** post

## What I saw
The AC-17 probe of `POST /api/v1/knowledge_bases/search` with the assistant token worked, but the order was not relevance:

```
vpn     [(14, '2026-04-20'), (8, '2025-09-01'), (15, '2025-08-25'), (13, '2021-02-08')]
lockout []
lock    [(2, '2026'), (15, '2025'), (3, '2025'), (1, '2020')]
```

With `KB_SEARCH_LIMIT` 3, "lock" keeps 2, 15, and 3 and drops article 1, the 2020 lockout article that contradicts article 2. In the live run "how long does a lockout last?" was answered correctly (15 minutes), but the conflict with the old article was never seen, so the ticket got no `kb-review` tag.

## What I thought it was
That the spec's "Zammad's relevance order" was what the endpoint returns.

## What it actually was
Zammad's REST search controller pins `order_by: { updated_at: :desc }`, and the query needs every word to match (with a prefix wildcard, so `lock` finds "locked" but `lockout` finds nothing). The same probe found three smaller gaps:
- The KB init read gives no locale code, only a `system_locale_id`, and this Zammad has no locales endpoint for the token. The `en-us` check is built on each hit's portal path (`/help/en-us/...`) instead.
- `GetAnswer` with `?full=true` returns no body; the body needs `?include_contents=<content id>`.
- Zammad's published scope ignores `internal_at`, but spec 0014's filter also requires it unset or later. That drops a published answer that was once internal; nothing in the seed is.

## How I fixed it
Built as specced (Zammad's order, the spec's filter), with the probe results recorded in spec 0014's `verify.md`. The ordering and the `internal_at` rule go to `/architect`. Not committed yet.

## What I would tell a team doing this for real
(draft) Probe the undocumented endpoint with the questions you will actually ask, and check its sort order, before you design rules that depend on what comes back.

## Evidence
- Error text or screenshot: the probe output above; `lib/search_knowledge_base_backend.rb` and `app/controllers/knowledge_base/search_controller.rb` in the Zammad container
- Commit or PR: not committed yet
- Eval run (if any): none (live harness `TestLiveKBAnswers`, 2026-10-21)
- Clip file and timestamp: TODO (you): clip file and timestamp
