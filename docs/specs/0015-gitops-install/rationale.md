# 0015. One command GitOps install: rationale

The decision record behind [index.md](index.md). `/develop` builds from `index.md` and skips this file.

## Context

> ⚠️ Premise note: the #17 Done when line asks that "model inference goes to an EU region endpoint". On 2026-10-21 the first party Anthropic API offered only `global` and `us` for `inference_geo`, with no EU value (basis: the data residency page, see References). You chose to keep the Anthropic API and state the gap rather than add Vertex or Bedrock. That is a defensible scope call, but it means this spec cannot meet that Done when line as written. The right framing is to amend the line to "the EU inference gap is stated" and keep `internal/llm` swappable, so EU inference stays a config and credentials change later. This closes spec 0001's week 1 follow up with a recorded "no".

Feature #17 turns a repo that runs on a laptop into something that installs into a Kubernetes cluster with one command and runs there through GitOps (git is the source of truth, and ArgoCD keeps the cluster matching it). Spec 0001 settled the broad shape: one Helm chart in `deploy/helm/noord`, an ArgoCD app of apps in `deploy/argocd/`, Sealed Secrets, CloudNativePG (CNPG, an operator that runs Postgres in Kubernetes), a Tailscale ingress for admin UIs, GHCR images by git SHA, and `make release`. Eleven later specs then left #17 follow ups: the CNPG roles, the migration Job and a `ScheduledBackup` (0003), Samba as a StatefulSet with its certificate and a seed Job (0004), Entra certificates (0005), the Zammad chart, its Rails seed runner and the Elasticsearch kernel setting (0006), the kill switch ConfigMap and the `/mcp` NetworkPolicy (0007), the `Recreate` agent and its shutdown grace (0008), one retention value for both purges (0010), the reset page on the tailnet with no path logging (0011), and the approval routes kept off any ingress (0012, 0013).

The forces are concrete. **The safety claim** is the product: only the tool server reaches client systems, approvals never pass through the model, and nothing listens on the internet. The install has to keep that true in the network, not only in code. **The repo is public**, so no plaintext secret and no real personal data may land in git, and site details like the tailnet name stay out too. **One command into a fresh cluster** is the week 3 milestone, and it has to be honest: Sealed Secrets can only encrypt for a controller that already runs in the target cluster, so the order of steps is a real design problem, not a detail. **Two kinds of install** coexist: your k3sprox cluster (Cilium, Longhorn, CNPG, Sealed Secrets, cert-manager, the Tailscale operator and ArgoCD already present, its own `root` app in `toyinogun/k3sprox-gitops`), and a bare cluster a stranger or a client brings. **Shared outside services** (one Slack app over Socket Mode, one Entra tenant) cannot serve two installs at once. **The handover**: a single IT generalist inherits this, so every routine action (release, rotate a secret, flip read only, restore) has to be one make target or one documented command.

