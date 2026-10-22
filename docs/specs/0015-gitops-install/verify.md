# Verify: One command GitOps install · spec 0015 · updated 2026-10-22
_Steps derived from spec 0015 acceptance criteria. `/check verify` runs these; `/test` locks the durable ones._

## Task 4: reset page and Entra

### Commands
- [ ] `go test -race -run 'TestGraphCertificateOrSecret|TestLoadGraph|TestMFAEnabled' ./internal/config/` → pass; with both `GRAPH_CLIENT_SECRET` and `GRAPH_CLIENT_CERT` set, `LoadGraph` and `LoadToolServer` fail naming both, never the value → AC-24
- [ ] `go test -race -run TestNewWithCredentialsCertificate ./internal/graph/` → a PEM with a certificate and its RSA key builds a client; a missing or bad file fails naming `GRAPH_CLIENT_ID` → AC-24
- [ ] `go test -race -run 'TestBuildReset|TestValuesReset|TestBuildGraphCert|TestPreflightGraphCert|TestTableCheckRefusesBadSources' ./internal/install/` → pass → AC-15, AC-24
- [ ] `go test -race -run 'TestToolServerResetAndCertificate|TestEntraJobFollowsDirectory|TestPartsRenderNoPlainSecret|TestSecretKeysMatchConfigTags|TestTemplateSecretsEnvMatches' ./internal/install/` (needs `helm`) → pass → AC-6, AC-7, AC-10, AC-15, AC-24
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

## Acceptance-criteria coverage
- AC-5: generated `RESET_LDAP_BIND_PASSWORD` (value sourcing)
- AC-6: no plain Secret in any part (commands)
- AC-7: table and template drift tests (commands)
- AC-8: rerun starts no new seed Job (live)
- AC-10: `seed-entra` Job order and hash (commands, live)
- AC-15: reset Ingress, port, per cluster host, URL from `TAILNET` (commands, value sourcing, live)
- AC-24: certificate credential, exclusivity, mount, cutover (commands, value sourcing, live)
