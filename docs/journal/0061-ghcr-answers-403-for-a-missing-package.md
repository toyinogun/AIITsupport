# 0061. GHCR answers 403, not 404, for a package that does not exist yet

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
The installer's image preflight (spec 0015, AC-2) checks that all four images exist for the cluster's `image.tag`. Run against k3sprox before CI had ever pushed an image, it failed with an error instead of a finding:

```
check image agent: the registry's token service answered 403
```

## What I thought it was
The anonymous token flow was wrong for GHCR, or GHCR needs a login even for public pulls.

## What it actually was
The flow was right. GHCR's token service refuses an anonymous token with 403 when the package does not exist (or is private), so "missing" never reached the manifest check. Anonymously, missing and private look the same.

## How I fixed it
`internal/install/images.go`: a 401, 403, or 404 from the token service is now `errNoPull`, which `Exists` reports as "image missing", so preflight prints the real finding ("no public agent, toolserver, seed, samba image ... wait for CI on the default branch, or make the packages public") and exits 2. A test fakes the 403. Checked once against a real public GHCR image (`cloudnative-pg/postgresql`). Not committed yet.

## What I would tell a team doing this for real
(draft) Treat a registry's "no" as an answer, not an error: anonymous checks cannot tell missing from private, so say both in the message.

## Evidence
- Error text: above
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
