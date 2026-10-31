# deploy

## Overview

Everything ArgoCD installs (spec 0015): the bootstrap chart that renders the Applications, our own chart with one component per Application, and one folder per cluster holding that cluster's values and sealed secrets. `make install CLUSTER=<name>` (`internal/install`) fills, seals, commits, and applies it; after that, git is the only way the cluster changes.

## Key files

| File | Owns |
|---|---|
| `argocd/` | chart `noord-apps`: `part: platform` renders the operator Applications (`templates/platform.yaml`), `part: apps` renders the AppProject `noord` and the component Applications `noord-agent`, `noord-directory`, `noord-client` (`templates/apps.yaml`, helper `apps.component`) |
| `helm/noord/` | our chart; `values.yaml` holds the defaults, `component` picks the part (`agent`, `directory`, `client`); templates live in `templates/<component>/` |
| `helm/noord/files/secrets.yaml` | the Secret table the chart and the installer share: every workload's Secret, its keys, and each key's source |
| `helm/noord/charts/zammad-20.0.0.tgz`, `Chart.lock` | the vendored Zammad chart (with its Postgres, Redis, and Memcached subcharts), rendered only by `noord-client` |
| `clusters/_template/` | what `cluster-init` copies for a new cluster: `values.yaml`, and `secrets.env` with `CHANGE_ME` and blanks only |
| `clusters/<name>/` | `values.yaml` (committed, no secret, no private value), `sealed-values.yaml` (written by `make install` only), `bootstrap.yaml` (the two bootstrap Applications), `secrets.env` (gitignored) |
| `helm/noord/files/observability/` | spec 0016: the Collector config, the Grafana datasources and provider, and the dashboard JSON, read by `templates/agent/observability.yaml` (ConfigMaps) and mounted by Compose; `charts/` also holds the vendored `opentelemetry-collector` (alias `collector`), `tempo`, and `grafana` charts, each turned on only by its own Application (`noord-collector`, `noord-tempo`, `noord-grafana`) |
| `slack/manifest.yaml` | the Slack app manifest |

## Commands

```bash
make install CLUSTER=<name>   # check, seal, commit and push the cluster folder, apply, wait (exit 2: preflight stopped, nothing changed)
helm template noord-client deploy/helm/noord -f deploy/clusters/k3sprox/values.yaml -f <sealed stand in> \
  --set component=client --set zammad.enabled=true --skip-tests   # render one part locally
helm dependency update deploy/helm/noord   # after bumping the Zammad version in Chart.yaml; commit Chart.lock and the new archive
```

## Conventions

- Every image is a full SHA tag or a pinned third party tag, never `latest`; every chart version is pinned (`Chart.lock`, `argocd/values.yaml`).
- No `Secret` with data is ever rendered from git: every secret arrives as a `SealedSecret` from `sealed-values.yaml`, except `noord-seed-overlays`, which the installer applies straight to the cluster. Never hand edit `sealed-values.yaml`.
- One Secret per workload, never shared (the table in `files/secrets.yaml`). A key added there also needs its `internal/config` field and both env templates.
- Each workload's pod template carries a checksum of its own sealed block (`noord.sealedChecksum`), so a resealed value rolls only the workloads that read it.
- Sync waves order each Application: `-1` Namespaces, `0` SealedSecrets, ConfigMaps, the databases and Elasticsearch, `1` Samba, Zammad and the migration hook, `2` the binaries and the seed Jobs. Give a new object the wave of what it depends on plus one.
- Seed Jobs are plain Jobs named by a hash of their rendered spec (plus what they read but do not show, such as the sealed block), so they run once per change and never on a self heal. `make resume` bumps `seedRun` to run them again.
- Hooks run only when ArgoCD syncs an Application, which happens only when its inputs change or drift: `noord-migrate` and Zammad's `zammad-init-1` are Sync hooks with `BeforeHookCreation`.
- Every Application has `skipTests: true` (the Zammad chart's Helm test Jobs are not part of the install), the resources finalizer, automated sync with prune and self heal, and `ServerSideApply`.
- The Zammad chart's values live under `zammad:` in `helm/noord/values.yaml`; `zammad.enabled` stays false there and only the `noord-client` Application turns it on. Rendering the client part without it fails with a clear message.
- Pod Security: `noord-agent` is `restricted`, `noord-directory` and `noord-client` are `baseline`. Our own pods run non root with a read only root filesystem and writable `emptyDir`s; no pod may be privileged (the Zammad chart's volume permissions init container is turned off for that reason).

- Observability (spec 0016): `observability.enabled` (default true) adds three Applications in `noord-agent` (Tempo and the Collector wave 1, Grafana wave 2) and, in `noord-agent` itself, the ConfigMaps they mount, their NetworkPolicies, Grafana's tailnet Ingress, and the `noord-grafana` Secret. `noord-pg-grafana` and the managed role `noord_grafana` exist whatever the switch says, because migration 00008 needs the role. Nothing is edited in Grafana's UI.

## Gotchas

- A StatefulSet whose `volumeClaimTemplates` omit `apiVersion` and `kind` stays OutOfSync forever, because Kubernetes fills them in: set both in our templates, and use `ignoreDifferences` for a subchart's (journal 0068).
- A StatefulSet keeps its broken pod during a rollout: after fixing a crashing pod's spec, delete the pod once so the new spec applies (journal 0067).
- A sync that started before a fix stays pinned to the old commit while it waits on an unhealthy resource; terminate it so the automated sync starts again at the new commit.
- The kill switch ConfigMap's `data` is in `ignoreDifferences` with `Prune=false`, so a `kubectl` flip survives self heal (AC-9). Never put its mode in git.
- The Tempo chart's StatefulSet also leaves `apiVersion` and `kind` out of its claim template, so its Application carries the same `ignoreDifferences`. The Tempo chart needs its Jaeger and OpenCensus receiver keys to exist (its Service template reads them), so they stay on; only 4317 and 3200 are reachable.
- A managed CloudNativePG role cannot carry a statement timeout. `noord_grafana` gets its 10s one from `bootstrap.initdb.postInitSQL`, which only runs on a fresh database; an existing one needs the one `ALTER ROLE` in `docs/runbooks/observability.md`.
- CI's runner pods share a node with Longhorn replicas; heavy image builds can starve the control plane's etcd and make the API unavailable for a while (journal 0069).

## Related specs

- [0015 One command GitOps install](../docs/specs/0015-gitops-install/index.md) (AC-9 to AC-15 describe what each part installs)
- [CI runner runbook](../docs/runbooks/ci-runner.md)

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
