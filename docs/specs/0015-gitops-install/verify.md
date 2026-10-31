# Verify: One command GitOps install · spec 0015 · updated 2026-10-22
_Steps derived from spec 0015 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

## Task 4: reset page and Entra

### Commands
- [x] `go test -race -run 'TestGraphCertificateOrSecret|TestLoadGraph|TestMFAEnabled' ./internal/config/` → pass; with both `GRAPH_CLIENT_SECRET` and `GRAPH_CLIENT_CERT` set, `LoadGraph` and `LoadToolServer` fail naming both, never the value → AC-24
- [x] `go test -race -run TestNewWithCredentialsCertificate ./internal/graph/` → a PEM with a certificate and its RSA key builds a client; a missing or bad file fails naming `GRAPH_CLIENT_ID` → AC-24
- [x] `go test -race -run 'TestBuildReset|TestValuesReset|TestBuildGraphCert|TestPreflightGraphCert|TestTableCheckRefusesBadSources' ./internal/install/` → pass → AC-15, AC-24
- [x] `go test -race -run 'TestToolServerResetAndCertificate|TestEntraJobFollowsDirectory|TestPartsRenderNoPlainSecret|TestSecretKeysMatchConfigTags|TestTemplateSecretsEnvMatches' ./internal/install/` (needs `helm`) → pass → AC-6, AC-7, AC-10, AC-15, AC-24
- [ ] `helm template noord-agent deploy/helm/noord -f deploy/clusters/k3sprox/values.yaml -f <stand in> --set component=agent --set reset.enabled=true --skip-tests` → exactly one Ingress, `reset`, class `tailscale`, backend `toolserver` port 8081, host `noord-k3sprox-reset`; no Service of type LoadBalancer or NodePort → AC-15
- [ ] Same render with `graph-client.pem` in the stand in → the tool server has `GRAPH_CLIENT_CERT=/etc/noord/graph/client.pem`, a `graph` volume with item `graph-client.pem` → `client.pem`, and no env var named `graph-client.pem` → AC-24
- [ ] Render the directory part with `SEED_GRAPH_CLIENT_ID` in the `noord-seed-directory` stand in → `seed-entra-<hash>` Job at wave 3 running `entra sync`; without it, no such Job; `seed-directory-<hash>` keeps its hash from before this change → AC-10
- [ ] With every part rendered from k3sprox's real `sealed-values.yaml` (reset off there), `helm template` succeeds for agent, directory, and client → nothing breaks on merge

### Value sourcing
- [ ] `RESET_PUBLIC_URL` is `https://<reset host>.<TAILNET>`: change `TAILNET` in a test env and the sealed value follows; set `reset.tailscaleHost` and the host follows; leave `TAILNET` blank with reset on and preflight names `TAILNET` → AC-15
- [ ] `RESET_LDAP_BIND_PASSWORD` is generated once in demo mode and is the same value in `noord-toolserver` and in `noord-samba` (`SAMBA_PWRESET_PASSWORD`); with demo off it is required from `secrets.env` → AC-5, AC-15
- [ ] `RESET_HTTP_ADDR` is the chart's `:8081` (`reset.port`), and the Service and Ingress use the same port → AC-15
- [ ] `GRAPH_CLIENT_CERT` content comes from the file `GRAPH_CERT_FILE` names (relative paths under the clone); setting `GRAPH_CLIENT_SECRET` too stops preflight with exit 2 → AC-24

