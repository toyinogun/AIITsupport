# 0073. The backup endpoint and bucket cannot be sealed: the ObjectStore takes them only as plain fields

**Date:** 2026-10-22 · **Week:** 1 · **Feature:** #17 One command GitOps install
**Time lost:** ~10 min (estimate), caught before anything was built on it · **Story worthy:** post

## What I saw
Spec 0015 marks `BACKUP_S3_ENDPOINT` and `BACKUP_S3_BUCKET` as private: kept in the gitignored `secrets.env`, sealed, never committed. Rendering the Barman Cloud plugin's chart (0.8.1, plugin v0.15.1) and reading its `ObjectStore` CRD showed `configuration.destinationPath` and `configuration.endpointURL` as plain strings; only `s3Credentials` (the key id, the secret, and the region) are Secret references.

## What I thought it was
TODO (you): what did you first think when the spec's "private" met the CRD?

## What it actually was
A GitOps object cannot read a plain field from a Secret, so a private endpoint would have to live outside git (applied by the installer, like `noord-seed-overlays`) or be committed. The Cilium FQDN rule for backups needs the same host in git too.

## How I fixed it
Your call: commit them as cluster values (`backup.endpoint`, `backup.bucket` in `values.yaml`). They are not credentials, though the R2 endpoint does carry the Cloudflare account id. The token and region stay sealed in `noord-backup`, and the two settings left the `Install` struct, `.env.example`, and `_template/secrets.env`. Spec wording for `/architect` to update (the Value sourcing and Secrets per workload rows). Not committed yet.

## What I would tell a team doing this for real
(draft) Before you call a value "private", check that every object that needs it can read it from a Secret; a CRD's plain field forces it into git.

## Evidence
- Error text or screenshot: the CRD's `configuration` keys from `helm template plugin-barman-cloud --include-crds`: `destinationPath`, `endpointURL` (strings), `s3Credentials` (Secret references)
- Commit or PR: not committed yet
- Eval run (if any): none
- Clip file and timestamp: TODO (you): clip file and timestamp
