# 0026. Zammad's KB content rows have no dates to backdate

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~2 min (estimate) · **Story worthy:** no

## What I saw
Spec 0006 lists the KB translations' content rows among the columns to backdate. Listing the columns inside the container:

```
KnowledgeBase::Answer::Translation::Content: id body
```

## What I thought it was
TODO (you): what did you expect the content table to hold?

## What it actually was
Zammad keeps only the body in that table; the dates live on the answer and its translation.

## How I fixed it
`backdate.rb` sets the answer's `created_at`, `updated_at`, `published_at` and the translation's `created_at`, `updated_at`, `edited_at`, and nothing on content rows. Recorded in the spec's `verify.md` for `/architect` to correct the column list. Not committed yet.

## What I would tell a team doing this for real
(draft) Check the real schema before you write a column list into a spec.

## Evidence
- Error text or screenshot: the column list above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
