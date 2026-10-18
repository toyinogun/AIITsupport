# 0021. The first bootstrap run stopped at the knowledge base

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~5 min (estimate) · **Story worthy:** no

## What I saw
The first run of `seed/helpdesk/rails/bootstrap.rb` failed partway, leaving a half bootstrapped Zammad:

```
NOORD_FAILED knowledge base: ActiveRecord::RecordInvalid: Validation failed: Kb locale has already been taken
```

## What I thought it was
That the knowledge base could be made over REST like the categories and answers. It cannot: `kb_locales` is not in the REST allowed params, so the empty KB is made in Rails.

## What it actually was
Creating a `KnowledgeBase` makes a translation for its primary locale by itself (titled "Noord Logistics Knowledge Base"), so creating a second one for the same locale fails the uniqueness check.

## How I fixed it
`bootstrap.rb` now finds or initializes the translation and updates its title. The half run Zammad was finished by hand for the probes and gets replaced by the next `make helpdesk-reset`. Not committed yet.

## What I would tell a team doing this for real
(draft) Make the first run of a seed script against a throwaway instance; a failure halfway is exactly what the reset path is for.

## Evidence
- Error text or screenshot: the line above
- Commit or PR: not committed yet
