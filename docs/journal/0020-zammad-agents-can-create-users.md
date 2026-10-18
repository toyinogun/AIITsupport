# 0020. Any Zammad agent can create users, so the assistant token can too

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~10 min (estimate) · **Story worthy:** both

## What I saw
AC-6 of spec 0006 says the `noord-assistant` token (role permissions `ticket.agent` and `knowledge_base.reader`) gets 403 on `POST /api/v1/users`. The probe:

```
GET /api/v1/settings -> 403
POST /api/v1/users -> 201
POST /api/v1/knowledge_bases/1/categories -> 403
```

The token created a user.

## What I thought it was
That the token's own permission list (`ticket.agent`, `knowledge_base.reader`) would keep it away from user admin. TODO (you): what did you expect?

## What it actually was
Zammad's policy grants user search, create, and update to agents, so they can add a new customer while raising a ticket:

```
app/policies/controllers/users_controller_policy.rb:
  permit! %i[search history create update], to: ['ticket.agent', 'admin.user']
```

There is no setting to turn it off, and dropping `ticket.agent` would also drop ticket work.

## How I fixed it
Not fixed; AC-6 cannot hold as written. The build paused for `/architect` to change the security model (for example: the tool server never exposes a user write, and AC-6 tests that instead). The probe user and ticket lived in a throwaway Zammad that the next `make helpdesk-reset` removes. Not committed yet.

## What I would tell a team doing this for real
(draft) A least privilege role in the client's SaaS is only as small as its built in policies; probe every "403" your design depends on before you build on it.

## Evidence
- Error text or screenshot: the probe output above
- Commit or PR: not committed yet
- Clip file and timestamp: TODO (you): clip file and timestamp