Not deciding means the eval (#19 to #21) and the handover (#22) have nothing real to run against, and the follow ups from eleven specs stay scattered.

**Compliance scope: GDPR.** The cluster holds the audit log, conversations and approvals (personal data of Noord staff), Postgres backups leave the cluster for an object store, and the model provider sees message content outside the EU. Audit logging is not negotiable and already exists (spec 0003); this spec decides where its backups live and how long they are kept.

## Options considered

### Option 1: `make install` seals and pushes, per cluster folders, an optional demo (chosen)

`make install CLUSTER=<name>` checks prerequisites, installs ArgoCD when it is missing, starts only the platform operators the cluster lacks, waits for the Sealed Secrets controller, seals the cluster's secrets into `deploy/clusters/<name>/secrets/`, commits and pushes them, then applies the bootstrap and waits for a smoke check to pass. Each cluster has its own folder of values and sealed files on main. Samba, Zammad and the seeds are a `demo` switch (on by default) so a real client turns them off.

**Pros**:
- Truly one command on a bare k3s, and everything except one personal data Secret lives in git, encrypted where secret.
- The same command serves k3sprox, the proof VM, and a stranger's fork.
- The demo switch is the honest FDE shape: a client already runs AD and a helpdesk.

**Cons**:
- The command pushes to git, so it needs a clean tree and push rights to the fork it installs from.
- The installer is real code (preflight, sealing, waiting) that needs tests, and installing needs the Go toolchain.
- Sealed blobs for k3sprox sit in a public repo; their safety rests on the controller key never leaking.

### Option 2: plain Secrets straight from `.env`

`make install` creates the Kubernetes Secrets from the local env file with kubectl, nothing written to git, then applies the bootstrap.

**Pros**:
- The simplest true one command; no sealing, no push, no controller ordering.
- Nothing secret is in git in any form.

**Cons**:
- Amends spec 0001 (drops Sealed Secrets) and breaks GitOps for every secret: a cluster rebuilt from git alone has no credentials.
- Rotation and drift become invisible to ArgoCD, and the handover loses "every change is a commit".

### Option 3: the controller as a prerequisite

Keep spec 0001 as written: a fresh cluster needs ArgoCD, a StorageClass and the Sealed Secrets controller first, then `make seal`, a commit, and `kubectl apply -f deploy/argocd/bootstrap.yaml`.

**Pros**:
- No installer code beyond `make seal`; matches spec 0001 word for word.

**Cons**:
- "One command" really means three or four steps with a commit in the middle, which the week 3 milestone and the README claim cannot honestly call one command.

### Option 4: Helm install with no ArgoCD

`helm upgrade --install` of the chart from the laptop, with secrets from `.env`.

**Pros**:
- The fewest moving parts on a fresh cluster.

**Cons**:
- No GitOps: no drift correction, no record of what is deployed, and it contradicts spec 0001 and the scope's "deploy through GitOps".

## Rationale

Option 1 is the only one that meets all three hard requirements at once: one command, secrets never plaintext in git, and GitOps for everything that defines the running system. Option 3 keeps the letter of spec 0001 but not the milestone; Option 2 meets the milestone by giving up the GitOps property the handover depends on ("every deploy is a git commit you can point to", spec 0001). Option 1's cost, an installer that pushes, is contained: it touches only the cluster's own folder, refuses a dirty tree, and reseals only what changed, so a rerun is a no op. The push also makes the stranger story coherent: your fork is your GitOps source, and the bootstrap points at it.

The demo switch answers a question spec 0001 left open: is Samba and Zammad part of "the whole thing"? For the portfolio, yes (a stranger must rebuild Noord); for a client, no (they bring AD and a helpdesk). One switch serves both, and it keeps the agent stack's templates free of mock only assumptions, since everything the demo provides is also a value a client sets.

For EU inference you overrode the recommendation. The recommendation was Vertex AI's EU multi region endpoint, the only EU option whose Go setup could be confirmed; the cost was a GCP project, a service account and a 10% price premium. Keeping the Anthropic API works and is simpler, but it requires the scope's Done when line to change and the data processing note (#22) and README to say plainly that inference is not EU bound. Because `internal/llm` is already a factory over the SDK's clients, adding Vertex later is a provider case and a sealed key, not a redesign.

### The smaller calls

Each was asked in the design conversation unless marked *recommended here*.

| Call | Pick | Runner up |
|---|---|---|
| What the command installs | Agent stack always; `demo` (Samba, Zammad, seeds) on by default | Everything always |
| Where "fresh" is proven | A throwaway one node k3s VM on Proxmox (4 vCPU, 12 GB, 60 GB), deleted after | Local k3d on arm64 |
| How k3sprox picks it up | The command, plus a pointer Application in `k3sprox-gitops/apps/` so `root` owns it | Command only |
| Secrets on a fresh cluster | `make install` seals and pushes | Plain Secrets from the env file |
| Zammad Rails seed in the cluster | A Job in the Zammad image; an init container exports the static `seed` binary; a new in process Runner | A Job that execs into the Zammad pod |
| Egress | Portable NetworkPolicy, default deny both ways; Cilium FQDN rules behind a value, on for k3sprox | Cilium only |
| Releases | CI pushes four images by SHA on main; `make release` commits a chosen SHA per cluster | CI bumps values on every merge |
| ArgoCD on a bare cluster | `make install` installs a pinned chart when missing | Prerequisite |
| Several clusters in one repo | A folder per cluster on main | A branch per cluster |
| Samba state | A Longhorn (or default class) volume | No volume, like local |
| Entra certificate | Tool server app only, here | Both apps |
| EU inference | Anthropic API, gap stated (you overrode Vertex EU) | Vertex AI `eu` |
| Zammad packaging | Official chart 20.0.0, its ECK Elasticsearch off, our Elasticsearch 9.5.5 StatefulSet | Chart plus the ECK operator |
| Postgres backups | Barman Cloud plugin to a Cloudflare R2 bucket in the EU jurisdiction | Nightly `pg_dump` to a volume |
| Zammad backups | None; reseedable demo data | Nightly Zammad dump |
| Secret list | A `secret:"true"` tag in `internal/config`, plus `private:"true"` for site values that are not credentials but stay out of the public repo (added after the cross check) | A hand kept list |
| Two installs, one Slack app and tenant | Pause k3sprox during the proof | A second Slack app with Entra off |
| Install success | ArgoCD Healthy, then a smoke Job | Healthy only |
| CI for deploy files | Lint, template, kubeconform, drift checks | Plus a kind install |
| Fresh cluster without Tailscale | The Tailscale operator as an optional platform app | Ingress class as a value only |
| Uninstall | Everything but backups, typed confirm | Apps only |
| The personal data overlays | One plain Secret applied from the local files, never in git | Seal them |
| *Recommended here:* who runs migrations | `seed migrate` (the ops binary) as a Job before the binaries start; `config.LoadMigrate` as spec 0003 planned | A `toolserver migrate` subcommand, rejected so the tool server image keeps one job |
| *Recommended here:* where the installer lives | `cmd/seed` subcommands (`install`, `seal`, `release`, `pause`, `resume`, `smoke`, `uninstall`, `seal-key-backup`) over a new `internal/install` package on both deny lists | A shell script, rejected because it cannot read the config tags or be unit tested |
| *Recommended here:* the Samba CA | Generated locally by the installer (the `seed certs` code, with cluster SANs); the CA key stays only in `~/.noord/<cluster>/`, the Samba server key is sealed, the public CA cert is a ConfigMap; renewed by `make install` within 60 days of expiry | cert-manager CA Issuer, rejected because three namespaces need the CA and that needs trust-manager, a fourth operator |
| *Recommended here (cross check):* seed steps | Plain Jobs named by a hash of the image tag, inputs and a `seedRun` counter, so they run on change and on resume | ArgoCD PostSync hooks, rejected because they rerun on every self heal |
| *Recommended here (cross check):* bootstrap shape | Two Applications per cluster (`noord-<c>-platform`, `noord-<c>`), applied in order, never with a parameter override | One Application with a `stage` parameter, rejected because `root` on k3sprox would revert the override |
| *Recommended here (cross check):* where the installer pushes | Directly to the branch's upstream (allowed today; `main` is not protected), hooks off for these data only commits; a pull request when a protection rule rejects the push | Always a pull request, rejected because a fresh install would stall on a merge every time |
| *Recommended here (cross check):* how sealed values reach the chart | `sealed-values.yaml` per cluster, rendered by the chart as SealedSecrets with a checksum per workload | A separate directory Application for sealed files, rejected because Helm could not checksum them for rollouts |
| *Recommended here:* Elasticsearch kernel setting | `node.store.allow_mmap: false`, so no `vm.max_map_count` and no privileged container; `noord-client` stays baseline | A privileged sysctl init container, which forces the namespace to privileged |
| *Recommended here:* one chart or several | One chart `deploy/helm/noord` with three components (`agent`, `directory`, `helpdesk`), each rendered by its own ArgoCD Application into its own namespace; Zammad as a conditional dependency | Three charts, rejected because the IT person would tune three values files |
| *Recommended here:* the kill switch under ArgoCD | The ConfigMap is created once and listed in `ignoreDifferences`, so `kubectl` flips survive self heal | Managing it through git only, rejected because spec 0007 promises a one command flip with no rollout |
| *Recommended here:* proving the install worked | New `/readyz` endpoints on both binaries that check their own dependencies, called by the smoke Job | A smoke Job holding the LDAP and Zammad credentials, rejected because only the tool server may hold them |
| *Recommended here:* site private values | Tailnet name, kube context, R2 account and bucket live in the gitignored `secrets.env`; URLs built from them are sealed | Commit them in values, rejected because the tailnet name is not in the public repo today |
| *Recommended here:* backup retention | 14 days, a value | 30 days, rejected because purged conversations would live a full extra retention period in backups |
| *Recommended here:* repo URL in the bootstrap | Derived from `git remote get-url origin` (https form) and the current branch | A value typed by hand |

### Amendment (2026-10-21): the Samba workload

Building task 2 showed AC-10's StatefulSet cannot run the Samba image. Samba listens only when the host name is exactly `samba`, with no error otherwise (journal 0008). A Docker check during the build added one fact: provisioning under a matching name (`samba-0`, or `sambazero`) still stays silent, so it is not a mismatch between the provisioned name and the host name. The root cause is still unknown. A StatefulSet always names its pod `<name>-<ordinal>` and sets the host name to match, so its pod can never be `samba`.

| Option | Verdict |
|---|---|
| A one replica Deployment, `Recreate`, `hostname: samba`, its own PVC | **Chosen.** A Deployment honors `hostname` in the pod spec. `Recreate` plus a ReadWriteOnce volume means two pods never hold the domain at once. Nothing needs a StatefulSet's stable pod identity: there is one replica, and clients use the Service name. |
| A StatefulSet, with the entrypoint provisioning under the pod name | Rejected: tested, Samba stays silent under `samba-0`. |
| A StatefulSet whose entrypoint sets the host name at boot | Rejected: `sethostname` needs `CAP_SYS_ADMIN`, which the baseline Pod Security level refuses and spec 0004 already ruled out. |
| Make Samba listen under any host name | Not now: two attempts never found the cause, and Noord's demo needs only one name. Worth retrying if a real client ever runs our image under another name. |

Smaller calls rode along, most of them from a cross check on a different model (2026-10-21), all applied:

- The PVC holds `/etc/samba` as well as `/var/lib/samba`, because provisioning writes `smb.conf` into the container's filesystem, which a new pod would lose.
- The entrypoint copies the certificates in on every boot, not only on first boot, so the `certs` step's renewal within 60 days actually reaches a running domain.
- Provisioning becomes crash safe with a completion marker. In Compose a half provisioned container was thrown away; on a volume, a pod killed between provisioning and delegation would otherwise come back serving a domain with no `svc-itsupport` and no delegation, silently.
- The PVC shares wave 1 with the Deployment. A StatefulSet's `volumeClaimTemplates` avoided this for free; a standalone PVC in an earlier wave would sit Pending on a StorageClass that binds on first use and stall the sync.
- `Prune=false` on the PVC was considered and dropped: turning demo off deletes the whole `noord-directory` Application, which is not a prune, and the demo domain is reseedable anyway.
- A cluster directory reset is a runbook procedure (AC-28), not a new make target, and it is the only way to change a Samba account password, since those are written into the domain at provisioning.
- Task 2 must confirm LDAPS answers inside the cluster, because "only `samba` works" is proven in Docker only.

### Landscape check (2026-10-21, cheap model, once)

- **EU inference.** First party API `inference_geo`: `global` (default) or `us` only, Claude 4.6 and later, `us` at 1.1x. Vertex AI: an `eu` multi region endpoint for current models, regional endpoints only up to Sonnet 4.6, 10% over global; Go: `vertex.WithGoogleAuth(ctx, "eu", projectID)`. Bedrock: `eu.` geo profiles in EU regions, from search snippets only; the Go `bedrock` package exists but its constructor was not confirmed.
- **Zammad chart 20.0.0**, appVersion 7.2.2-0012 (the same Zammad as the local Compose, 7.2.2). Dependencies: `eck-elasticsearch` 0.20.0 (needs the ECK operator and its CRDs cluster wide), `rustfs` 1.0.3, and `memcached`, `postgres`, `redis` from `oci://ghcr.io/cloudpirates-io/helm-charts`. No Bitnami dependency, so the Bitnami catalog change does not apply. The `vm.max_map_count` option and the external database keys were not confirmed.
- **CNPG 1.30** (k3sprox runs 1.30.1): the in tree `barmanObjectStore` is deprecated since 1.26. The Barman Cloud plugin (0.15.1) uses an `ObjectStore` (`barmancloud.cnpg.io/v1`), the Cluster's `spec.plugins` with `isWALArchiver: true`, and `ScheduledBackup` `method: plugin`. From prior knowledge, not confirmed in this check: the plugin's install manifest needs cert-manager.
- **Sealed Secrets**: keys are Secrets labelled `sealedsecrets.bitnami.com/sealed-secrets-key`; a restore is applying the key and restarting the controller; keys renew every 30 days by default. Current version not confirmed.
- **ArgoCD multiple sources** are documented, with no stated stability; this design avoids them by wrapping Zammad as a chart dependency.
- **k3sprox, read only**: k3s v1.36.5 on four amd64 nodes (three workers at about 11 GB), Cilium, Longhorn (default class), CNPG 1.30.1, Sealed Secrets in `kube-system`, cert-manager, the Tailscale operator with a `tailscale` ingress class, ArgoCD with a `root` app over `k3sprox-gitops/apps`, ArgoCD Image Updater. No VolumeSnapshotClass, no Barman plugin, no ECK, and no working CNPG backup on any cluster yet.
- **Agent Skills and MCP**: no official skill for Helm, ArgoCD, Sealed Secrets or CNPG. Credible community or vendor ones: `tailscale/tailscale-skill`, `wshobson/agents` (`helm-chart-scaffolding`, `k8s-security-policies`, `gitops-workflow`), `mmontes11/skills` (`kubeseal`). MCP: `argoproj-labs/mcp-for-argocd` (read only mode available). You installed none for now and asked to note the MCP server for later.

Full notes: `docs/.agent-cache/research/gitops-install.md` (gitignored).

### Cross check (2026-10-21, a different model, read only)

Verdict on the first draft: conditionally sound, not buildable without invention. It found 14 decision gaps and 13 soundness issues, and you chose to apply every recommended fix. The main ones: how the chart sees sealed files (now `sealed-values.yaml`); a dozen required settings with no named source (now the *Value sourcing* and *Secrets per workload* tables, and the `private` tag); the Zammad subcharts rendering random plaintext Secrets (now `existingSecret`); seed order across two Applications and hooks rerunning on self heal (now waves and hashed Jobs); the `stage` override fighting `root` (now two Applications); NetworkPolicy holes for the Tailscale proxies, the CNPG operator, the kube API and the Barman plugin; Tailscale host and backup path collisions between two clusters (now per cluster names); the sealing key renewing under a one time backup; a restore never exercised (now AC-29); and uninstall leaving namespaces and PVCs behind. One claim was checked by hand: `main` has no branch protection today, so direct pushes work.

## References

**Project sources**:
- `AGENTS.md`: the credential boundary rule, one setting declared once in `internal/config`, the public repo rule, the CI runner rule.
- Spec 0001 (stack): Helm, ArgoCD app of apps, Sealed Secrets, CNPG, Tailscale ingress, GHCR, `make release`, the namespaces.
- Specs 0003, 0004, 0005, 0006, 0007, 0008, 0010, 0011, 0012, 0013: their #17 follow ups, listed in Context.
- `docs/runbooks/ci-runner.md`: the k3sprox GitOps repo and the `runs-on` rule.
- `seed/helpdesk/rails/bootstrap.rb`: Zammad's LDAP host and CA, and the token value the seed installs.
- The installed `senior-kubernetes-engineer` skill.

**Practices & standards**:
- GitOps (the cluster converges on git; every change is a commit).
- Least privilege networking (default deny ingress and egress, explicit allows).
- Kubernetes Pod Security Standards (restricted, baseline).
- Backups that leave the failure domain (off site, point in time recovery), with retention no longer than the data's own retention.
- Secrets encrypted at rest in git, with the decryption key backed up outside the cluster.

**Links** (web verified in the landscape check):
- Claude data residency (`inference_geo`): https://platform.claude.com/docs/en/manage-claude/data-residency
- Claude on Vertex AI: https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai
- Zammad Helm chart dependencies: https://raw.githubusercontent.com/zammad/zammad-helm/main/zammad/Chart.yaml
- CNPG Barman Cloud plugin migration: https://cloudnative-pg.io/plugin-barman-cloud/docs/migration/
- Sealed Secrets: https://github.com/bitnami-labs/sealed-secrets
- ArgoCD multiple sources: https://argo-cd.readthedocs.io/en/stable/user-guide/multiple_sources/
- Argo CD MCP server: https://github.com/argoproj-labs/mcp-for-argocd
