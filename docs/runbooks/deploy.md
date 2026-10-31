# Runbook: install and run the cluster

This page is for you as Noord's IT person. It says how to install the whole system into a cluster with one command, and how to do the everyday jobs after that: release, pause, rotate a secret, switch to read only, check backups, and uninstall (spec [0015](../specs/0015-gitops-install/index.md)).

Everything below runs from a clone of this repo, on your own machine, against the cluster named by `KUBE_CONTEXT`.

## Where your data goes

Everything at rest stays in your cluster or in an EU bucket. Model inference does not: the agent calls Anthropic's global endpoint, because the Anthropic API offered no EU region on 2026-10-21. Slack sees the chat. If a client needs EU only inference, `internal/llm` is built to swap the provider (a Vertex AI `eu` or Bedrock `eu.` case is listed as Phase 2 in the spec).

## Prerequisites

On your machine: Go (the version in `go.mod`), `kubectl`, `helm`, `kubeseal`, `git`, and `gh` (only for the pull request path below).

For a fresh cluster:

- A one node k3s cluster with its default StorageClass. The demo needs about 6 GiB of allocatable memory free (`demo.minFreeMemory`).
- Four images pushed by CI for the commit you install: `agent`, `toolserver`, `seed`, `samba`, under `ghcr.io/<owner>/aiitsupport-<name>:<full sha>`. Set the GHCR packages to public after the first push, so the cluster can pull them without a secret.
- A Cloudflare R2 bucket in the EU jurisdiction and a token scoped to it, if you turn backups on.
- A Tailscale OAuth client if you turn on the Tailscale operator, and `tagOwners` entries in your tailnet policy for the operator's tags (`tag:k8s-operator` and `tag:k8s` unless you changed them). Without them the operator cannot create its proxies.
- A certificate added to the tool server's Entra app, if you use Entra (`GRAPH_CERT_FILE`).

## First install

1. Make the cluster folder from the template and the live cluster:

   ```bash
   make cluster-init CLUSTER=<name>
   ```

   It reads the pod, service, and node ranges and the operators already running, picks the newest commit that has all four images, and writes `deploy/clusters/<name>/values.yaml`, `bootstrap.yaml`, and `secrets.env`. It never overwrites a file that exists.

2. Open `deploy/clusters/<name>/secrets.env` and fill every `CHANGE_ME`. Blank values are optional or generated. This file is gitignored and holds every secret of the install, so keep a copy somewhere safe.

3. Install:

   ```bash
   make install CLUSTER=<name>
   ```

   The command checks first and changes nothing if a check fails (exit 2, with a message that names the problem and never a value). Then it installs ArgoCD if the cluster has none, installs the platform operators you enabled, generates and seals your secrets, commits and pushes only `deploy/clusters/<name>/`, applies the two Applications, waits until everything is Synced and Healthy, runs the smoke check, and prints the helpdesk and reset page URLs.

   It stops within `INSTALL_TIMEOUT` (default 30 minutes). Running it again is safe: with nothing changed it commits nothing, restarts nothing, and ends with the smoke check.

   Exit codes: 0 done, 1 a step failed (the message names it), 2 a check failed before anything changed, 3 the branch is protected and a pull request was opened (merge it, then run the command again).

4. The kill switch starts at `read_only`. When you are ready for the agent to act, flip it (see below).

## Release a new version

CI builds and pushes the four images on every push to the default branch. Then:

```bash
make release CLUSTER=<name> SHA=<full sha>
```

It checks that all four images exist, writes `image.tag` in the cluster's `values.yaml`, commits, and pushes. ArgoCD rolls the pods. An unknown commit or a missing image stops it with nothing written.

## Pause and resume

```bash
make pause CLUSTER=<name>
make resume CLUSTER=<name>
```

Pause sets `paused: true`: the agent goes to zero replicas and no seed Job renders. The command waits until the agent pod is gone. Resume sets it back and raises `seedRun`, so every seed step runs once more.

## Check the cluster

```bash
make smoke CLUSTER=<name>
```

