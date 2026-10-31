# 0015. One command GitOps install: `make install` seals and pushes, a folder per cluster, and an optional Noord demo

**Date**: 2026-10-21
**Status**: Accepted

## Summary

This spec decides how the whole system gets into a Kubernetes cluster with one command and then stays there through git. `make install CLUSTER=<name>` checks the cluster, adds ArgoCD and any missing operators, encrypts that cluster's secrets into its own folder in the repo, commits and pushes them, then lets ArgoCD (the tool that keeps the cluster matching git) install everything and finishes with a smoke check. Samba AD, Zammad and the seed data are a `demo` switch, on by default, so a real client can turn them off and point at their own systems. Model calls stay on the Anthropic API, which has no EU region, and the docs say so plainly.

## Rationale

Reasoning, the options weighed, the smaller calls with their runners up, the landscape check, and the cross check: see [rationale.md](rationale.md).

## Requirements

**User stories**:
- As you (the FDE), I want one command that installs everything into a bare k3s cluster, so the week 3 milestone and the README claim are literally true.
- As Noord's IT person, I want every routine action (release, rotate a secret, switch to read only, restore, pause, uninstall) to be one make target or one documented command, so I can run the agent alone after handover.
- As a stranger reading the public repo, I want to fork it, fill one env file, and rebuild Noord in my own cluster, with no secret of yours and no real personal data anywhere in the repo.
- As the person accountable for the safety claim, I want the network itself to keep the agent away from client systems and keep every admin surface off the internet.

**Acceptance criteria**:

