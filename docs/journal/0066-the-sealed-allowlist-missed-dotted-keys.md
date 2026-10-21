# 0066. The sealed ciphertext allowlist missed dotted key names

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~10 min (estimate) · **Story worthy:** no

## What I saw
`main` stayed red after the demo directory install pushed its seal commit (`6384657`): the `secrets` job failed, everything else passed. Found while running `make check` for build plan task 3:

```
WRN leaks found: 1
Finding:     tls.key: REDACTED
RuleID:      generic-api-key
File:        deploy/clusters/k3sprox/sealed-values.yaml
Commit:      63846570a87e0ecb6ae1b2633bf5e42ffab4c8b5
```

## What I thought it was
The same sealed ciphertext false positive as journal 0065, which the allowlist was meant to cover.

## What it actually was
The 0065 allowlist only matched key names of `[A-Za-z0-9_]`. The Samba Secret's `tls.key` has a dot, so its ciphertext line fell outside the allowlist. The new Zammad Secret keys (`postgres-password`, `redis-password`) have hyphens and would have failed the same way.

## How I fixed it
Widened the key pattern in `.gitleaks.toml` to `[A-Za-z0-9_.-]+`; the path condition and the 300+ character `Ag…` ciphertext condition are unchanged, so a plaintext value still fails. Not committed yet.

## What I would tell a team doing this for real
(draft) Test an allowlist against every key shape the generator can emit, not just the first file it produced.

## Evidence
- Error text or screenshot: above
- Commit or PR: not committed yet (the failing commit is `6384657`)
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
