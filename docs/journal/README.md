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
| [0008](0008-samba-hangs-without-its-hostname.md) | 2026-10-18 | Samba starts but never listens unless the hostname is "samba" (again on 2026-10-21: it rules out a StatefulSet) | #6 Noord directory & seeded mess, #17 | ~35 min | post |
| [0009](0009-ldapsearch-cannot-check-ldaps-on-macos.md) | 2026-10-18 | ldapsearch on macOS cannot check our LDAPS cert | #6 Noord directory & seeded mess | ~8 min | post |
| [0010](0010-gitleaks-flags-a-checksum-in-tools-sum.md) | 2026-10-18 | gitleaks flags a module checksum in tools.sum | #6 Noord directory & seeded mess | ~5 min | post |
| [0011](0011-testcontainers-hangs-on-docker-credentials.md) | 2026-10-18 | Live Samba tests hang forever on Docker's credential helper | #7 Entra tenant & sync | ~20 min | post |
| [0012](0012-free-tenant-cannot-create-tenants.md) | 2026-10-18 | A free Azure tenant may not create a second tenant | #7 Entra tenant & sync | ~10 min | post |
| [0013](0013-graph-rejects-the-fictional-phone-range.md) | 2026-10-18 | Graph rejects the fictional phone range the spec picked | #7 Entra tenant & sync | ~35 min | post |
| [0014](0014-mfa-clear-reports-too-early.md) | 2026-10-18 | The MFA clear said done while Graph still listed the phone | #7 Entra tenant & sync | ~10 min | video |
| [0015](0015-graph-reads-lag-behind-writes.md) | 2026-10-18 | Graph's reads lag its writes, so drift takes two syncs to repair | #7 Entra tenant & sync | ~30 min | both |
| [0016](0016-password-reset-needs-a-directory-role.md) | 2026-10-18 | Resetting a password needs a directory role, not just a Graph permission | #7 Entra tenant & sync | ~10 min | post |
| [0017](0017-demo-sign-in-and-blocked-texts.md) | 2026-10-18 | The demo sign in skipped MFA, then Microsoft refused to send the text | #7 Entra tenant & sync | ~40 min | both |
| [0018](0018-tool-app-can-read-the-admins-phones.md) | 2026-10-18 | The tool server app can read the tenant admin's phone methods | #7 Entra tenant & sync | ~0 min | post |
| [0019](0019-zammad-ldap-has-one-base-dn.md) | 2026-10-18 | Zammad's LDAP source has one base DN for users and groups | #8 Ticketing, knowledge base & Slack workspace | ~15 min | post |
| [0020](0020-zammad-agents-can-create-users.md) | 2026-10-18 | Any Zammad agent can create users, so the assistant token can too | #8 Ticketing, knowledge base & Slack workspace | ~10 min | both |
| [0021](0021-zammad-kb-translation-already-exists.md) | 2026-10-18 | The first bootstrap run stopped at the knowledge base | #8 Ticketing, knowledge base & Slack workspace | ~5 min | no |
| [0022](0022-zammad-signin-needs-csrf.md) | 2026-10-18 | Wrong Zammad passwords seemed not to count toward the AD lockout | #8 Ticketing, knowledge base & Slack workspace | ~5 min | no |
| [0023](0023-helpdesk-marker-depends-on-cwd.md) | 2026-10-18 | The helpdesk marker changed with the working directory | #8 Ticketing, knowledge base & Slack workspace | ~5 min | post |
| [0024](0024-zammad-open-tickets-stay-new.md) | 2026-10-18 | Seeded open tickets stayed in Zammad state "new" | #8 Ticketing, knowledge base & Slack workspace | ~5 min | no |
| [0025](0025-zammad-user-search-lags-a-reset.md) | 2026-10-18 | Zammad's user search found nobody right after a reset | #8 Ticketing, knowledge base & Slack workspace | ~10 min | no |
| [0026](0026-kb-content-rows-have-no-dates.md) | 2026-10-18 | Zammad's KB content rows have no dates to backdate | #8 Ticketing, knowledge base & Slack workspace | ~2 min | no |
| [0027](0027-tidy-check-needs-tools-mod-too.md) | 2026-10-18 | tidy-check failed on tools.mod after adding app dependencies | #8 Ticketing, knowledge base & Slack workspace | ~2 min | no |
| [0028](0028-helpdesk-seed-raced-zammad-init.md) | 2026-10-18 | The helpdesk seed raced Zammad's init, so no user was ever searchable | #9 Tool server & risk tiers | ~15 min | post |
| [0029](0029-sdk-tests-pull-an-ambiguous-import.md) | 2026-10-19 | Adding the Anthropic SDK broke go mod tidy with an ambiguous import | #10 Locked out user, end to end | ~10 min | post |
| [0030](0030-local-toolserver-token-too-short.md) | 2026-10-19 | Both binaries refused to start: the local tool server token was too short | #10 Locked out user, end to end | ~3 min | no |
| [0031](0031-ctrl-c-cannot-interrupt-a-turn.md) | 2026-10-19 | Ctrl+C can't leave an interrupted turn for the restart check | #10 Locked out user, end to end | ~2 min | no |
| [0032](0032-agent-image-misses-eval.md) | 2026-10-19 | The agent image failed in CI: the Dockerfile never copied eval | #10 Locked out user, end to end | ~5 min | post |
| [0033](0033-sqlc-guesses-a-nullable-column-wrong.md) | 2026-10-19 | sqlc guessed the type of a nullable ticket id wrong three times | #11 Privileged request refusal & escalation | ~10 min | no |
| [0034](0034-test-fake-refusals-missed-the-code-prefix.md) | 2026-10-19 | The agent's test fake sent refusals without the code the real tool server sends | #11 Privileged request refusal & escalation | ~5 min | post |
| [0035](0035-zammad-merged-tickets-have-no-close-time.md) | 2026-10-19 | Zammad gives merged tickets no close time, and a reclosed ticket keeps its first one | #12 PII redaction & retention | ~20 min | both |
| [0036](0036-samba-ignores-the-password-history-hint.md) | 2026-10-19 | Samba ignores the password history hint on a delegated reset | #13 Password reset links | ~10 min | post |
| [0037](0037-denied-groups-would-block-every-reset.md) | 2026-10-19 | The denied groups list would have refused a reset link to almost everyone | #13 Password reset links | ~10 min | both |
| [0038](0038-samba-keeps-the-old-password-for-an-hour.md) | 2026-10-19 | Samba kept accepting the old password for an hour after a reset | #13 Password reset links | ~15 min | both |
| [0039](0039-a-stale-approval-ran-the-clear-twice.md) | 2026-10-20 | A stale copy of an approval ran the MFA clear a second time | #14 MFA re-registration with approval | ~5 min | post |
| [0040](0040-test-clock-and-store-clock-disagreed.md) | 2026-10-20 | The test clock and the fake store's clock disagreed, so every approval was born expired | #14 MFA re-registration with approval | ~5 min | no |
| [0041](0041-graph-will-not-delete-the-default-method-first.md) | 2026-10-20 | Graph will not delete a user's default MFA method while others remain | #14 MFA re-registration with approval | ~15 min | both |
| [0042](0042-the-slack-test-account-was-a-guest-mapped-to-the-wrong-user.md) | 2026-10-20 | The Slack test account was a guest, and its email mapped to nobody | #14 MFA re-registration with approval | ~15 min | post |
| [0043](0043-the-tick-had-no-source-for-the-approval-timeout.md) | 2026-10-20 | The approval tick had no source for the approval timeout | #14 MFA re-registration with approval | ~5 min | post |
| [0044](0044-pre-push-checks-the-working-tree-not-the-commit.md) | 2026-10-20 | The pre push hook checked the working tree, not the commit being pushed | #4 Coding standards & tooling | ~5 min | post |
| [0045](0045-the-pi-runner-had-no-c-compiler-for-race.md) | 2026-10-20 | The Pi runner had no C compiler, so the race tests could not start | #4 Coding standards & tooling | ~10 min | post |
| [0046](0046-a-1s-audit-timeout-flaked-on-the-pi.md) | 2026-10-20 | A 1s audit write timeout in the tests timed out on the Pi runner | #4 Coding standards & tooling | ~10 min | no |
| [0047](0047-one-pending-approval-was-checked-not-enforced.md) | 2026-10-20 | "One pending approval per account" was checked in code, not enforced by the database | #14 MFA re-registration with approval | ~30 min | post |
| [0048](0048-the-ci-fork-check-failed-open.md) | 2026-10-20 | The CI fork check failed open, so a deleted fork's code would run on the Pi | #4 Coding standards & tooling | ~5 min | post |
| [0049](0049-pi-runners-share-a-buildx-config.md) | 2026-10-20 | The Pi runners shared state and ran out of time, so CI moved to the cluster | #4 Coding standards & tooling | ~2 hours | both |
| [0050](0050-two-branches-took-the-same-journal-number.md) | 2026-10-20 | Two branches took the same journal number, so the merge from main collided | #14 MFA re-registration with approval | ~10 min | no |
| [0051](0051-parallel-test-packages-raced-on-one-image-tag.md) | 2026-10-20 | Parallel test packages raced to build one Samba image tag, so a docs only PR failed CI | #6 Noord directory & seeded mess | ~20 min | post |
| [0052](0052-govulncheck-flags-go-1-27-2.md) | 2026-10-21 | govulncheck flagged the Go 1.27.2 standard library partway through a feature build | #4 Coding standards & tooling | ~15 min | no |
| [0053](0053-elasticsearch-disk-watermark-stalled-zammad-init.md) | 2026-10-21 | A full Docker disk stalled the Zammad reset at the Elasticsearch watermark | #15 Access requests via group owner | ~12 min | post |
| [0054](0054-pgrep-wait-loop-matched-itself.md) | 2026-10-21 | A pgrep wait loop matched itself, so the binaries never started | #15 Access requests via group owner | ~5 min | no |
| [0055](0055-rate-limits-outlived-the-reset-during-live-verify.md) | 2026-10-21 | The per person rate limits outlived the reset and blocked the live verify | #15 Access requests via group owner | ~15 min | post |
| [0056](0056-live-samba-run-outlasted-the-default-test-timeout.md) | 2026-10-21 | The live Samba run outlasted Go's default 10 minute test timeout before any test ran | #15 Access requests via group owner | ~20 min | no |
| [0057](0057-migration-00006-could-not-go-down-and-up-with-access-requests.md) | 2026-10-21 | Migration 00006 could not go down, then up, with access requests in the table | #15 Access requests via group owner | ~10 min | no |
| [0058](0058-read-only-env-and-the-token-budget-ended-a-live-run.md) | 2026-10-21 | READ_ONLY in .env, then the token budget, ended a live run early | #15 Access requests via group owner | ~10 min | post |
| [0059](0059-kb-answers-missed-the-token-budget-target.md) | 2026-10-21 | Every knowledge base answer used nearly the whole token budget | #16 Knowledge base answers | ~15 min | post |
| [0060](0060-zammad-kb-search-is-newest-first-not-relevance.md) | 2026-10-21 | Zammad's KB search returns newest first, so an older conflicting article can drop out | #16 Knowledge base answers | ~30 min | post |
| [0061](0061-ghcr-answers-403-for-a-missing-package.md) | 2026-10-21 | GHCR answers 403, not 404, for a package that does not exist yet | #17 One command GitOps install | ~10 min | no |
| [0062](0062-go-run-hides-the-installers-exit-code.md) | 2026-10-21 | `go run` would hide the installer's exit code 2 | #17 One command GitOps install | ~5 min | no |
| [0063](0063-cnpg-managed-roles-keep-argocd-out-of-sync.md) | 2026-10-21 | CNPG managed roles keep the Cluster OutOfSync in ArgoCD | #17 One command GitOps install | ~15 min | post |
| [0064](0064-a-stray-reset-password-crashed-the-tool-server.md) | 2026-10-21 | A stray reset password crashed the tool server on the first install | #17 One command GitOps install | ~5 min | no |
| [0065](0065-gitleaks-flags-sealed-ciphertext.md) | 2026-10-21 | gitleaks flags the sealed ciphertext the installer commits | #17 One command GitOps install | ~15 min | post |
| [0066](0066-the-sealed-allowlist-missed-dotted-keys.md) | 2026-10-21 | The sealed ciphertext allowlist missed dotted key names | #17 One command GitOps install | ~10 min | no |
| [0067](0067-elasticsearch-cp-a-on-an-emptydir.md) | 2026-10-21 | Elasticsearch's config copy failed on the emptyDir | #17 One command GitOps install | ~10 min | no |
| [0068](0068-statefulset-claim-templates-stay-outofsync.md) | 2026-10-22 | StatefulSet claim templates kept noord-client OutOfSync | #17 One command GitOps install | ~10 min | no |
| [0069](0069-ci-image-builds-starved-etcd.md) | 2026-10-22 | CI image builds starved k3sprox's etcd | #17 One command GitOps install | ~20 min | post |
| [0070](0070-buildkit-session-dies-before-the-push.md) | 2026-10-22 | BuildKit dropped the session before the agent image's push | #17 One command GitOps install | ~45 min | post |
| [0071](0071-ci-ok-stranded-by-a-listener-restart.md) | 2026-10-22 | Lost runners and a listener restart stranded ci-ok on main | #17 One command GitOps install | ~20 min | post |
| [0072](0072-cilium-ipblock-skips-node-addresses.md) | 2026-10-22 | Cilium ignores an ipBlock to a node, so the kube API allow blocked it | #17 One command GitOps install | ~10 min | post |
| [0073](0073-objectstore-endpoint-cannot-be-sealed.md) | 2026-10-22 | The backup endpoint and bucket cannot be sealed: plain ObjectStore fields | #17 One command GitOps install | ~10 min | post |
| [0074](0074-platform-project-synced-after-its-operator.md) | 2026-10-22 | The platform sync waited forever: an operator Application synced before its AppProject | #17 One command GitOps install | ~15 min | post |
| [0075](0075-operator-app-revision-is-a-chart-version.md) | 2026-10-22 | make install hung at platform: an operator Application's revision is its chart version | #17 One command GitOps install | ~20 min | post |
| [0076](0076-zammad-geo-ip-lookup-hangs-without-egress.md) | 2026-10-22 | The helpdesk seed "lost" its token: Zammad's geo IP lookup hung once egress was closed | #17 One command GitOps install | ~40 min | both |
| [0077](0077-resume-stalled-while-argocd-repo-server-restarted.md) | 2026-10-23 | make resume left the agent down: ArgoCD's repo-server kept restarting | #17 One command GitOps install | ~8 min | post |
| [0078](0078-two-firewalls-one-silent-timeout.md) | 2026-10-30 | Reaching the hypervisor API took two firewalls, and both failed as the same silent timeout | #17 One command GitOps install | ~1 hour | post |
| [0079](0079-fresh-helpdesk-seed-blocked-by-its-own-network-policy.md) | 2026-10-31 | The first fresh helpdesk seed under default deny failed: Elasticsearch was closed on both sides | #17 One command GitOps install | ~2 h 15 min | both |
| [0080](0080-smoke-job-refused-in-its-first-second.md) | 2026-10-31 | Every smoke check failed on a healthy cluster: a new pod's first second is refused on stock k3s | #17 One command GitOps install | ~40 min | both |
| [0081](0081-the-fresh-install-proof-run.md) | 2026-10-31 | The fresh install proof: one command, a Slack unlock, a reset link, a restore, and the bill | #17 One command GitOps install | ~1 day | both |

**Story worthy** is `post`, `video`, `both`, or `no`. In week 4, pick the top three for the launch post and the video.
