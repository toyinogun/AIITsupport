package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// backupOn renders with backups on, the way a cluster folder turns them on.
var backupOn = []string{
	"--set", "backup.enabled=true",
	"--set", "backup.endpoint=https://abc123.eu.r2.cloudflarestorage.com",
	"--set", "backup.bucket=noord-backups",
}

// dig walks nested maps and lists by key or index.
func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

// covers: AC-18 (spec 0015). With backups off nothing backup related renders;
// with them on, the ObjectStore points at <bucket>/<cluster>/ with sealed
// credentials, the Cluster archives WAL through the plugin, and a daily
// ScheduledBackup runs at once.
func TestBackupObjects(t *testing.T) {
	off := renderPart(t, componentAgent)
	for _, kind := range []string{"ObjectStore", "ScheduledBackup"} {
		if object(off, kind, "") != nil {
			t.Errorf("with backups off the agent part renders a %s", kind)
		}
	}
	if object(off, "SealedSecret", "noord-backup") != nil || dig(object(off, "Cluster", "noord-pg"), "spec", "plugins") != nil {
		t.Error("with backups off the agent part renders noord-backup or a Cluster plugin")
	}

	on := renderPart(t, componentAgent, backupOn...)
	store := object(on, "ObjectStore", "noord-backup")
	if store == nil {
		t.Fatal("no ObjectStore noord-backup")
	}
	conf := dig(store, "spec", "configuration")
	if dig(conf, "destinationPath") != "s3://noord-backups/k3sprox/" || dig(conf, "endpointURL") != "https://abc123.eu.r2.cloudflarestorage.com" ||
		dig(store, "spec", "retentionPolicy") != "14d" {
		t.Errorf("ObjectStore = %v", dig(store, "spec"))
	}
	for _, k := range []string{"accessKeyId", "secretAccessKey", "region"} {
		if dig(conf, "s3Credentials", k, "name") != "noord-backup" {
			t.Errorf("s3Credentials.%s does not read noord-backup", k)
		}
	}
	if object(on, "SealedSecret", "noord-backup") == nil {
		t.Error("no SealedSecret noord-backup")
	}
	plugin := dig(object(on, "Cluster", "noord-pg"), "spec", "plugins", 0)
	if dig(plugin, "name") != "barman-cloud.cloudnative-pg.io" || dig(plugin, "isWALArchiver") != true || dig(plugin, "parameters", "barmanObjectName") != "noord-backup" {
		t.Errorf("Cluster plugin = %v, want the Barman plugin archiving WAL to noord-backup", plugin)
	}
	sb := object(on, "ScheduledBackup", "noord-pg-daily")
	if dig(sb, "spec", "method") != "plugin" || dig(sb, "spec", "immediate") != true || dig(sb, "spec", "schedule") != "0 0 2 * * *" ||
		dig(sb, "spec", "pluginConfiguration", "name") != "barman-cloud.cloudnative-pg.io" {
		t.Errorf("ScheduledBackup = %v", dig(sb, "spec"))
	}
}

// covers: AC-14, AC-18 (spec 0015). With backups on the database may reach
// the plugin on 9090 and, with Cilium FQDN rules, only the bucket's host
// on 443.
func TestBackupNetwork(t *testing.T) {
	off := renderPart(t, componentAgent)
	if object(off, "CiliumNetworkPolicy", "noord-pg-https") != nil {
		t.Error("with backups off the database has a 443 allow")
	}
	on := renderPart(t, componentAgent, backupOn...)
	if got := fqdns(object(on, "CiliumNetworkPolicy", "noord-pg-https")); !slices.Equal(got, []string{"abc123.eu.r2.cloudflarestorage.com"}) {
		t.Errorf("noord-pg-https hosts = %v, want only the endpoint's host", got)
	}
	egress, _ := dig(object(on, "NetworkPolicy", "noord-pg"), "spec", "egress").([]any)
	var plugin bool
	for _, e := range egress {
		plugin = plugin || (dig(e, "to", 0, "podSelector", "matchLabels", "app.kubernetes.io/name") == "plugin-barman-cloud" && dig(e, "ports", 0, "port") == 9090)
	}
	if !plugin {
		t.Error("the database cannot reach the Barman plugin on 9090")
	}
}

