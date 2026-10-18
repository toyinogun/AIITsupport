# 0019. Zammad's LDAP source has one base DN for users and groups

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #8 Ticketing, knowledge base & Slack workspace
**Time lost:** ~15 min (estimate) · **Story worthy:** post

## What I saw
Spec 0006 asks for an LDAP source with a user base `OU=Users,OU=Noord,...` and a separate group base `OU=Groups,OU=Noord,...`. Reading Zammad 7.2.2's own code inside the container, there is no second base:

```
lib/ldap/user.rb:97:        base:     @ldap.base_dn,
lib/ldap/group.rb:39:    # @param base_dn [String] The applied base DN for listing groups. Default is Ldap#base_dn.
```

Both the user search and the group search (which drives the `APP-Helpdesk-Agents` role map) use the one `base_dn` of the source. With the base at `OU=Users`, the role map group is out of reach; with it at `OU=Noord`, the service and admin accounts come along.

## What I thought it was
The spec assumed Zammad's LDAP wizard took a separate group base, like many LDAP clients do. TODO (you): did you expect two bases too?

## What it actually was
A Zammad design limit: one LDAP source, one search base. Nothing to configure.

## How I fixed it
Not fixed in the spec yet; the build paused for `/architect`. The workaround probed and working: base `OU=Noord,DC=ad,DC=noord,DC=example` and user filter `(&(objectCategory=person)(objectClass=user)(department=*))`. Only the `OU=Users` staff carry a department, so the import gave exactly 85 users, Ruben with Agent and Admin, and no `svc-*` or `adm.*` accounts. It lives in `seed/helpdesk/rails/bootstrap.rb`. Not committed yet.

## What I would tell a team doing this for real
(draft) Read the product's LDAP code, not its docs, before you promise an OU layout; one base DN is common in helpdesk tools.

## Evidence
- Error text or screenshot: the two source lines above; import result `{"created":85, ... "failed":0}`
- Commit or PR: not committed yet
- Clip file and timestamp: TODO (you): clip file and timestamp
