# Runbook: the CI runners in the k3sprox cluster

CI (`.github/workflows/ci.yml`) runs on ephemeral self-hosted runners in the k3sprox homelab cluster (k3s on Proxmox, linux/amd64). They come from GitHub's Actions Runner Controller (ARC): a runner scale set named `k3sprox`, which is also the `runs-on` label. Each job gets a fresh pod, used once, then deleted.

Until 2026-10-20 CI ran on two runners on a Raspberry Pi. They shared one user's Docker and Go state, and jobs timed out under load (journal [0049](../journal/0049-pi-runners-share-a-buildx-config.md)).

## Which runner a job uses

Every job has the same `runs-on` expression:

```yaml
runs-on: ${{ (github.event_name == 'push' || github.event.pull_request.head.repo.full_name == github.repository) && 'k3sprox' || 'ubuntu-latest' }}
```

- A push to `main`, or a pull request from a branch in this repo, runs in the cluster.
- Anything else runs on `ubuntu-latest`: a pull request from a fork, and one whose fork was deleted (its head repo is null, so a check on `head.repo.fork` alone would pick the cluster). The repo is public, and a runner pod is privileged inside a home network, so code from outside the repo never runs there.
- Also keep the repo setting **Settings, Actions, General, Fork pull request workflows** on "Require approval for all outside collaborators", so a fork run waits for you either way.

## What a runner pod is

| Container | What |
|---|---|
| `runner` | `ghcr.io/actions/actions-runner`, with `DOCKER_HOST` pointing at the sidecar's socket |
| `dind` | `docker:<n>-dind` as a native sidecar, **privileged**, with its own `/var/lib/docker` emptyDir (capped at 20Gi) |

Service containers (Postgres in `test`), testcontainers (the live Samba tests), buildx, and QEMU all use the sidecar's dockerd. Ports they publish are on the pod's localhost. Nothing is shared between jobs: no Docker state, no Go cache on disk. The Go cache comes from `actions/cache`.

The runner image has no `make` or C compiler. The `setup-go` composite action installs `make`, `gcc`, and `libc6-dev` when they are missing; that step is a no-op on `ubuntu-latest`.

## Where it is defined

Everything is GitOps, in the `toyinogun/k3sprox-gitops` repo. Argo CD syncs it; never `helm install` by hand.

| File | What |
|---|---|
| `apps/arc-chart-repo.yaml` | the OCI chart repository entry for Argo CD |
| `apps/arc-controller.yaml` | the ARC controller and CRDs (namespace `arc-systems`) |
| `apps/arc-aiitsupport.yaml` | the `k3sprox` scale set: chart version, runner and dind images, 0 to 6 runners, resources |
| `manifests/arc-runners/arc-github.sealedsecret.yaml` | the GitHub App credentials (app id, installation id, private key), sealed |

Auth is a GitHub App owned by the account, with one repository permission (Administration: read and write) and installed on this repo only. It does not expire.

## Change something

- **More or fewer runners, resources, images:** edit `apps/arc-aiitsupport.yaml`, open a PR in k3sprox-gitops, merge. Argo CD applies it.
- **Upgrade ARC:** bump `targetRevision` in both `arc-controller.yaml` and `arc-aiitsupport.yaml` together. The controller and the scale set chart must be the same version.
- **Rotate the GitHub App key:** generate a new private key on the app's settings page, reseal it (below), merge, then delete the old key on GitHub.

Reseal the credentials (asks for the three values; only the encrypted secret is written):

```bash
read "APP_ID?App ID: " && read "INST_ID?Installation ID: " && read "PEM?Path to the .pem file: " && PEM="${PEM/#\~/$HOME}" && kubectl create secret generic arc-github -n arc-runners --from-literal=github_app_id="$APP_ID" --from-literal=github_app_installation_id="$INST_ID" --from-file=github_app_private_key="$PEM" --dry-run=client -o yaml | kubeseal --format yaml > manifests/arc-runners/arc-github.sealedsecret.yaml
```

## Check on it

- GitHub: Settings, Actions, Runners lists the `k3sprox` scale set. From a laptop: `gh api repos/toyinogun/AIITsupport/actions/runners --jq '.runners[] | "\(.name) \(.status)"'` (runners appear only while a job runs).
- Cluster: `kubectl get applications -n argocd | grep arc`, `kubectl get pods -n arc-systems` (the controller and the `k3sprox` listener), `kubectl get pods -n arc-runners` (one pod per running job).

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Jobs sit in "Waiting for a runner" | the listener is down or cannot authenticate | `kubectl logs -n arc-systems -l app.kubernetes.io/component=runner-scale-set-listener`; a 401 means the App key or installation is wrong, reseal it |
| Runner pods stay `Pending` | not enough CPU or memory on the workers | `kubectl describe pod -n arc-runners <pod>`; lower `maxRunners` or the requests |
| A job dies with no error and the pod shows `Evicted` | the Docker data emptyDir passed its 20Gi limit | raise `sizeLimit` in `arc-aiitsupport.yaml`, or check what the job builds |
| `go: -race requires cgo` | the setup-go install step was skipped or failed | read that step's log; it needs `sudo apt-get` in the runner image |

## Remove

Delete the three `apps/arc-*.yaml` files and `manifests/arc-runners/` from k3sprox-gitops and merge; Argo CD prunes the scale set, then the controller. Then set every `runs-on` back to `ubuntu-latest`, and delete the GitHub App.