*Install*
- **AC-1**: On a bare one node k3s cluster with only its default StorageClass, `make install CLUSTER=<name>` run from a clone whose `deploy/clusters/<name>/secrets.env` is filled (and the two optional seed overlays present) finishes with exit 0 in at most `INSTALL_TIMEOUT` (default `30m`): every ArgoCD Application of the install is Synced and Healthy, every `SealedSecret` reports its `Synced` condition true, the smoke check (AC-17) passes, and it prints the Zammad and reset page URLs and the next step (flip the kill switch, AC-9). Nothing else was run on the cluster before it.
- **AC-2**: Before changing anything, the installer stops with a message naming the problem (never a value) and exit 2 when: `deploy/clusters/<name>/` does not exist; the current kube context differs from `KUBE_CONTEXT` in `secrets.env`; there is no default StorageClass; `kubectl`, `helm`, `kubeseal` or `git` is missing; the git tree has changes outside `deploy/clusters/<name>/`; the branch has no upstream or is behind it; a setting the enabled components require is missing from `secrets.env` or still holds the template's `CHANGE_ME`; any of the four images for the cluster's `image.tag` is missing from the registry; or, with `demo.enabled`, no node has at least `demo.minFreeMemory` (default `6Gi`) of allocatable memory left after current requests.
- **AC-3**: When the `argocd` namespace has no `argocd-server`, the installer installs the argo-cd Helm chart at the version pinned in `deploy/argocd/values.yaml` and waits for it. When ArgoCD exists it is not touched (no upgrade, no config change).
- **AC-4**: `platform.*` values in the cluster folder turn on, per cluster, the Sealed Secrets controller (installed with key renewal off, `--key-renew-period=0`), cert-manager, the CNPG operator, the Barman Cloud plugin, and the Tailscale operator, each a pinned chart rendered by the Application `noord-<cluster>-platform` in sync waves: Sealed Secrets and cert-manager at minus 30, CNPG at minus 20, the Barman plugin at minus 10, Tailscale at minus 10. k3sprox enables only `barmanPlugin`; the proof VM enables all five. `platform.sealedSecrets.namespace` and `.name` (k3sprox: `kube-system`, `sealed-secrets-controller`) tell `kubeseal` where the controller is, whether we installed it or not. The installer applies `noord-<cluster>-platform` first, waits until every enabled operator is Available, and only then seals (AC-5) and applies `noord-<cluster>`.
- **AC-5**: Sealing. The set of sealed values is every `internal/config` field tagged `secret:"true"` or `private:"true"` that the enabled workloads read, plus the deploy only secrets (AC-6). A generated value (the database role passwords, `TOOLSERVER_TOKEN`, `ZAMMAD_TOKEN`, the Samba and seed account passwords, `SEED_USER_PASSWORD`, `SEED_ENTRA_DEMO_PASSWORD`, the Zammad chart's database and Redis passwords) is created once with `crypto/rand` and written into `secrets.env` when absent; when `secrets.env` lacks it but the live Secret exists, the live value is written back instead. A value is resealed only when it differs from the live Secret in the cluster or that Secret is missing (sealing is not deterministic, so comparing ciphertext would always differ), so a rerun with unchanged inputs makes no commit. Sealed values are written to `deploy/clusters/<name>/sealed-values.yaml` (one block per Secret, each sealed for its own name and namespace, strict scope), which the chart renders as `SealedSecret` objects. The installer commits only `deploy/clusters/<name>/` with `chore(deploy): seal secrets for <name>`, with git hooks off for that commit (`LEFTHOOK=0`), and pushes to the branch's upstream. A rejected push stops the install before anything is applied (exit 1); when the rejection is a branch protection rule, it pushes `deploy/<name>-<UTC timestamp>` instead, opens a pull request with `gh`, prints its URL and exits 3, and a rerun after the merge continues.
- **AC-6**: No plaintext secret is in git. Under `deploy/`, the only Secret material is sealed values; `deploy/clusters/*/secrets.env` is gitignored; gitleaks passes. Deploy only secrets (read by no binary): the CNPG role credentials, the Zammad chart's database and Redis passwords (passed to the chart through its `existingSecret` options, so the chart renders no Secret of its own), `BACKUP_S3_ACCESS_KEY_ID`, `BACKUP_S3_SECRET_ACCESS_KEY`, `TS_OAUTH_CLIENT_ID`, `TS_OAUTH_CLIENT_SECRET`, and the Samba server key. The Samba CA's private key never enters the cluster or the repo: it stays in `~/.noord/<name>/ca.key` (mode 0600). The one exception to GitOps is `noord-seed-overlays` in `noord-directory`: created server side from `seed/directory/overlay.local.yaml` and `seed/entra/demo.local.yaml` with `kubectl create secret --dry-run=client -o yaml | kubectl apply -f -` piped in memory, never written to disk; when the files are absent the installer prints one notice and the seed runs without overlays.
- **AC-7**: A Go test fails, in either direction, when the keys the chart puts in a workload's Secret differ from the `secret:"true"` and `private:"true"` fields that workload's loader reads (the workloads and keys in *Secrets per workload*). `Names()` and the `.env.example` drift test also cover the new `Install` settings, and a second drift test checks `deploy/clusters/_template/secrets.env` against the same names.
- **AC-8**: Running `make install CLUSTER=<name>` again on an installed cluster with unchanged inputs makes no commit, starts or restarts no pod (no workload pod, no seed Job, no Zammad init Job), and ends with the smoke check passing. Each workload's pod template carries a checksum of its own sealed block, so a resealed value rolls exactly the workloads that read it and no others.

*What gets installed*
- **AC-9**: `noord-agent` holds: a CNPG `Cluster` `noord-pg` (PostgreSQL 17, pinned image, one instance, `postgres.storage.size` value, the cluster's default StorageClass unless `postgres.storageClass` is set), database `noord` owned by `noord_owner`, and `managed.roles` for `noord_agent`, `noord_toolserver`, `noord_eval` from sealed basic auth Secrets; a migration Job (`seed migrate`, sync wave 1, `backoffLimit` 6) that waits up to `MIGRATE_WAIT_TIMEOUT` until the database accepts `noord_owner` and the three managed roles exist in `pg_roles`, then applies the embedded goose migrations; the tool server Deployment (one replica, wave 2); the agent Deployment (one replica, `strategy: Recreate`, `terminationGracePeriodSeconds` 40, above `AGENT_SHUTDOWN_GRACE`'s 25s, wave 2); and the ConfigMap `noord-kill-switch`, mounted as a whole directory (never `subPath`, which kubelet does not refresh) with `KILL_SWITCH_FILE` pointing into it. ArgoCD creates the kill switch once at `read_only` and lists its `data` in `ignoreDifferences` with `RespectIgnoreDifferences=true`, so a `kubectl` flip is never reverted by self heal; it takes effect within about a minute (kubelet's ConfigMap sync), on the next write call after that. Database URLs use `sslmode=verify-full` with the CNPG CA mounted.
- **AC-10**: With `demo.enabled` true, `noord-directory` (Application wave 0) holds Samba as a Deployment `samba`: one replica, `strategy: Recreate`, and `hostname: samba`, because Samba listens only under that host name (journal 0008) and a StatefulSet always names its pod `samba-0`. The entrypoint keeps its fixed `samba` host name check, with no setting to change it. A PersistentVolumeClaim `samba-data` (the default StorageClass unless `directory.samba.storageClass` is set) holds both `/var/lib/samba` and `/etc/samba` as two subpaths, so a new pod keeps the domain and its `smb.conf`. The PVC, the Deployment and the ClusterIP Service `samba` (port 636, selecting `app.kubernetes.io/name: samba`, the label AC-14's allows select on) are all sync wave 1, so a cluster whose StorageClass binds on first use never waits on a PVC. The pod runs the registry's Samba image with the sealed server certificate, whose SANs include `samba.noord-directory.svc.cluster.local` and which is valid 2 years. The entrypoint copies the certificates in on every boot, and the `noord-samba` checksum on the pod template restarts the pod when the sealed pair changes, so a renewed certificate takes effect after a short LDAP outage (`Recreate`). Provisioning is crash safe: the entrypoint writes a marker file in `/var/lib/samba` only after provisioning, the skeleton and the delegation all succeed, and a boot that finds the domain without the marker wipes `/var/lib/samba` and `/etc/samba` and provisions again, so a pod killed halfway never serves a domain without `svc-itsupport` or its delegation. The Samba account passwords take effect only when the domain is provisioned; rotating one needs a directory reset (AC-28). Turning `demo.enabled` off removes the `noord-directory` Application, and its finalizer deletes `samba-data` with it; the demo directory is reseedable (Data model sketch). `noord-client` (Application wave 1) holds the Zammad chart 20.0.0 as a dependency of our chart (its Postgres, Redis and Memcached on with `existingSecret` passwords, its ECK Elasticsearch and RustFS off, the Zammad chart vendored in our chart's `charts/` so ArgoCD fetches nothing, its Helm test hooks skipped with `skipTests`) plus our Elasticsearch 9.5.5 StatefulSet (one node, `node.store.allow_mmap: false`, `xpack.security.enabled: false` since only Zammad's pods can reach it, a 1 GiB heap, a volume). The seed steps are plain Jobs (not sync hooks), each named `seed-<step>-<hash>`, where the hash covers the seed image tag, the seed input files and the values that step reads, plus `seedRun` (AC-20); so a step runs once per change and never on a self heal. Zammad's own init Job (migrations and the search index, the chart's `zammad-init-1`, whose name never changes under ArgoCD) is an ArgoCD Sync hook with `BeforeHookCreation`: it runs on each sync of `noord-client`, which happens only when that Application's inputs change or drift, never on an unchanged rerun (AC-8). It is safe to repeat. `fqdn` and `http_type` are Zammad settings the seed sets from `ZAMMAD_PUBLIC_URL` on the fresh seed (AC-11). Order: `seed directory` (its "already seeded" outcome exits 0), then `seed entra sync` (skipped when `SEED_GRAPH_*` is unset), both in `noord-directory`; then `seed helpdesk` in `noord-client`, which first waits up to `SEED_ZAMMAD_WAIT_TIMEOUT` for a bind as `svc-zammad` to succeed. With `demo.enabled` false none of this renders, and `LDAP_URL`, `LDAP_CA_CERT`, `ZAMMAD_URL` and `ZAMMAD_PUBLIC_URL` come from `secrets.env`.
- **AC-11**: The helpdesk seed runs in a Job on the same Zammad image the chart runs, with the same database, Redis and Memcached environment as the chart's Rails pods: a named template in our chart produces that environment from the same values, and a chart test compares the environment variable names of the rendered Job with those of the chart's rendered `railsserver` container. An init container from the seed image runs `seed export /shared`, which copies the seed executable, the `seed/helpdesk/` tree, and `seed/directory/noord.yaml` (the user list the seed checks the LDAP import against, and an input to the helpdesk marker) into a shared volume (no shell needed). The main container runs `/shared/seed helpdesk` with `SEED_ZAMMAD_RUNNER=local`, a new Runner that runs `bundle exec rails r <step>` in process with the same arguments, environment and `SEED_ZAMMAD_WAIT_TIMEOUT` as the Compose Runner; the Job's own secrets are left out of the Rails process's environment, and the Job has no retries (a half seeded Zammad only answers `needs_reset`). `bootstrap.rb` sets Zammad's `fqdn` and `http_type` from `ZAMMAD_PUBLIC_URL`, which the seed requires with the `local` Runner. `bootstrap.rb` reads the LDAP host from `SEED_ZAMMAD_LDAP_HOST` (default `samba`; in the cluster `samba.noord-directory.svc.cluster.local`) and the CA from the mounted `noord-ca` ConfigMap; the Zammad scheduler mounts the same ConfigMap where the chart's extra volume options allow.
- **AC-12**: Namespaces are rendered as `Namespace` objects by our chart (not `CreateNamespace`) with Pod Security labels: `noord-agent` `restricted`, `noord-directory` `baseline`, `noord-client` `baseline`. Every pod our chart templates runs as non root with a read only root filesystem (writable `emptyDir` where needed), drops all capabilities, and uses `seccompProfile: RuntimeDefault`, except where a baseline namespace's image needs root (Samba, the Zammad image). Every container has requests and limits from values (defaults in *Resources*).
- **AC-13**: `TICKET_RETENTION_DAYS` is one chart value that reaches both the agent and the helpdesk seed Job, so the two purges cannot drift (spec 0010).

*Network and exposure*
- **AC-14**: Each of the three namespaces has a default deny NetworkPolicy for ingress and egress. Kubelet probes are not affected (both Cilium and k3s's built in policy controller allow node to pod traffic), and the runbook says so. The allows are exactly:
  - DNS (UDP and TCP 53) to kube-dns in `kube-system` from every pod.
  - Agent to tool server 8080; smoke Job to tool server 8080 and agent 8080.
  - Tool server to Samba 636 and to the Zammad web Service; the seed Jobs to Samba 636 and to Zammad's web Service, Postgres, Redis and Memcached (the helpdesk Job runs Rails), and the helpdesk Job also to Elasticsearch on 9200, because its Rails indexes every ticket it writes. That is an egress rule aimed at the elasticsearch pods by name, plus an ingress allow on Elasticsearch's own policy, since Elasticsearch's pod does not carry the Zammad instance label. Under default deny a connection needs an allow on both ends, and each rule must select the right pods; Zammad's pods to Samba 636 and to their own Postgres, Redis, Memcached and Elasticsearch.
  - The Tailscale proxy pods (namespace `tailscale.proxyNamespace`, default `tailscale`) to the Zammad web Service and to the tool server's reset port 8081 only.
  - To `noord-pg` 5432: the agent, the tool server, the migration Job and the smoke Job. The CNPG operator (namespace `platform.cnpg.namespace`, default `cnpg-system`) to the instance on 5432 and 8000. The instance to the kube API (`cluster.apiServerCIDR`, 6443) and to the Barman plugin (in the CNPG operator namespace, 9090).
  - TCP 443 to addresses outside `cluster.podCIDR`, `cluster.serviceCIDR` and `cluster.nodeCIDR` for the agent, the tool server, the `seed entra sync` Job and the CNPG instance (backups).
  - With `cilium.fqdnEgress` true (on for k3sprox), a CiliumNetworkPolicy narrows that 443 to named hosts, with the DNS L7 rule FQDN matching needs: agent to `slack.com`, `*.slack.com`, `*.slack-edge.com`, `api.anthropic.com`; tool server and `seed entra sync` to `graph.microsoft.com`, `login.microsoftonline.com`; CNPG to the backup endpoint's host.
- **AC-15**: No Service of type `LoadBalancer` or `NodePort` and no Ingress other than two with class `tailscale`: the Zammad web UI (host `helpdesk.tailscaleHost`, default `noord-<cluster>-helpdesk`) and the tool server's reset listener port only (host `reset.tailscaleHost`, default `noord-<cluster>-reset`), so two clusters on one tailnet never collide. The MCP listener (`/mcp`, the approval routes, `/readyz`) and the agent are reachable only inside the cluster. `RESET_PUBLIC_URL` and `ZAMMAD_PUBLIC_URL` are `https://<host>.<TAILNET>`. The reset ingress records no request paths.

*Readiness and smoke*
- **AC-16**: The tool server serves `GET /readyz` on its MCP listener behind the same bearer token, and the agent serves `GET /readyz` on its health listener. Each returns JSON `{"checks": {"<name>": {"status": "ok"|"unavailable"|"not_configured", "cause": "<cause token>"}}}` with status 200 when every configured check is `ok`, else 503, and never error text. Tool server checks: `database` (ping), `ldap` (bind as its service account), `zammad` (an authenticated read), `graph` (a token, when configured). Agent checks: `database`, `toolserver` (an authenticated `/readyz` call), `slack` (Socket Mode connected, when tokens are set). Neither is a Kubernetes probe; liveness and readiness stay on `/healthz`.
- **AC-17**: The chart renders a suspended CronJob `noord-smoke` in `noord-agent` (the seed image at the cluster's `image.tag`, ServiceAccount `noord-smoke` with no RBAC and no mounted token, label `app.kubernetes.io/name: noord-smoke`). The installer, after AC-1's Healthy wait, and `make smoke CLUSTER=<name>` run it with `kubectl create job --from=cronjob/noord-smoke`. `seed smoke` passes only when: both `/readyz` return 200; an MCP `tools/list` with the bearer token returns at least one tool; and the goose version in `noord-pg` equals the newest embedded migration. It prints one line per check (`pass` or `fail` plus the cause token) and exits non zero on any failure. A refused connection (the `network` cause) is tried again until `SMOKE_SETTLE` (default `10s`, from `0s` to `1m`) has passed since the run began, because on a stock k3s cluster a new pod's first connections are refused for a second or two; a wrong answer is never tried again, and a service that stays down still fails as `network` after the settle period.

*Backups*
- **AC-18**: With `backup.enabled` true: an `ObjectStore` (`barmancloud.cnpg.io/v1`) at `s3://<BACKUP_S3_BUCKET>/<cluster>/` on `BACKUP_S3_ENDPOINT` (a Cloudflare R2 bucket in the EU jurisdiction on k3sprox, region `BACKUP_S3_REGION`, default `auto`) with sealed credentials; `noord-pg` archives WAL through the Barman Cloud plugin (`isWALArchiver: true`); a `ScheduledBackup` (`method: plugin`) runs daily at 02:00 UTC with `immediate: true`; `backup.retention` defaults to `14d`. On k3sprox the first backup reaches phase `completed` and the Cluster reports continuous archiving working before the first real audit rows land. `proofvm` runs with backups off. Zammad's data is not backed up.

*Day two operations*
- **AC-19**: On every push to the default branch of the repo CI runs in (yours, or a fork's own), CI builds `linux/amd64` and `linux/arm64` images for `agent`, `toolserver`, `seed` and `samba` and pushes them to `ghcr.io/<lowercased owner>/aiitsupport-<name>:<full git SHA>` with `packages: write`; pull requests (a fork's included) build without pushing. Commits that change only `deploy/clusters/**` skip the image builds (`paths-ignore`) but still run the `deploy` job. `make release CLUSTER=<name> SHA=<sha>` checks that all four tags exist, writes `image.tag` in `deploy/clusters/<name>/values.yaml`, commits `chore(release): <name> to <short sha>` with hooks off and pushes (the AC-5 push rules); an unknown SHA or a missing image stops it with nothing written. `make cluster-init` sets `image.tag` to the newest SHA on the default branch that has all four images, and `image.registry` and `image.owner` (defaults `ghcr.io` and the lowercased `origin` owner) are values.
- **AC-20**: `make pause CLUSTER=<name>` sets `paused: true` in the cluster's values (agent at 0 replicas, seed Jobs not rendered), commits, pushes, and waits until the agent pod is gone. `make resume CLUSTER=<name>` sets it back and increments `seedRun`, so every seed step runs once more on that sync. It always commits and pushes both changes, including on a cluster that was never paused, where the `seedRun` bump is the only change.
- **AC-21**: `make uninstall CLUSTER=<name>` asks you to type the cluster name, refuses when `noord-<cluster>` is owned by another ArgoCD Application (naming the pointer to remove first, as on k3sprox), then deletes `noord-<cluster>` and `noord-<cluster>-platform` with cascade (every Application carries `resources-finalizer.argocd.argoproj.io`), waits until the three namespaces are gone, and deletes any PersistentVolumeClaim left in them. Bucket backups, `~/.noord/<name>/` and ArgoCD stay.
- **AC-22**: `make seal-key-backup CLUSTER=<name>` writes the Sealed Secrets controller's keys to `~/.noord/<name>/sealing-key.yaml` with mode 0600, outside the repo. `make install` prints a reminder when that file is absent, and a warning when the names of the controller's live keys differ from those in the backup (a controller that renews keys, like one installed before this spec, outdates the backup).
- **AC-23**: `deploy/clusters/<name>/bootstrap.yaml` holds the two ArgoCD Applications for the cluster, `noord-<name>-platform` and `noord-<name>`, in the AppProject `default`, each pointing at this repo's `deploy/argocd` chart with that folder's values (the platform one renders only the platform part). For k3sprox, an identical copy is committed as `apps/noord.yaml` in `toyinogun/k3sprox-gitops`, and `root` shows both Synced; the installer applies the same file, with no parameter override, so the two owners never disagree.
- **AC-24**: The tool server's Graph client uses a certificate when `GRAPH_CLIENT_CERT` (the path of a PEM holding the certificate and its key, inside the container) is set. `LoadGraph` refuses both `GRAPH_CLIENT_SECRET` and `GRAPH_CLIENT_CERT` set at once, naming both. The installer reads the local PEM from `GRAPH_CERT_FILE` in `secrets.env`, seals its content into the tool server's Secret, and the chart mounts it at the path it sets in `GRAPH_CLIENT_CERT`. The k3sprox install uses the certificate, and the tool server app's client secret is deleted in Entra after the cutover. The seed app keeps its client secret.

*Proof and documentation*
- **AC-25**: `docs/runbooks/deploy.md` and the README's install section say that model inference goes to Anthropic's global endpoint, because the Anthropic API offered no EU region on 2026-10-21, while every byte at rest stays in the cluster or the EU bucket.
- **AC-26**: A CI job `deploy` (joining `ci-ok`, same `runs-on` expression) runs `helm lint`, `helm template` of the bootstrap chart and our chart for every `deploy/clusters/*/values.yaml`, `kubeconform` with the Kubernetes schemas and the CRD schemas for the SealedSecret, CNPG, Barman Cloud, Tailscale and Cilium kinds, the AC-7 and AC-11 tests, and a check that no rendered object is a `Secret` with `data` or `stringData`.
- **AC-27**: The fresh install proof: on a new one node VM (`proofvm`, 4 vCPU, 12 GB, 60 GB, Ubuntu 24.04, k3s only; the run on 2026-10-31 used 8 GB, because the two hypervisors had no more free memory, with two other VMs stopped for the run) with k3sprox paused (AC-20), AC-1 passes on a recorded run; one Slack DM from a locked out demo user is unlocked end to end there (spec 0008's flow), and a reset link opens through the tailnet URL. Then the restore drill (AC-29), `make uninstall CLUSTER=proofvm`, the VM is deleted, its folder is removed in a commit, k3sprox is resumed, and a journal entry records the run.
- **AC-28**: `docs/runbooks/deploy.md` covers: prerequisites (including the tailnet policy's `tagOwners` for the Tailscale operator's tags), first install, release, pause and resume, rotating one secret, resetting the demo directory (delete the `samba-data` PVC and the Samba pod, let ArgoCD make both again, then `make resume` so the seed runs once more; the only way to change a Samba account password), switching to read only and back, backup checks and the restore, the sealing key backup and restore, uninstall, and the one Secret outside git.
- **AC-29**: The restore works once, for real: on `proofvm`, a CNPG `Cluster` `noord-pg-restore` in a scratch namespace bootstraps by recovery from k3sprox's object store path (read only, through the Barman plugin, with the sealed bucket credentials), reaches Healthy, and its `audit_log` row count matches k3sprox's at the backup's end time; the runbook's restore page is the steps used.

## Decision

**Chosen option**: Option 1: `make install` seals and pushes, a folder per cluster, an optional Noord demo

A Go installer in `cmd/seed` (over a new `internal/install` package) makes one command install everything through ArgoCD from this repo, with sealed secrets per cluster folder, the client systems behind a `demo` switch, and the network locked to the paths the safety claim needs.

**Implementation skills**: `senior-kubernetes-engineer` (`~/.claude/skills/senior-kubernetes-engineer/`) · `golang-error-handling` · `golang-security` (`samber/cc-skills-golang`, `.claude/skills/<name>/`) · `postgres-patterns`, `golang-testing` (`everything-claude-code` plugin) · `testcontainers-go` (`testcontainers/claude-skills`, `.claude/skills/testcontainers-go/`, for the `seed migrate` and installer tests). Open each only when building in its area.

## Feature design

### Data model sketch

No table or migration changes; spec 0003's schema is installed as is by `seed migrate`. The state this feature creates is cluster and local state:

| Store | Holds | Lifetime | Backed up |
|---|---|---|---|
| CNPG `Cluster` `noord-pg` (`noord-agent`) | database `noord` (owner `noord_owner`), roles `noord_agent`, `noord_toolserver`, `noord_eval` | until uninstall | yes, WAL plus daily base backups to R2 under `<cluster>/`, 14 days (AC-18) |
| Samba volume (`noord-directory`, PVC `samba-data` of the Deployment `samba`) | the domain, its SID, lockouts, passwords, and the `smb.conf` provisioning wrote | until uninstall or a directory reset | no, reseedable |
| Zammad's bundled Postgres, Redis and our Elasticsearch volumes (`noord-client`) | tickets, KB, search index | until uninstall or a helpdesk reset | no, reseedable |
| ConfigMap `noord-kill-switch` | `read_only` or `read_write` | created once, changed by `kubectl` only | no |
| `deploy/clusters/<name>/secrets.env` (local, gitignored) | every secret and private site value for that cluster, including generated ones | your machine | yours to keep; the live Secrets are the fallback (AC-5) |
| `~/.noord/<name>/` (local, 0600) | `ca.key`, `ca.crt`, `sealing-key.yaml` | your machine | the only copies outside the cluster (AC-6, AC-22) |

### Repo layout

```
deploy/
  argocd/                  chart "noord-apps": AppProjects, platform Applications, component Applications
    values.yaml            pinned chart versions (argo-cd, sealed-secrets, cert-manager, cloudnative-pg,
                           plugin-barman-cloud, tailscale-operator) and defaults
  helm/noord/              our chart: components agent, directory, helpdesk; zammad 20.0.0 as a
                           conditional dependency (Chart.lock pinned); values.yaml is what the IT person tunes
  clusters/
    _template/             values.yaml, secrets.env (generated keys blank, site keys CHANGE_ME, no dev values)
    k3sprox/               values.yaml, bootstrap.yaml, sealed-values.yaml, secrets.env (gitignored)
    proofvm/               the same, removed after AC-27
cmd/seed/                  new subcommands: install, seal, release, pause, resume, smoke, uninstall,
                           seal-key-backup, cluster-init, migrate, export
internal/install/          the installer (preflight, ArgoCD, platform, generate, seal, git, waits); on both
                           agent deny lists
Dockerfile                 a `seed` target that also copies db/ and seed/ (WORKDIR holds the seed tree)
docs/runbooks/deploy.md
```

`make cluster-init CLUSTER=<name>` copies `deploy/clusters/_template/` into a new folder and sets `image.tag` (AC-19); the stranger's path is: fork, `make cluster-init`, fill `secrets.env`, `make install`.

### ArgoCD shape

- `bootstrap.yaml`: two Applications in `argocd`, project `default`: `noord-<cluster>-platform` (path `deploy/argocd`, Helm value `part: platform`) and `noord-<cluster>` (Helm value `part: apps`), both with value files `values.yaml` and `../clusters/<cluster>/values.yaml` plus `../clusters/<cluster>/sealed-values.yaml` for the apps part, the repo URL and branch from `origin` at `cluster-init`, automated sync with prune and self heal, `ServerSideApply=true` and `SkipDryRunOnMissingResource=true`, retry limit 10 with backoff up to 5 minutes, and the resources finalizer.
- The platform part renders the AppProject `noord-platform` (sources: the pinned operator chart repos; destinations: each enabled operator's namespace; cluster scoped kinds: `Namespace`, `CustomResourceDefinition`, `ClusterRole`, `ClusterRoleBinding`, `MutatingWebhookConfiguration`, `ValidatingWebhookConfiguration`, `ClusterIssuer`) and one Application per enabled operator (waves in AC-4). With nothing enabled it renders only the AppProject.
- The apps part renders the AppProject `noord` (sources: this repo and the Zammad chart repo; destinations: the three namespaces; cluster scoped kinds: `Namespace` only) and `noord-agent` (wave 0), `noord-directory` (wave 0) and `noord-client` (wave 1, the last two only with `demo.enabled`), each rendering `deploy/helm/noord` with only its component on, in the AppProject `noord`, with the same sync options and finalizer.
- Waves inside `noord-agent`: minus 1 the `Namespace`; 0 SealedSecrets, ConfigMaps, the CNPG Cluster and backup objects, NetworkPolicies; 1 the migration Job (a Sync hook, `BeforeHookCreation` delete policy); 2 the tool server, the agent and the smoke CronJob.
- The kill switch ConfigMap carries `argocd.argoproj.io/sync-options: Prune=false` and its `data` is in the Application's `ignoreDifferences`.

### State transitions

The installer is a straight line; rerunning starts from the top and each step is a no op when already done:

`preflight` → `argocd` (install if missing) → `platform` (apply `noord-<c>-platform`, wait for enabled operators) → `certs` (make or renew the Samba CA and server cert in `~/.noord/<c>/` when absent or within 60 days of expiry) → `generate` (fill missing generated values) → `seal` (reseal changed values into `sealed-values.yaml`) → `commit and push` (skipped when nothing changed) → `overlays` (apply `noord-seed-overlays`) → `apply` (`noord-<c>`) → `wait` (every Application Synced and Healthy and every SealedSecret Synced, `INSTALL_TIMEOUT`) → `smoke` → `report`.

A failure at any step exits non zero naming the step; nothing after it runs.

### Interface surface

| Command or endpoint | Inputs | Outputs | Auth | Key errors |
|---|---|---|---|---|
| `make install CLUSTER=` (`seed install --cluster`) | cluster folder, `secrets.env`, overlay files, kube context | commit (when changed), installed apps, smoke report, URLs | your kubeconfig and git push rights | exit 2 preflight; exit 3 pull request opened; exit 1 step failure naming the step, or a timeout naming the unhealthy apps and SealedSecrets |
| `make seal CLUSTER=` | `secrets.env`, live Secrets | resealed `sealed-values.yaml` (no commit) | kubeconfig | controller not found |
| `make release CLUSTER= SHA=` | SHA | values commit | git push | unknown SHA; an image missing |
| `make pause` / `make resume CLUSTER=` | none | values commit; agent scaled; seeds rerun on resume | git push | push rejected |
| `make smoke CLUSTER=` | none | one line per check | kubeconfig | any check fails, exit 1 |
| `make uninstall CLUSTER=` | typed cluster name | deleted apps, namespaces, PVCs | kubeconfig | name mismatch; owned by another Application |
| `make seal-key-backup CLUSTER=` | none | `~/.noord/<name>/sealing-key.yaml` | kubeconfig | controller not found |
| `make cluster-init CLUSTER=` | name | new folder | read access to the registry | folder exists; no SHA with all four images |
| tool server `GET /readyz` (MCP listener) | bearer token | per check status, 200 or 503 | `TOOLSERVER_TOKEN` | 401 without token |
| agent `GET /readyz` (health listener) | none | per check status, 200 or 503 | network only (AC-14) | none |
| `seed migrate` | `MIGRATE_DATABASE_URL`, `MIGRATE_WAIT_TIMEOUT` | migrations applied | owner role | database or roles not ready in time |
| `seed export <dir>` | target dir | seed binary and `seed/helpdesk/` copied | none | target not writable |

### Secrets per workload

One Secret per workload, never shared; each is a `SealedSecret` rendered from `sealed-values.yaml` (S = `secret:"true"`, P = `private:"true"`, D = deploy only).

| Secret (namespace) | Workload | Keys |
|---|---|---|
| `noord-agent` (`noord-agent`) | agent | S: `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`, `ANTHROPIC_API_KEY`, `TOOLSERVER_TOKEN`, `AGENT_DATABASE_URL`; P: `SLACK_IT_CHANNEL` |
| `noord-toolserver` (`noord-agent`) | tool server | S: `TOOLSERVER_TOKEN`, `TOOLSERVER_DATABASE_URL`, `LDAP_BIND_PASSWORD`, `RESET_LDAP_BIND_PASSWORD`, `ZAMMAD_TOKEN`, `GRAPH_CLIENT_CERT` content (a file key); P: `LDAP_URL` (non demo), `LDAP_BASE_DN`, `LDAP_BIND_DN`, `RESET_LDAP_BIND_DN`, `GRAPH_TENANT_ID`, `GRAPH_CLIENT_ID`, `SLACK_IT_APPROVERS`, `ZAMMAD_URL` (non demo), `ZAMMAD_PUBLIC_URL`, `RESET_PUBLIC_URL` |
| `noord-migrate` (`noord-agent`) | migration Job | S: `MIGRATE_DATABASE_URL` |
| `noord-smoke` (`noord-agent`) | smoke Job | S: `TOOLSERVER_TOKEN`, `MIGRATE_DATABASE_URL` (read of `goose_db_version` only) |
| `noord-pg-<role>` (`noord-agent`) | CNPG roles | D: basic auth `username`, `password` for `noord_owner`, `noord_agent`, `noord_toolserver`, `noord_eval` |
| `noord-backup` (`noord-agent`) | ObjectStore | D: `BACKUP_S3_ACCESS_KEY_ID`, `BACKUP_S3_SECRET_ACCESS_KEY` |
| `noord-samba` (`noord-directory`) | Samba | S: `SAMBA_ADMIN_PASSWORD` (= `SEED_ADMIN_PASSWORD`), `SAMBA_SVC_PASSWORD` (= `LDAP_BIND_PASSWORD`), `SAMBA_ZAMMAD_PASSWORD` (= `SEED_ZAMMAD_BIND_PASSWORD`), `SAMBA_PWRESET_PASSWORD` (= `RESET_LDAP_BIND_PASSWORD`); D: `tls.crt`, `tls.key` |
| `noord-seed-directory` (`noord-directory`) | `seed directory`, `seed entra sync` | S: `SEED_ADMIN_PASSWORD`, `SEED_USER_PASSWORD`, `SEED_GRAPH_CLIENT_SECRET`, `SEED_ENTRA_DEMO_PASSWORD`; P: `SEED_ADMIN_USER`, `SEED_GRAPH_TENANT_ID`, `SEED_GRAPH_CLIENT_ID`, `SEED_ENTRA_DOMAIN` |
| `noord-seed-helpdesk` (`noord-client`) | `seed helpdesk` | S: `ZAMMAD_TOKEN`, `SEED_ZAMMAD_BIND_PASSWORD`, `SEED_ZAMMAD_ADMIN_PASSWORD`; P: `SEED_ZAMMAD_ADMIN_EMAIL`, `ZAMMAD_PUBLIC_URL` (required, the `fqdn` Zammad links to), `ZAMMAD_URL` (non demo); read by the Zammad image's Rails, not our loader: `ZAMMAD_POSTGRES_PASSWORD`, `ZAMMAD_REDIS_PASSWORD`. No directory admin or seed user password: the seed only binds as `svc-zammad` |
| `noord-zammad` (`noord-client`) | Zammad chart (`existingSecret`) | D: `postgres-password`, `redis-password` (the subcharts' key names, from `ZAMMAD_POSTGRES_PASSWORD` and `ZAMMAD_REDIS_PASSWORD`) |
| `noord-tailscale` (operator namespace) | Tailscale operator | D: `TS_OAUTH_CLIENT_ID`, `TS_OAUTH_CLIENT_SECRET` |
| `noord-seed-overlays` (`noord-directory`) | seed Jobs, optional | the two overlay files; applied directly, not sealed (AC-6) |

### Value sourcing

| Action | Value | Source | Form |
|---|---|---|---|
| install | target cluster check | `KUBE_CONTEXT` | `secrets.env`, never committed |
| install | repo URL and branch in `bootstrap.yaml` | `git remote get-url origin` (written as https) and the current branch, at `cluster-init` | committed |
| install | `image.tag`, `image.registry`, `image.owner` | AC-19 | committed values |
| install | role passwords, `TOOLSERVER_TOKEN` (48 chars), `ZAMMAD_TOKEN` (43 chars, spec 0006's token rules), `SEED_ADMIN_PASSWORD`, `LDAP_BIND_PASSWORD`, `SEED_ZAMMAD_BIND_PASSWORD`, `RESET_LDAP_BIND_PASSWORD`, `SEED_ZAMMAD_ADMIN_PASSWORD`, `SEED_USER_PASSWORD`, `SEED_ENTRA_DEMO_PASSWORD`, the Zammad database and Redis passwords | generated once (AC-5), passwords meeting Samba's complexity rule | sealed |
| install | `AGENT_DATABASE_URL`, `TOOLSERVER_DATABASE_URL`, `MIGRATE_DATABASE_URL`, `EVAL_DATABASE_URL` | derived: role and password from above, host `noord-pg-rw.noord-agent.svc`, database `noord`, `sslmode=verify-full`, `sslrootcert` the mounted CNPG CA (`noord-pg-ca`) | sealed |
| install | Samba CA and server certificate | the `seed certs` code with the cluster SANs, into `~/.noord/<c>/`; the server key sealed, the CA cert written into the cluster's values as `ca.certificate` (public) and rendered as ConfigMap `noord-ca` in the three namespaces | server key sealed; CA cert committed |
| tool server | `LDAP_URL`, `LDAP_CA_CERT` | demo: `ldaps://samba.noord-directory.svc.cluster.local:636` and the `noord-ca` mount path (chart); otherwise `secrets.env` | chart value or private |
| tool server | `LDAP_BASE_DN`, `LDAP_BIND_DN`, `RESET_LDAP_BIND_DN` | demo: from `seed/directory/noord.yaml`'s realm (the `.env.example` values), copied into `_template/secrets.env`; otherwise `secrets.env` | private |
| tool server | `ZAMMAD_URL` | demo: the Zammad chart's web Service in `noord-client` (name and port read once from the pinned chart's `helm template` and fixed in our chart); otherwise `secrets.env` | chart value or private |
| tool server | `ZAMMAD_DEFAULT_GROUP` | chart value, default from `internal/config` | committed |
| tool server | `ZAMMAD_PUBLIC_URL`, `RESET_PUBLIC_URL` | `https://<helpdesk or reset host>.<TAILNET>`; hosts from values, `TAILNET` from `secrets.env` | private |
| tool server | `GRAPH_TENANT_ID`, `GRAPH_CLIENT_ID`, `SLACK_IT_APPROVERS` | `secrets.env` (you) | private |
| tool server | `GRAPH_CLIENT_CERT` | the chart's mount path; the content from the file at `GRAPH_CERT_FILE` in `secrets.env` | content sealed |
| tool server | `RESET_HTTP_ADDR` | chart value `:8081` | committed |
| tool server | `KILL_SWITCH_FILE` | the chart's ConfigMap directory mount | committed |
| agent | `TOOLSERVER_URL` | chart: `http://toolserver.noord-agent.svc:8080` | committed |
| agent | `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`, `ANTHROPIC_API_KEY` | `secrets.env` (you) | sealed |
| agent | `SLACK_IT_CHANNEL` | `secrets.env` (you) | private |
| seed | `SEED_LDAP_URL`, `SEED_CA_CERT` | chart: the Samba Service URL and the `noord-ca` mount path | committed |
| seed | `SEED_ZAMMAD_LDAP_HOST` | chart: the Samba Service FQDN | committed |
| seed | Zammad's `fqdn`, `http_type` | derived from `ZAMMAD_PUBLIC_URL` (its host and scheme), set once on the fresh seed | private (sealed) |
| seed | `SEED_ADMIN_USER`, `SEED_ZAMMAD_ADMIN_EMAIL`, `SEED_ENTRA_DOMAIN`, `SEED_GRAPH_TENANT_ID`, `SEED_GRAPH_CLIENT_ID`, `SEED_GRAPH_CLIENT_SECRET` | `secrets.env` (you; the first two prefilled in the template); unset `SEED_GRAPH_*` skips the Entra step | private or sealed |
| backups | `BACKUP_S3_ENDPOINT`, `BACKUP_S3_BUCKET`, `BACKUP_S3_REGION` | `secrets.env` (you; region default `auto`) | private |
| network | `cluster.podCIDR`, `cluster.serviceCIDR`, `cluster.nodeCIDR`, `cluster.apiServerCIDR`, `platform.cnpg.namespace`, `tailscale.proxyNamespace` | cluster values; `cluster-init` fills the CIDRs from the live cluster (node addresses, the k3s defaults `10.42.0.0/16` and `10.43.0.0/16` unless the nodes report others) | committed (cluster internal ranges only) |
| Tailscale operator | `TS_OAUTH_CLIENT_ID`, `TS_OAUTH_CLIENT_SECRET` | `secrets.env` (you), only when `platform.tailscale` is on | sealed |
| release | image tag | the SHA argument, checked against the registry | committed |
| everything else | `TICKET_RETENTION_DAYS`, approval, access and KB settings, loop bounds, `LOG_LEVEL` | chart values, defaults from `internal/config` | committed |

### Resources

Defaults (values, per container, request then limit): agent and tool server 50m / 128Mi to 500m / 512Mi; migration, seed and smoke Jobs 50m / 64Mi to 500m / 256Mi (the helpdesk seed Job takes Zammad's Rails sizes, 500m / 1Gi to 2 / 2Gi); Samba 100m / 256Mi to 1 / 1Gi; Elasticsearch 250m / 1536Mi to 2 / 2Gi (1 GiB heap); CNPG instance 100m / 256Mi to 1 / 1Gi; Zammad's own components at the chart's defaults.

### Key invariants

- Each workload reads only its own Secret (*Secrets per workload*). The agent's Secret holds no client system credential; `LDAP_*`, `GRAPH_*` and `RESET_LDAP_*` passwords reach only the tool server, Samba (which must know its own accounts' passwords) and the seed Jobs; `ZAMMAD_TOKEN` reaches only the tool server and the helpdesk seed, which installs it in Zammad.
- Nothing is reachable from outside the cluster except the two tailnet hosts in AC-15.
- No `Secret` with data is ever rendered from git; every secret value arrives as a `SealedSecret`, except `noord-seed-overlays`.
- `secrets.env` and `~/.noord/` never enter the repo; the Samba CA key never enters the cluster.
- Every image is referenced by the full SHA tag or a pinned third party tag, never `latest`; every chart is pinned (Chart.lock, `deploy/argocd/values.yaml`).
- The kill switch survives ArgoCD self heal (AC-9).
- `bootstrap.yaml` and k3sprox's `apps/noord.yaml` are byte identical (AC-23).

### Security model

- **Credential boundary.** As in spec 0001: the agent never receives client system credentials; the network adds a second lock (AC-14), and `internal/install` joins `agent-credential-boundary` in `.golangci.yml` and `cmd/agent/boundary_test.go`.
- **Who can change the cluster.** Anyone who can push to the branch ArgoCD follows, and anyone with your kubeconfig. The AppProjects limit what the repo can create to the noord namespaces and the enabled platform pieces. The installer's direct pushes touch only `deploy/clusters/<name>/`; protecting `main` later is supported (AC-5's pull request path).
- **Admin surfaces.** The Zammad UI and the reset page are reachable only by tailnet members. The MCP listener, approval routes and `/readyz` are cluster internal, and the token guards the tool server's.
- **Secrets in a public repo.** Sealed blobs are safe while the controller key is private; that key is backed up only to `~/.noord/` (AC-22). If it leaks, rotate every secret, not just the key. Private site values (tailnet name, tenant and channel ids) are sealed too, so the repo shows none of them.
- **Personal data.** The two overlays (real Slack test emails and your MFA phone) never enter git (AC-6).
- **Compliance scope: GDPR.** Backups leave the cluster for an R2 bucket in the EU jurisdiction and are kept 14 days. Slack and the model provider remain processors outside the EU (AC-25, data processing note in #22).

### Configuration required

- `secret:"true"` and `private:"true"`: new `internal/config` tags. `secret` marks credentials (tokens, passwords, keys, database URLs); `private` marks site values that are not credentials but stay out of the public repo (tenant, client and channel ids, the public URLs, a client's LDAP and Zammad URLs and DNs).
- `GRAPH_CLIENT_CERT` (tool server, optional): the in container path of a PEM with the Graph app's certificate and key; exclusive with `GRAPH_CLIENT_SECRET`.
- `SEED_ZAMMAD_LDAP_HOST` (seed, default `samba`): the LDAPS host Zammad's LDAP source uses.
- `SEED_ZAMMAD_RUNNER` (seed, `compose` or `local`, default `compose`).
- `MIGRATE_WAIT_TIMEOUT` (migrate, default `5m`, 10s to 30m).
- A new `Install` struct, read only by `seed install` and its siblings: `KUBE_CONTEXT`, `TAILNET`, `INSTALL_TIMEOUT` (default `30m`), `GRAPH_CERT_FILE` (local path), `BACKUP_S3_ENDPOINT`, `BACKUP_S3_BUCKET`, `BACKUP_S3_REGION` (default `auto`), `BACKUP_S3_ACCESS_KEY_ID`, `BACKUP_S3_SECRET_ACCESS_KEY`, `TS_OAUTH_CLIENT_ID`, `TS_OAUTH_CLIENT_SECRET`. All appear in `.env.example` under an "Install (cluster only)" heading and in `deploy/clusters/_template/secrets.env`.
- Prerequisite accounts and settings: a Cloudflare R2 bucket in the EU jurisdiction with a token scoped to it; a Tailscale OAuth client for the proof VM's operator, and `tagOwners` for its tags in the tailnet policy; a certificate added to the tool server's Entra app; the GHCR packages set public after the first push.

### Critical test scenarios

- Happy path: the recorded `proofvm` run of `make install` ends with the smoke pass and a working Slack unlock, verifies **AC-1**, **AC-27**.
- Rerun: a second `make install` on k3sprox makes no commit and restarts nothing; changing one value rolls only the workload that reads it, verifies **AC-5**, **AC-8**.
- Preflight: a wrong kube context, a dirty tree, a missing `SLACK_APP_TOKEN`, a `CHANGE_ME` left in `secrets.env`, and a `image.tag` with no `samba` image each stop with exit 2 before any change, verifies **AC-2**.
- Secrets: CI fails a branch that adds a plaintext `Secret` under `deploy/`, and the drift test fails when a tagged field has no chart key or a chart key has no tagged field, verifies **AC-6**, **AC-7**, **AC-26**.
- Network, denied: from a debug pod in `noord-agent` labelled as the agent, Samba 636, Zammad and any host not on the FQDN list are unreachable, while the tool server reaches Samba and the Tailscale proxy reaches only port 8081, verifies **AC-14**.
- Exposure: `kubectl get svc,ingress -A` shows no LoadBalancer, NodePort or non tailscale Ingress for the install, and `/mcp` is 404 or unreachable through the reset host, verifies **AC-15**.
- Kill switch: flip it with `kubectl`, wait out a self heal cycle and a minute, and the next write call is refused, verifies **AC-9**.
- Readiness: stop Samba and the tool server's `/readyz` is 503 with `ldap` `unavailable` and a cause token, no error text, and `make smoke` fails on that line, verifies **AC-16**, **AC-17**.
- Seeds: a self heal sync starts no seed Job; `make resume` starts each once, verifies **AC-10**, **AC-20**.
- Backup and restore: the first `Backup` is `completed` and archiving is working on k3sprox; the restore on `proofvm` matches the row count, verifies **AC-18**, **AC-29**.
- Release: an unknown SHA leaves the values untouched, verifies **AC-19**.
- Uninstall guard: on k3sprox it refuses and names `apps/noord.yaml`; on `proofvm` it leaves no namespace or PVC behind, verifies **AC-21**.

## Build plan

Tracer Bullet for an install feature means the install pipe end to end first (git to ArgoCD to running pods to a passing check) on the cluster that already has the operators, then the business pipe through it (a real Slack unlock in the cluster), then thickening: network, fresh cluster path, backups, day two commands, and the proof.

1. **Tracer thread on k3sprox, agent stack only.** CI pushes the four images on the default branch (AC-19's CI part; the `seed` image target with `db/` and `seed/`; `paths-ignore` for `deploy/clusters/**`). The `secret` and `private` tags, `_template/secrets.env`, and the drift tests. `internal/install` minimal path: preflight, generate, seal changed into `sealed-values.yaml`, commit with hooks off and push, apply `noord-k3sprox`, wait including SealedSecret conditions. The `noord-apps` chart (both parts, platform empty) and `noord-agent` only; our chart's `agent` component: the Namespace, CNPG Cluster and managed roles, `seed migrate` with `LoadMigrate` waiting for the roles, tool server, agent (`Recreate`, grace 40s), the kill switch ConfigMap (directory mount, `ignoreDifferences`), checksum annotations, resources. One `make install CLUSTER=k3sprox` with `demo.enabled` false and Slack paused brings up a migrated database and both pods, satisfies **AC-5**, **AC-7**, **AC-8**, **AC-9**, **AC-12**, **AC-19** (CI part).
2. **Directory in the cluster.** The Samba Deployment (one replica, `Recreate`, `hostname: samba`) with its PVC, all in wave 1; the entrypoint copying certificates on every boot and its crash safe provisioning marker; then confirm LDAPS answers inside k3sprox under `hostname: samba` before trusting the seed Job's result (the host name fix rests on Docker tests), the `certs` step (CA in `~/.noord/`, sealed server key, `noord-ca` ConfigMaps), `noord-seed-overlays`, the hashed `seed directory` Job, the tool server's `LDAP_*` wiring, satisfies **AC-6**, **AC-10** (directory part).
3. **Helpdesk in the cluster, then the business pipe.** The Zammad dependency (chart 20.0.0, ECK and RustFS off, `existingSecret` passwords, `fqdn`, `http_type`; confirm the bundled images resolve and record the web Service name; confirm its init Job completes under ArgoCD as a Sync hook), our Elasticsearch StatefulSet, the `local` Runner and its environment template with the env name test, `seed export`, `SEED_ZAMMAD_LDAP_HOST`, the hashed helpdesk Job waiting for `svc-zammad` with the shared `TICKET_RETENTION_DAYS`, the Tailscale ingress for the UI (no path logging checked). Then resume Slack on k3sprox and run one live locked out user DM end to end in the cluster, satisfies **AC-10**, **AC-11**, **AC-13**.
4. **Reset page and Entra.** The reset ingress (reset port only, per cluster host), `RESET_PUBLIC_URL` from `TAILNET`, `GRAPH_CLIENT_CERT` in `internal/graph` and `LoadGraph`, `GRAPH_CERT_FILE` in the installer, the hashed `seed entra sync` Job after `seed directory`, the cutover and the old secret deleted, satisfies **AC-15**, **AC-24**.
5. **Network and pod security.** Default deny policies and the full allow list (Tailscale proxies, CNPG operator, kube API, Barman plugin), the Cilium FQDN policy with its DNS rule behind `cilium.fqdnEgress`, Pod Security labels and the restricted security contexts; the denied path tests from a debug pod, satisfies **AC-12**, **AC-14**, **AC-15**.
6. **Readiness and smoke.** `/readyz` on both binaries with cause tokens and allow listed log keys, `seed smoke`, the suspended `noord-smoke` CronJob, the installer's wait, smoke and report steps, `make smoke`, satisfies **AC-1** (report), **AC-16**, **AC-17**.
7. **Backups.** `platform.barmanPlugin` on k3sprox, the R2 EU bucket and token, `ObjectStore` under `<cluster>/`, the Cluster plugin, `ScheduledBackup`, 14 day retention; check the first backup and continuous archiving, satisfies **AC-4** (plugin), **AC-18**.
8. **Fresh cluster path.** ArgoCD install when missing, the platform part with its waves (Sealed Secrets with renewal off, cert-manager, CNPG, Barman plugin, Tailscale operator), the full preflight list (images, free memory, `CHANGE_ME`), `make cluster-init` (tag, CIDRs), the pull request push path, satisfies **AC-2**, **AC-3**, **AC-4**, **AC-5** (push path).
9. **Day two commands.** `make release`, `pause`, `resume` (`seedRun`), `uninstall` with its guard and PVC cleanup, `seal-key-backup` with the reminder and the key name warning, satisfies **AC-19**, **AC-20**, **AC-21**, **AC-22**.
10. **CI deploy job.** `helm lint`, template per cluster folder, `kubeconform` with the CRD schemas, the drift and env name tests, the no plaintext Secret check, joined to `ci-ok`, satisfies **AC-6**, **AC-26**.
11. **Registration and docs.** The pointer `apps/noord.yaml` in `k3sprox-gitops` (a PR there), `docs/runbooks/deploy.md`, the README install section with the EU inference statement, satisfies **AC-23**, **AC-25**, **AC-28**.
12. **The fresh install proof.** Create `proofvm`, pause k3sprox, record `make install CLUSTER=proofvm`, unlock a user over Slack there, open a reset link on the tailnet, run the restore drill, uninstall, delete the VM and its folder, resume k3sprox, write the journal entry, satisfies **AC-1**, **AC-27**, **AC-29**.

## Consequences

**Positive**:
- One command installs the whole system into a bare k3s cluster, and the same command keeps a running cluster in step, so the README claim is true.
- Every follow up eleven specs left for #17 lands in one place, and the safety claim is now enforced by the network as well as by code.
- A real client gets the honest shape: turn `demo` off and point a handful of private settings at their systems.
- Backups leave the cluster, inside the EU, before the first real audit rows, and a restore is proven once.

**Negative / tradeoffs**:
- **Inference is not EU bound.** You chose the Anthropic API over Vertex EU; success criterion 3 holds for data at rest only, and the scope's Done when line must change (see Follow up).
- **The installer pushes to git.** It needs a clean tree and push rights, it skips the git hooks for its own commits (they touch only cluster data), and it is real code to test and maintain (preflight, sealing, waits).
- **Installing needs Go** on the machine that runs it, besides `kubectl`, `helm`, `kubeseal`, `git` and (for the pull request path) `gh`.
- **One Secret lives outside git** (`noord-seed-overlays`); a cluster rebuilt from git alone seeds without the demo overlays until `make install` runs again.
- **Sealed blobs for k3sprox are public.** Their safety rests on the controller key; losing that key means resealing everything, leaking it means rotating everything. k3sprox's existing controller may renew keys, so its backup needs refreshing when the installer warns.
- **The Samba CA key lives only on your machine.** Lose `~/.noord/k3sprox/` and the next certificate renewal needs a new CA, which means reseeding Zammad's trust and restarting the tool server.
- **Elasticsearch without mmap and without security** is fine at Noord's size behind its NetworkPolicy, not a production search setup.
- **Zammad's bundled databases come from a third party chart source** (cloudpirates), not yet checked for image health; task 3 checks it, and the fallback is the ECK operator path from rationale.md.
- **The Barman plugin pulls in cert-manager** on a fresh cluster, so a bare install runs five operators.
- **The allow list is long and exact.** A new dependency (a new SaaS host, a sidecar) needs a policy change, and a missing allow shows up as a timeout, not an error.
- **Two owners on k3sprox**: `root` (through the pointer) and `make install` apply the same file; they must stay identical (AC-23).

**Amendments after the fresh cluster proof (2026-10-31)**: the proof ran as AC-27 describes, and found four places where this spec's wording was wrong or incomplete. No decision changed; the text above now says what was built and proven. The record is journal [0081](../../journal/0081-the-fresh-install-proof-run.md).
- AC-14: the helpdesk seed Job needs Elasticsearch on both ends of the connection (journal 0079). The first fix aimed its egress rule at the wrong pods; a test now checks the target as well as the port.
- AC-17: the smoke check settles a new pod's first refused connections (journal 0080), through the new setting `SMOKE_SETTLE`.
- AC-20: `make resume` always commits its `seedRun` bump (journal 0082).
- AC-27: the proof VM had 8 GB, not 12 GB, and ran the install four times before it passed, each time for a reason recorded in the journal.

**Neutral**:
- `cmd/seed` becomes the ops tool for the cluster too (install, release, smoke), and `internal/install` joins both deny lists.
- Spec 0004's "cert-manager self signed CA Issuer" becomes a locally generated CA; spec 0006's two phase Zammad token is gone, since the seed already installs a known token value.
- Seed steps run on change (a hash), not on every sync, which differs from the "PostSync hook" spec 0004 sketched.
- The proof VM briefly holds a copy of the system and is then destroyed; its folder leaves the tree but stays in history (sealed for a controller that no longer exists).

## Follow-up

- [ ] **Scope:** amend #17's Done when line from "model inference goes to an EU region endpoint" to "the EU inference gap is stated in the runbook and README (the Anthropic API had no EU region on 2026-10-21), and `internal/llm` stays swappable".
- [ ] **Spec 0001:** close the week 1 EU residency follow up with the outcome above, and log it in the journal; tick the #8/#17 and #17 follow ups; note the amendments (installer pushes, `seed` and `samba` images, the local CA, the demo switch, two bootstrap Applications instead of `kubectl apply -f deploy/argocd/bootstrap.yaml`).
- [ ] **Spec 0003:** tick the #17 follow up (managed roles, migration Job, backups) and note that roles come from CNPG, not `db/roles.sql`, in the cluster.
- [ ] **Spec 0004:** amend the k3s note (local CA, hashed seed Jobs instead of PostSync, a one replica Deployment with `hostname: samba` instead of a StatefulSet) and tick its #17 follow up.
- [ ] **Spec 0005:** tick the certificate follow up for the tool server app only; the seed app keeps its client secret.
- [ ] **Spec 0006:** close the two phase token and Zammad chart follow ups.
- [ ] **Specs 0007, 0008, 0010, 0011, 0012, 0013:** tick their #17 follow ups (kill switch mount and NetworkPolicy; `Recreate` and grace; one retention value; the reset ingress; approval routes off any ingress; `ACCESS_*` and the approval settings as chart values).
- [ ] **#22 data processing note:** the R2 bucket (EU jurisdiction, 14 days), Slack, and Anthropic's global endpoint as processors, with what each sees.
- [ ] **Phase 2:** Vertex AI `eu` (or Bedrock `eu.`) as a `LLM_PROVIDER` case when a client requires EU inference; pricing rows with the regional premium (enrolled on the scope's Deferred list).
- [ ] **Phase 2:** a restore drill on a schedule, not just once (enrolled on the scope's Deferred list).
- [ ] Agent Skills not installed now (you can add them later): `tailscale/tailscale-skill`, `wshobson/agents` `helm-chart-scaffolding` and `k8s-security-policies`, `mmontes11/skills` `kubeseal`.
- [ ] MCP server to connect later in your own MCP settings: `argoproj-labs/mcp-for-argocd` with `MCP_READ_ONLY=true` and an ArgoCD API token.
- [ ] `/sync`: root `AGENTS.md` (the install commands, the `deploy/` layout, `internal/install` on the deny lists, the `secret` and `private` tags, the one Secret outside git), `internal/config/AGENTS.md` (the tags, `Install`, the new settings), `internal/seed/AGENTS.md` (the new subcommands, the `local` Runner, `seed export`), a new `internal/install/AGENTS.md`, `internal/graph/AGENTS.md` (certificate credential), `internal/server/AGENTS.md` (`/readyz`), `docs/runbooks/ci-runner.md` (image pushes).
