# Build journal

Everything that goes wrong while building the Noord Logistics agent, written down the moment it happens. This is the raw material for the LinkedIn post and the "what broke" part of the video, so capture it while the details are fresh, not from memory in week 4.

## How to use it

- **One file per incident:** copy [`_template.md`](_template.md) to `NNNN-short-name.md` (for example `0003-entra-sync-stuck.md`) and fill it in the same evening.
- **Log small things too.** A 20 minute detour is still a data point. You can drop it later; you cannot recover it later.
- **Grab evidence while you can:** the error text, a screenshot, the commit that fixed it, the eval run id. Start a screen recording whenever something looks like it is about to go sideways.
- **Eval failures count.** Each failure category from eval v1 gets an entry, fixed or not.
- **Add a row to the index** below so week 4 is a sorting job, not a writing job.

## Predictions (fill in before week 1)

Write down what you expect to break before you start. Comparing these with the real index makes a strong beat in the post ("I thought X would be the hard part. It was Y.").

1.
2.
3.

## Index

| # | Date | Title | Feature (scope #) | Time lost | Story worthy |
|---|------|-------|-------------------|-----------|--------------|
| [0001](0001-sqlc-refuses-empty-scaffold.md) | 2026-10-17 | sqlc refuses to generate on an empty scaffold | #3 Stack & architecture | ~5 min | no |
| [0002](0002-dev-ports-already-taken.md) | 2026-10-17 | Local dev ports already taken on the laptop | #3 Stack & architecture | ~3 min | no |
| [0003](0003-go-tool-missing-go-sum.md) | 2026-10-17 | go.sum entries missing after adding goose as a tool | #3 Stack & architecture | ~2 min | no |
| [0004](0004-gitleaks-build-clash-in-tools-mod.md) | 2026-10-17 | gitleaks won't build next to golangci-lint in one tools.mod | #4 Coding standards & tooling | ~5 min | post |
| [0005](0005-govulncheck-flags-go-toolchain.md) | 2026-10-17 | govulncheck fails the first run on the Go toolchain itself | #4 Coding standards & tooling | ~5 min | post |
| [0006](0006-setup-go-ignores-toolchain-line.md) | 2026-10-17 | CI installs the go line's Go, not the toolchain line's | #4 Coding standards & tooling | ~5 min | no |
| [0007](0007-edited-migration-breaks-local-down.md) | 2026-10-17 | Editing a migration that already ran locally breaks goose down | #5 Data model | ~5 min | no |
| [0008](0008-samba-hangs-without-its-hostname.md) | 2026-10-18 | Samba starts but never listens unless the hostname is "samba" | #6 Noord directory & seeded mess | ~10 min | post |
| [0009](0009-ldapsearch-cannot-check-ldaps-on-macos.md) | 2026-10-18 | ldapsearch on macOS cannot check our LDAPS cert | #6 Noord directory & seeded mess | ~8 min | post |
| [0010](0010-gitleaks-flags-a-checksum-in-tools-sum.md) | 2026-10-18 | gitleaks flags a module checksum in tools.sum | #6 Noord directory & seeded mess | ~5 min | post |
| [0011](0011-testcontainers-hangs-on-docker-credentials.md) | 2026-10-18 | Live Samba tests hang forever on Docker's credential helper | #7 Entra tenant & sync | ~20 min | post |
| [0012](0012-free-tenant-cannot-create-tenants.md) | 2026-10-18 | A free Azure tenant may not create a second tenant | #7 Entra tenant & sync | ~10 min | post |
| [0013](0013-graph-rejects-the-fictional-phone-range.md) | 2026-10-18 | Graph rejects the fictional phone range the spec picked | #7 Entra tenant & sync | ~35 min | post |
| [0014](0014-mfa-clear-reports-too-early.md) | 2026-10-18 | The MFA clear said done while Graph still listed the phone | #7 Entra tenant & sync | ~10 min | video |
| [0015](0015-graph-reads-lag-behind-writes.md) | 2026-10-18 | Graph's reads lag its writes, so drift takes two syncs to repair | #7 Entra tenant & sync | ~30 min | both |
| [0016](0016-password-reset-needs-a-directory-role.md) | 2026-10-18 | Resetting a password needs a directory role, not just a Graph permission | #7 Entra tenant & sync | ~10 min | post |
| [0017](0017-demo-sign-in-and-blocked-texts.md) | 2026-10-18 | The demo sign in skipped MFA, then Microsoft refused to send the text | #7 Entra tenant & sync | ~40 min | both |

**Story worthy** is `post`, `video`, `both`, or `no`. In week 4, pick the top three for the launch post and the video.
