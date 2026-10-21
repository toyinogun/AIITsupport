# 0065. gitleaks flags the sealed ciphertext the installer commits

**Date:** 2026-10-21 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~15 min (estimate) · **Story worthy:** post

## What I saw
CI on `main` failed after the first live install pushed its seal commit (`132553e`). The `secrets` job, gitleaks over the full history:

```
WRN leaks found: 8
```

A local scan with `--redact` showed all eight as `generic-api-key` in `deploy/clusters/k3sprox/sealed-values.yaml`, one per sealed value (entropy about 5.9).

## What I thought it was
Sealed ciphertext tripping the generic high entropy rule, not a real leak. True, but it was also a gap in the build: the installer commits with the git hooks off (spec 0015, AC-5), so the pre-commit gitleaks never saw the file, and CI was the first place it was scanned.

## What it actually was
Every line of `sealed-values.yaml` is `KEY: <hundreds of base64 characters>`, exactly what `generic-api-key` looks for. The ciphertext is public by design: only k3sprox's Sealed Secrets controller can open it.

## How I fixed it
An allowlist in `.gitleaks.toml` with two conditions joined by AND: the path is a cluster's `sealed-values.yaml`, and the line is one key holding kubeseal output (starts `Ag`, 300 or more base64 characters). Checked that history scans clean, and that a plaintext Slack token planted in a `sealed-values.yaml` is still reported, the same as anywhere else. Committed on `fix/cnpg-role-drift` (PR #24).

## What I would tell a team doing this for real
(draft) If a tool commits on your behalf with hooks off, run its output through your secret scanner before the first real run, not after CI goes red on main.

## Evidence
- Error text: above
- Commit or PR: TODO (you): the merged PR for fix/cnpg-role-drift
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
