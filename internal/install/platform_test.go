package install

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// covers: AC-4 (spec 0015). With every operator on, the platform part renders
// one pinned Application each, in the waves the spec names, and the project
// permits every chart repository.
func TestPlatformOperators(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	out, err := exec.CommandContext(t.Context(), "helm", "template", "p", filepath.Join(repoRoot, "deploy/argocd"), //nolint:gosec // G204: fixed arguments
		"-f", filepath.Join(repoRoot, ClustersDir, "k3sprox", valuesFile), "--set", "part=platform",
		"--set", "platform.sealedSecrets.enabled=true,platform.certManager.enabled=true,platform.cnpg.enabled=true,platform.tailscale.enabled=true",
	).Output()
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	waves := map[string]string{}
	var project map[string]any
	for doc := range strings.SplitSeq(string(out), "\n---") {
		var d map[string]any
		must(t, yaml.Unmarshal([]byte(doc), &d))
		meta, _ := d["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		switch d["kind"] {
		case "Application":
			waves[strings.TrimPrefix(name, "noord-k3sprox-")] = fmt.Sprint(dig(d, "metadata", "annotations", "argocd.argoproj.io/sync-wave"))
			if v := fmt.Sprint(dig(d, "spec", "source", "targetRevision")); v == "" || v == "main" || v == "<nil>" {
				t.Errorf("%s is not pinned (targetRevision %q)", name, v)
			}
		case "AppProject":
			project = d
		}
	}
	want := map[string]string{"sealed-secrets": "-30", "cert-manager": "-30", "cnpg": "-20", "barman": "-10", "tailscale": "-10"}
	for name, wave := range want {
		if waves[name] != wave {
			t.Errorf("%s wave = %q, want %s", name, waves[name], wave)
		}
	}
	repos := fmt.Sprint(dig(project, "spec", "sourceRepos"))
	for _, r := range []string{"bitnami.github.io", "charts.jetstack.io", "cloudnative-pg.github.io", "pkgs.tailscale.com"} {
		if !strings.Contains(repos, r) {
			t.Errorf("the project does not permit %s: %s", r, repos)
		}
	}
}

// covers: AC-4, AC-6 (spec 0015). The Tailscale operator's OAuth Secret is
// rendered, sealed, only with platform.tailscale.enabled.
func TestTailscaleSecretOnlyWithTailscale(t *testing.T) {
	if object(renderPart(t, componentAgent), "SealedSecret", "operator-oauth") != nil {
		t.Error("operator-oauth renders with Tailscale off")
	}
	d := object(renderPart(t, componentAgent, "--set", "platform.tailscale.enabled=true"), "SealedSecret", "operator-oauth")
	if d == nil {
		t.Fatal("operator-oauth is missing with Tailscale on")
	}
	if ns := fmt.Sprint(dig(d, "metadata", "namespace")); ns != "tailscale" {
		t.Errorf("namespace = %s, want tailscale", ns)
	}
	if enc := fmt.Sprint(dig(d, "spec", "encryptedData")); !strings.Contains(enc, "client_id") || !strings.Contains(enc, "client_secret") {
		t.Errorf("encryptedData = %s, want client_id and client_secret", enc)
	}
}