// covers: AC-18 (spec 0015). The backup Secret and its settings apply only
// with backups on.
func TestBackupSecretOnlyWithBackups(t *testing.T) {
	tbl := realTable(t)
	if slices.Contains(tbl.Active([]string{componentAgent}, Conditions{}), "noord-backup") {
		t.Error("noord-backup is active with backups off")
	}
	off := tbl.Required([]string{componentAgent}, Conditions{})
	on := tbl.Required([]string{componentAgent}, Conditions{Backup: true})
	for _, name := range []string{"BACKUP_S3_ACCESS_KEY_ID", "BACKUP_S3_SECRET_ACCESS_KEY", "BACKUP_S3_REGION"} {
		if slices.Contains(off, name) || !slices.Contains(on, name) {
			t.Errorf("%s required: off %v, on %v; want only on", name, slices.Contains(off, name), slices.Contains(on, name))
		}
	}
}

// covers: AC-2, AC-18 (spec 0015). backup.enabled without the endpoint or
// bucket stops the preflight, as does a missing bucket token.
func TestPreflightNeedsTheBucket(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, ClustersDir, "k3sprox", valuesFile)
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path in t.TempDir
	must(t, err)
	must(t, os.WriteFile(path, append(raw, []byte("backup: {enabled: true}\n")...), 0o600)) //nolint:gosec // a fixed path in t.TempDir
	err = Run(t.Context(), f.options())
	exit, ok := errors.AsType[*ExitError](err)
	if !ok || exit.Code != ExitPreflight {
		t.Fatalf("Run = %v, want exit 2", err)
	}
	for _, want := range []string{"backup.endpoint and backup.bucket", "BACKUP_S3_ACCESS_KEY_ID is not set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

// covers: AC-4 (spec 0015). With the Barman plugin on, the platform step
// waits for the plugin's own Application, not only its parent, and accepts
// it at its chart's version: an operator Application is never at a commit.
func TestPlatformWaitsForThePlugin(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, ClustersDir, "k3sprox", valuesFile)
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path in t.TempDir
	must(t, err)
	values := strings.Replace(string(raw), "platform: {sealedSecrets:", "platform: {barmanPlugin: {enabled: true}, sealedSecrets:", 1)
	must(t, os.WriteFile(path, []byte(values), 0o600)) //nolint:gosec // a fixed path in t.TempDir
	s := &state{Options: f.options().Defaults()}
	must(t, s.preflight(t.Context()))
	// A short deadline, so a wait that never ends fails here instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := s.platform(ctx); err != nil {
		t.Fatalf("platform: %v", err)
	}
	first := f.run.calls[slices.IndexFunc(f.run.calls, func(c Cmd) bool { return slices.Contains(c.Args, "applications.argoproj.io") })]
	if !slices.Contains(first.Args, "noord-k3sprox-barman") || !slices.Contains(first.Args, "noord-k3sprox-platform") {
		t.Errorf("the platform wait read %v, want the platform and the plugin Applications", first.Args)
	}
}

// covers: AC-4 (spec 0015). The platform AppProject syncs before every
// operator Application, since ArgoCD waits for each one's health and one
// whose repo the project does not yet permit never turns healthy.
func TestPlatformProjectSyncsFirst(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	out, err := exec.CommandContext(t.Context(), "helm", "template", "p", filepath.Join(repoRoot, "deploy/argocd"), //nolint:gosec // G204: fixed arguments
		"-f", filepath.Join(repoRoot, ClustersDir, "k3sprox", valuesFile), "--set", "part=platform").Output()
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	project, lowest := 0, 0
	var apps int
	for doc := range strings.SplitSeq(string(out), "\n---") {
		var d map[string]any
		must(t, yaml.Unmarshal([]byte(doc), &d))
		wave, _ := strconv.Atoi(fmt.Sprint(dig(d, "metadata", "annotations", "argocd.argoproj.io/sync-wave")))
		switch d["kind"] {
		case "AppProject":
			project = wave
		case "Application":
			apps++
			lowest = min(lowest, wave)
		}
	}
	if apps == 0 {
		t.Fatal("k3sprox's platform part renders no operator Application")
	}
	if project >= lowest {
		t.Errorf("AppProject wave %d, want below every operator Application (lowest %d)", project, lowest)
	}
}