It prints one line per check (`pass` or `fail` plus a short cause) and exits non zero on any failure. It checks both `/readyz` endpoints, a tool list through the tool server, and that the database has the newest migration. It refuses to run if your kube context is not the cluster's.

## Switch to read only, and back

The kill switch is a ConfigMap the tool server reads. Account changes run only when it holds exactly `read_write`; any other content (the install starts it at `read_only`) refuses them, while lookups and ticket tools keep working.

```bash
kubectl -n noord-agent patch configmap noord-kill-switch --type merge -p '{"data":{"mode":"read_only"}}'
```

Use `read_write` in the same command to allow changes again. The change takes effect in about a minute, with no restart. ArgoCD ignores this value on purpose, so self heal never undoes your flip, and the mode is never kept in git.

## Rotate one secret

1. Edit the value in `deploy/clusters/<name>/secrets.env`.
2. Run `make install CLUSTER=<name>`.

The installer reseals only the values that differ from the live Secret, commits, and pushes. Only the workloads that read that Secret restart. Do not edit `sealed-values.yaml` by hand.

Exception: Samba's account passwords take effect only when the domain is provisioned. To change one, reset the demo directory (next section).

## Reset the demo directory

This is the only way to change a Samba account password, and it wipes the demo directory.

1. Delete the `samba-data` PersistentVolumeClaim and the Samba pod in `noord-directory`.
2. Let ArgoCD make both again.
3. Run `make resume CLUSTER=<name>`, so the seed runs once more.

## Backups and restore

Backups run only when `backup.enabled` is true in the cluster's values. The database archives its write ahead log to the R2 bucket continuously, and a daily backup runs at 02:00 UTC. Backups are kept 14 days. Zammad's data is not backed up.

Check them:

```bash
kubectl -n noord-agent get backups
kubectl -n noord-agent get cluster noord-pg -o jsonpath='{.status.conditions[?(@.type=="ContinuousArchiving")].status}'
```

You want the latest backup in phase `completed` and `True` from the second command.

To restore, make a new CNPG `Cluster` in a scratch namespace that bootstraps by recovery from the bucket path `<cluster>/` through the Barman Cloud plugin, using read only access to the same bucket. Wait for it to be Healthy, then compare the `audit_log` row count with the source. This page is a sketch: the restore drill on the proof VM (spec 0015, task 12) has not run yet, and the exact manifest gets written here after it does.

## Back up the sealing key

Sealed secrets can only be opened by the controller's private key. Lose it and you reseal everything from `secrets.env`; leak it and you rotate everything.

```bash
make seal-key-backup CLUSTER=<name>
```

This writes `~/.noord/<name>/sealing-key.yaml` (mode 0600), outside the repo. `make install` reminds you when the file is missing and warns when the controller's live keys differ from the backup. A controller that renews its keys makes the backup stale, so run the command again when you see the warning.

To restore, apply that file to the cluster's sealed secrets namespace, then restart the controller.

## Uninstall

```bash
make uninstall CLUSTER=<name>
```

It asks you to type the cluster name. It refuses when another ArgoCD Application owns `noord-<name>` (on k3sprox, remove the pointer `apps/noord.yaml` in the `k3sprox-gitops` repo first). Then it deletes the Applications with their resources, waits until the three namespaces are gone, and deletes any volume left behind. Bucket backups, `~/.noord/<name>/`, and ArgoCD stay.

## The one Secret outside git

`noord-seed-overlays` in `noord-directory` is built from `seed/directory/overlay.local.yaml` and `seed/entra/demo.local.yaml` on your machine and applied straight to the cluster by `make install`. It never touches disk or git. A cluster rebuilt from git alone seeds without the demo overlays until you run `make install` again.

## When something goes wrong

- Policies are default deny. A new outside host or sidecar needs a policy change, and a missing allow shows up as a timeout, not an error. Kubelet probes are not affected.
- A slow cluster API (disk pressure on etcd) fails the checks with `InternalError`. Nothing was changed; wait and run the command again.
- If the install times out because ArgoCD already moved to a newer commit, run it again.
- The build journal in `docs/journal/` has the cases that cost real time.
