# internal/install

## Overview

The one command cluster installer behind `make install CLUSTER=<name>` (`seed install`, spec 0015). It checks the cluster and the clone, fills a cluster's generated secrets, seals every secret into `deploy/clusters/<name>/sealed-values.yaml`, commits and pushes only that folder, applies the cluster's bootstrap Applications, and waits until ArgoCD reports everything Synced and Healthy. Only `cmd/seed` uses it.

## Key files

| File | Owns |
|---|---|
| `install.go` | `Run` and its steps in order (preflight, platform, certs, generate, seal, commit and push, apply, overlays, wait, report), `Options`, `ExitError` and the exit codes |
| `table.go` | `LoadTable`: the Secret table `deploy/helm/noord/files/secrets.yaml` (which Secret each workload reads, each key's source), `Inputs`, `Enabled`, `Required` |
| `secrets.go` | `Build` (plaintext per Secret, in memory only), `Generate` (create a missing value once, or read it back from the live Secret), `randomValue` |
| `seal.go` | `ReadSealed`, `WriteSealed`, `Reseal`: reseal a key only when its value differs from the live Secret |
| `directory.go` | the demo components (`components`), the `certs` step (CA and Samba server pair in `~/.noord/<cluster>/`), the `overlays` step (`noord-seed-overlays`, applied straight to the cluster) |
| `cluster.go` | `OpenFolder`, `Values` (the part of a cluster's `values.yaml` the installer reads), `SetCACertificate` |
| `env.go` | `EnvFile`: parse and write `secrets.env` (mode 0600, errors name the line, never a value) |
| `ops.go` | the day two commands: `Release`, `Pause`, `Resume`, `Uninstall`, `SealKeyBackup`, and the key backup notice `install` prints (AC-19 to AC-22) |
| `fresh.go` | the fresh cluster path: the `argocd` step (installs the pinned chart only when `argocd-server` is missing), the demo memory check, the pull request push path (exit 3), `Record` use in sealing |
| `clusterinit.go` | `ClusterInit` (`make cluster-init`): the folder from the template, the ranges and operators read from the live cluster, the newest commit with all four images, the generated `bootstrap.yaml` |
| `git.go` | `Changed`, `Unexpected`, `Track`, `Commit` (only `values.yaml` and `sealed-values.yaml`, hooks off), `Push` |
| `images.go` | `Registry.Exists`: anonymous checks that the four images exist for `image.tag` |
| `kube.go` | `Runner`, `ExecRunner`, `Kube` (every `kubectl` call names its context), `CmdError` |
| `chart_test.go` | renders the client part with `helm template` and checks the helpdesk Job's Rails environment and that no plain `Secret` renders (skips without `helm`) |

## Conventions

- Every step is a no op when its work is done, so a rerun with unchanged inputs commits nothing and restarts nothing (AC-8). Keep new steps that way.
- Preflight changes nothing and exits 2 (`ExitPreflight`) with a message that names the problem, never a value; any later step failure exits 1 naming the step. One `INSTALL_TIMEOUT` bounds everything after preflight.
- The Secret table is shared with the chart: the installer builds and seals exactly the keys the chart renders. Adding a key means editing `files/secrets.yaml`, `internal/config` (a `secret` or `private` field), `.env.example`, and `deploy/clusters/_template/secrets.env`; the table tests fail on any drift (AC-7).
- A key with `rails: true` is read by the Zammad image's Rails, not by our loader, so the AC-7 test leaves it out.
- A generated value is made once with `crypto/rand` (letters and digits, at least one upper, lower, and digit, so it meets Samba's rule and needs no URL escaping) and written back to `secrets.env`. A value lost from `secrets.env` is read back from the live Secret, never made again.
- The installer commits only `deploy/clusters/<name>/values.yaml` and `sealed-values.yaml`, with `LEFTHOOK=0`, and pushes to the upstream branch. Preflight refuses any other change in the tree, so keep unrelated work off the branch you install from.
- Every external command goes through `Runner`; tests pass a fake runner, a fake `Live`, and a fake registry, never a real cluster.
- `internal/install` is on both agent deny lists (`.golangci.yml` `agent-credential-boundary` and `cmd/agent/boundary_test.go`).

## Gotchas

- With Tailscale on, its operator Application only turns healthy after the apps part delivers its OAuth Secret, so the platform wait leaves it and the platform Application out; the last wait covers both.
- A pull request push (exit 3) puts the local branch back on its upstream. `~/.noord/<cluster>/sealed-record.json` (hashes, mode 0600) lets the rerun after the merge keep the sealed ciphertext instead of sealing again and opening another pull request.

- `make install` builds `bin/seed` and runs it, because `go run` hides the exit code (journal 0062).
- The wait step waits for every Application to reach the commit it pushed or found. If ArgoCD has already moved to a newer commit, the run times out; rerun it (journal 0069 has a case).
- Sealing is not deterministic, so the installer compares plaintext with the live Secret, never ciphertext with the file.
- A cluster whose API is slow (etcd under disk pressure) fails preflight with `InternalError`; nothing is changed, wait and rerun (journal 0069).

## Related specs

- [0015 One command GitOps install](../../docs/specs/0015-gitops-install/index.md) (the contract, AC-1 to AC-29)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
