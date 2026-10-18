# 0012. A free Azure tenant may not create a second tenant

**Date:** 2026-10-18 · **Week:** 1 · **Feature:** #7 Entra tenant & sync (timeboxed)
**Time lost:** ~10 min (estimate) · **Story worthy:** post

## What I saw
The tenant create form now asks for an Azure subscription and a resource group. After filling both in (`rg-noord-entra`), **Create** failed:

```
Create Tenant
Base-tenant [redacted] does not have a paid license. Hence add-on tenant
creation is not allowed on this base-tenant.
```

## What I thought it was
At first a missing field: the form wanted a subscription and a resource group, so I added an empty resource group.

## What it actually was
A Microsoft rule. The Default Directory that came with the free Azure account is on the Entra ID Free tier, and only a tenant with a paid licence may create more tenants. Spec 0005's plan (the "free Azure account route" to a dedicated tenant) assumed it could.

## How I fixed it
Took option A: the Default Directory itself becomes Noord's tenant, renamed to "Noord Logistics (test)", using its existing `*.onmicrosoft.com` domain. The managed marker fence still keeps the sync and the tool server away from the admin account. Option B (start the P1 trial, then try again) was not tried. AC-1 says "a dedicated tenant for Noord only", so the spec may need a one line update (`/architect`). Not committed yet.

## What I would tell a team doing this for real
(draft) Check the tenant creation rules for the licence you actually hold before you put "create a tenant" on evening one.

## Evidence
- Error text or screenshot: the Create Tenant error above (tenant id redacted)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
