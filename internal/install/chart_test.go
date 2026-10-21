package install

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/config"
)

// renderClient renders our chart's client part for k3sprox with a stand in
// for every sealed key, or skips when helm is not installed.
func renderClient(t *testing.T) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	tbl := realTable(t)
	sealed := map[string]map[string]string{}
	for _, name := range tbl.Enabled([]string{componentClient}) {
		sealed[name] = map[string]string{}
		for key := range tbl.Secrets[name].Keys {
			sealed[name][key] = "AgB-stand-in"
		}
	}
	raw, err := yaml.Marshal(map[string]any{"sealed": sealed})
	if err != nil {
		t.Fatal(err)
	}
	stand := filepath.Join(t.TempDir(), "sealed.yaml")
	must(t, os.WriteFile(stand, raw, 0o600))

	cmd := exec.CommandContext(t.Context(), "helm", "template", "noord-client", filepath.Join(repoRoot, "deploy/helm/noord"), //nolint:gosec // G204: fixed arguments and a temp file
		"--namespace", "noord-client", "--skip-tests",
		"-f", filepath.Join(repoRoot, ClustersDir, "k3sprox", valuesFile), "-f", stand,
		"--set", "component="+componentClient, "--set", "zammad.enabled=true")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr.String())
	}
	var docs []map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
	return docs
}

// envNames is the env of the named container in the first object of kind
// whose name starts with prefix.
func envNames(t *testing.T, docs []map[string]any, kind, prefix, container string) []string {
	t.Helper()
	for _, d := range docs {
		meta, _ := d["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if d["kind"] != kind || !strings.HasPrefix(name, prefix) {
			continue
		}
		spec, _ := d["spec"].(map[string]any)
		tpl, _ := spec["template"].(map[string]any)
		pod, _ := tpl["spec"].(map[string]any)
		containers, _ := pod["containers"].([]any)
		for _, c := range containers {
			cm, _ := c.(map[string]any)
			if cm["name"] != container {
				continue
			}
			var names []string
			env, _ := cm["env"].([]any)
			for _, e := range env {
				em, _ := e.(map[string]any)
				names = append(names, em["name"].(string))
			}
			slices.Sort(names)
			return names
		}
	}
	t.Fatalf("no %s %s* with a container %s", kind, prefix, container)
	return nil
}

// covers: AC-11 (spec 0015). The helpdesk seed Job runs Rails with the same
// database, Redis, and Memcached environment as the chart's Rails pods: it
// has every variable the railsserver container has, and nothing else but
// our own settings.
func TestHelpdeskJobHasZammadRailsEnv(t *testing.T) {
	docs := renderClient(t)
	rails := envNames(t, docs, "Deployment", "zammad-railsserver", "zammad-railsserver")
	job := envNames(t, docs, "Job", "seed-helpdesk-", "seed")
	known := config.Names()
	for _, name := range rails {
		if !slices.Contains(job, name) {
			t.Errorf("the seed Job lacks %s, which Zammad's Rails pods have", name)
		}
	}
	for _, name := range job {
		if !slices.Contains(rails, name) && !slices.Contains(known, name) {
			t.Errorf("the seed Job sets %s, neither a Rails variable nor one of our settings", name)
		}
	}
}

// covers: AC-6 (spec 0015). The client part renders no Secret with data:
// the Zammad chart reads noord-zammad, a SealedSecret.
func TestClientRendersNoPlainSecret(t *testing.T) {
	for _, d := range renderClient(t) {
		if d["kind"] == "Secret" {
			meta, _ := d["metadata"].(map[string]any)
			t.Errorf("the client part renders Secret %v", meta["name"])
		}
	}
}