### Live on k3sprox (the cutover, after the merge and CI images)
- [ ] Add the certificate to the tool server's Entra app, set `GRAPH_TENANT_ID`, `GRAPH_CLIENT_ID`, `GRAPH_CERT_FILE` in `secrets.env` (no `GRAPH_CLIENT_SECRET`), `make install CLUSTER=k3sprox` → the tool server boots and `make entra-mfa NAME=<user>` lists methods with the certificate → AC-24
- [ ] Delete the tool server app's client secret in Entra; the tool server still reads MFA methods → AC-24
- [ ] Set `SEED_GRAPH_*` and `SEED_ENTRA_DOMAIN`, `make install` → `seed-entra-<hash>` completes after `seed-directory-<hash>`; a second `make install` starts no new Job → AC-8, AC-10
- [ ] Set `reset.enabled: true` and `RESET_LDAP_BIND_DN`, `make install` (seals the svc-pwreset password); then delete the `samba-data` PVC and the Samba pod so Samba provisions again with svc-pwreset; once ArgoCD rebuilds them, bump `seedRun` in the cluster's `values.yaml` by hand (`make resume` is task 9) and `make install` again → `https://noord-k3sprox-reset.<TAILNET>` serves the reset page on the tailnet, and `/mcp` through that host is 404 or unreachable → AC-15
- [ ] One locked out user DM in Slack, end to end in the cluster (spec 0008's flow), and one reset link opened through the tailnet URL → AC-10, AC-15

## Tasks 5 and 6: network policies, readiness, and smoke

### Commands
- [x] `go test -race -run 'TestEveryPartDeniesByDefault|TestFQDNEgressNarrowsHTTPS|TestHTTPSWithoutCilium|TestResetPortOnlyFromTailnetProxy' ./internal/install/` (needs `helm`) → pass: every part has `default-deny` and `dns`; with `cilium.fqdnEgress` no NetworkPolicy opens 443 to `0.0.0.0/0`; without Cilium no Cilium kind renders; only the reset proxy reaches 8081 → AC-14, AC-15
- [ ] `helm template` of the agent part with `cilium.fqdnEgress=true` and `cilium.enabled=false` → fails naming `cilium.enabled` → AC-14
- [x] `go test -race ./internal/readiness/ ./cmd/agent/ ./cmd/toolserver/` → pass: 200 only when every configured check is ok, `not_configured` never fails the report, a slow check is `deadline`, no error text in the body, the tool server's report is 401 without the token → AC-16
- [x] `go test -race -run 'TestPingGetsAToken|TestErrorCause|TestPingErrorCause|TestConnectedFollowsSocketEvents' ./internal/graph/ ./internal/zammad/ ./internal/ldap/ ./internal/slack/` → the cause tokens `graph_token`, `zammad_<status>`, `ldap_<code>`, `slack_not_connected` → AC-16
- [x] `DOCKER_AUTH_CONFIG='{"auths":{}}' TEST_SAMBA=1 go test -race -timeout 25m -run TestLivePing ./internal/ldap/` → a bind as svc-itsupport passes; a wrong password is `ldap_49` → AC-16
- [x] `go test -race -run 'TestSmoke|TestLoadSmoke' ./cmd/seed/ ./internal/config/` and `go test -race -run 'TestRunEndsWithTheSmokeCheck|TestFailedSmokeFailsTheRun|TestSmokeAlone|TestReportNamesTheURLs' ./internal/install/` → pass: one line per check, fixed tokens only, a failed Job fails the install at `smoke`, `make smoke` refuses the wrong kube context with exit 2 → AC-1, AC-17
- [ ] With `TEST_DATABASE_URL` set, `go test -race -run TestRunMigratesThenIsANoOp ./internal/migrate/` → `Current` equals `Latest` after the run → AC-17

### Value sourcing
- [ ] `cluster.nodeCIDR` and `cluster.podCIDR`/`serviceCIDR` are the `except` ranges of every `*-https` NetworkPolicy when Cilium is off: change one in a test values file and the render follows → AC-14
- [ ] `cluster.apiServerCIDR` is the `noord-pg` NetworkPolicy's only 6443 peer; on k3sprox (`cilium.enabled: true`) the `noord-pg-kube-api` CiliumNetworkPolicy (`toEntities: [kube-apiserver]`) also renders, and the database stays Healthy with the default deny in place (journal 0072) → AC-14
- [ ] `tailscale.proxyNamespace` is the namespace of the proxy peers in `toolserver` (reset) and `zammad-web` (helpdesk); a proxy for another Ingress in that namespace cannot reach either → AC-14, AC-15
- [ ] The smoke Job reads `TOOLSERVER_TOKEN` and `MIGRATE_DATABASE_URL` from `noord-smoke` only, and `SMOKE_AGENT_URL`, `TOOLSERVER_URL` from the chart → AC-17

### Live on k3sprox (after the merge; `make install CLUSTER=k3sprox` seals `noord-smoke`)
- [x] The merge alone leaves `noord-agent` with a render error until `make install` seals `noord-smoke`; the running pods are untouched meanwhile, then every Application is Synced and Healthy and the install ends with four `pass` lines → AC-1, AC-17
- [x] From a debug pod in `noord-agent` labelled `app.kubernetes.io/name: agent` (with a readiness probe that always fails, so no Service routes to it): `toolserver:8080` and `noord-pg-rw:5432` open; Samba 636, `zammad-nginx` 8080, and `example.com:443` blocked; `api.anthropic.com:443` open → AC-14
- [x] The same as `toolserver`: Samba 636, `zammad-nginx` 8080, `graph.microsoft.com:443` open; `slack.com:443` and `agent:8080` blocked → AC-14
- [x] `noord-pg` stays Healthy for 10 minutes with the policies in place (the instance manager reaches the API through the entity rule) → AC-14
- [x] The reset page and the helpdesk still open through their tailnet URLs; `/mcp` through the reset host is 404 or unreachable → AC-15
- [ ] Stop Samba (scale the Deployment to 0): `make smoke CLUSTER=k3sprox` prints `fail toolserver_readyz http_503 ldap=network` (or `ldap=deadline`) and exits 1; scale it back and it passes → AC-16, AC-17
- [x] `kubectl get svc,ingress -A` shows no LoadBalancer, NodePort, or non tailscale Ingress for the install → AC-15

## Task 7: backups

### Commands
- [x] `go test -race -run 'TestBackupObjects|TestBackupNetwork|TestBackupSecretOnlyWithBackups|TestPreflightNeedsTheBucket|TestPlatformWaitsForThePlugin' ./internal/install/` (needs `helm`) → pass: nothing backup related renders with backups off; with them on, the ObjectStore at `s3://<bucket>/<cluster>/` reads `noord-backup`, the Cluster archives WAL through the plugin, the daily ScheduledBackup runs at once, the database reaches only the plugin on 9090 and the bucket's host on 443; preflight names `backup.endpoint and backup.bucket` and the token; the platform step waits for `noord-<cluster>-barman` → AC-2, AC-4, AC-14, AC-18
- [ ] `helm template p deploy/argocd -f deploy/clusters/k3sprox/values.yaml --set part=platform` → an Application `noord-k3sprox-barman`, chart `plugin-barman-cloud` 0.8.1 into `cnpg-system`, wave minus 10; the AppProject `noord-platform` lists the chart repo → AC-4

### Value sourcing
- [ ] `backup.endpoint` and `backup.bucket` are committed values (the ObjectStore takes them only as plain fields, journal 0073); `BACKUP_S3_ACCESS_KEY_ID`, `BACKUP_S3_SECRET_ACCESS_KEY`, `BACKUP_S3_REGION` (default `auto`) are sealed in `noord-backup` and nowhere else → AC-6, AC-18
- [ ] The bucket path follows `cluster.name`: render with another name and `destinationPath` follows; the FQDN rule's host follows `backup.endpoint` → AC-18

### Live on k3sprox (after the merge)
- [x] With `platform.barmanPlugin.enabled: true`, `make install CLUSTER=k3sprox` → `noord-k3sprox-barman` Synced and Healthy, the `barman-cloud` Deployment Available in `cnpg-system`, its certificates issued by cert-manager → AC-4
- [x] Create the R2 bucket in the EU jurisdiction and a token scoped to it; put the token in `secrets.env`, set `backup.enabled: true`, `backup.endpoint`, `backup.bucket` in values.yaml; `make install CLUSTER=k3sprox` → `noord-pg` restarts once with the plugin sidecar and stays Healthy → AC-18
- [ ] `kubectl -n noord-agent get backups` → the first Backup reaches phase `completed`; `kubectl -n noord-agent get cluster noord-pg -o jsonpath='{.status.conditions[?(@.type=="ContinuousArchiving")].status}'` → `True`; the bucket holds `k3sprox/noord-pg/` with `base/` and `wals/` → AC-18
- [ ] A second `make install` with nothing changed starts no new Backup and restarts nothing → AC-8

## Acceptance-criteria coverage
- AC-1: the smoke step and the URL report (commands, live)
- AC-2: the backup preflight (commands)
- AC-4: the Barman plugin Application and its wait (commands, live)
- AC-5: generated `RESET_LDAP_BIND_PASSWORD` (value sourcing)
- AC-6: no plain Secret in any part (commands)
- AC-7: table and template drift tests (commands)
- AC-8: rerun starts no new seed Job (live)
- AC-10: `seed-entra` Job order and hash (commands, live)
- AC-14: default deny, the allow list, FQDN egress, the kube API entity rule (commands, value sourcing, live)
- AC-15: reset Ingress, port, per cluster host, URL from `TAILNET` (commands, value sourcing, live)
- AC-16: both `/readyz`, the cause tokens (commands, live)
- AC-17: `seed smoke`, the CronJob, `make smoke` (commands, value sourcing, live)
- AC-18: ObjectStore, WAL archiving, ScheduledBackup, the first completed backup (commands, value sourcing, live)
- AC-24: certificate credential, exclusivity, mount, cutover (commands, value sourcing, live)

## Task 9: day two commands

- [ ] `go test -race -run 'TestRelease|TestPauseAndResume|TestUninstall|TestSealKeyBackup|TestPausedChart' ./internal/install/` → release writes `image.tag` only when all four images exist, pause waits for the agent pod, resume bumps `seedRun`, uninstall wants the name and refuses an owned Application, the key backup is mode 0600 and the install warns on a mismatch, a paused chart has no agent replicas and no seed Job → AC-19 to AC-22
- [ ] Live on k3sprox: `make pause CLUSTER=k3sprox` then `make resume CLUSTER=k3sprox` → the agent pod goes and comes back, `seed-*` Jobs run once more, `make smoke` passes → AC-20
- [ ] `make seal-key-backup CLUSTER=k3sprox` → `~/.noord/k3sprox/sealing-key.yaml` with mode 0600 → AC-22

## Task 8: fresh cluster path

- [ ] `go test -race -run 'TestArgoCDInstall|TestReadArgoPin|TestMemoryCheck|TestPlatformWait|TestPullRequestPath|TestResealKeepsRecorded|TestClusterInit|TestBootstrapMatchesK3sprox|TestPlatformOperators|TestTailscaleSecret' ./internal/install/` → ArgoCD installs at the pinned version only when missing, the demo memory check, Tailscale set aside in the platform wait, a protected branch gives a pull request and exit 3 and a rerun does not seal again, cluster-init reads ranges and operators from the cluster and never overwrites, all five operators render pinned in waves -30, -30, -20, -10, -10 → AC-2 to AC-5
- [ ] `make cluster-init CLUSTER=scratch` on k3sprox's context, then compare `deploy/clusters/scratch/values.yaml` with k3sprox's: same CIDRs, Cilium on, the four operators off, tag a commit with all four images; delete the folder → AC-19
- [ ] On the proof VM (task 12): `make install CLUSTER=proofvm` installs ArgoCD, then Sealed Secrets, cert-manager, CNPG, the Barman plugin and Tailscale in order, and ends with the smoke check → AC-1, AC-3, AC-4

