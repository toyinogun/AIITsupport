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
	return renderPart(t, componentClient, "--set", "zammad.enabled=true")
}

// renderPart renders one component of our chart for k3sprox, every sealed
// key of its Secrets stood in for (so every optional part renders).
func renderPart(t *testing.T, component string, extra ...string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	tbl := realTable(t)
	sealed := map[string]map[string]string{}
	for _, name := range tbl.Enabled([]string{component}) {
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

	args := append([]string{
		"template", "noord-" + component, filepath.Join(repoRoot, "deploy/helm/noord"), "--skip-tests",
		"-f", filepath.Join(repoRoot, ClustersDir, "k3sprox", valuesFile), "-f", stand,
		"--set", "component=" + component,
	}, extra...)
	cmd := exec.CommandContext(t.Context(), "helm", args...) //nolint:gosec // G204: fixed arguments and a temp file
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

// object is the first rendered object of kind whose name starts with
// prefix, or nil.
func object(docs []map[string]any, kind, prefix string) map[string]any {
	for _, d := range docs {
		meta, _ := d["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if d["kind"] == kind && strings.HasPrefix(name, prefix) {
			return d
		}
	}
	return nil
}

// podSpec is the pod template's spec of a workload object.
func podSpec(d map[string]any) map[string]any {
	spec, _ := d["spec"].(map[string]any)
	tpl, _ := spec["template"].(map[string]any)
	pod, _ := tpl["spec"].(map[string]any)
	return pod
}

// envNames is the env of the named container in the first object of kind
// whose name starts with prefix.
func envNames(t *testing.T, docs []map[string]any, kind, prefix, container string) []string {
	t.Helper()
	if d := object(docs, kind, prefix); d != nil {
		containers, _ := podSpec(d)["containers"].([]any)
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

// covers: AC-6 (spec 0015). No part renders a Secret with data: every
// secret is a SealedSecret (the Zammad chart reads noord-zammad).
func TestPartsRenderNoPlainSecret(t *testing.T) {
	parts := map[string][]map[string]any{
		componentAgent:     renderPart(t, componentAgent),
		componentDirectory: renderPart(t, componentDirectory),
		componentClient:    renderClient(t),
	}
	for part, docs := range parts {
		for _, d := range docs {
			if d["kind"] == "Secret" {
				meta, _ := d["metadata"].(map[string]any)
				t.Errorf("the %s part renders Secret %v", part, meta["name"])
			}
		}
	}
}

// covers: AC-15, AC-24 (spec 0015). The tool server listens for the reset
// page on its own port, which only a tailscale Ingress reaches, and gets
// the Graph certificate as a mounted file, never in its environment.
func TestToolServerResetAndCertificate(t *testing.T) {
	docs := renderPart(t, componentAgent, "--set", "reset.enabled=true") // off on k3sprox until its cutover
	env := envNames(t, docs, "Deployment", "toolserver", "toolserver")
	for _, want := range []string{"RESET_HTTP_ADDR", "RESET_PUBLIC_URL", "GRAPH_CLIENT_CERT", "TOOLSERVER_TOKEN"} {
		if !slices.Contains(env, want) {
			t.Errorf("the tool server's env lacks %s", want)
		}
	}
	for key, k := range realTable(t).Secrets["noord-toolserver"].Keys {
		if k.Mounted() && slices.Contains(env, key) {
			t.Errorf("the mounted key %s is in the tool server's env", key)
		}
	}
	vols, _ := podSpec(object(docs, "Deployment", "toolserver"))["volumes"].([]any)
	var mounted bool
	for _, v := range vols {
		vm, _ := v.(map[string]any)
		sec, _ := vm["secret"].(map[string]any)
		items, _ := sec["items"].([]any)
		for _, it := range items {
			im, _ := it.(map[string]any)
			mounted = mounted || (sec["secretName"] == "noord-toolserver" && im["key"] == "graph-client.pem" && im["path"] == "client.pem")
		}
	}
	if !mounted {
		t.Error("the Graph certificate is not mounted as client.pem")
	}

	var ingresses []string
	for _, d := range docs {
		if d["kind"] != "Ingress" {
			continue
		}
		meta, _ := d["metadata"].(map[string]any)
		ingresses = append(ingresses, meta["name"].(string))
		spec, _ := d["spec"].(map[string]any)
		backend, _ := spec["defaultBackend"].(map[string]any)
		svc, _ := backend["service"].(map[string]any)
		port, _ := svc["port"].(map[string]any)
		if spec["ingressClassName"] != "tailscale" || svc["name"] != "toolserver" || port["number"] != 8081 || spec["rules"] != nil {
			t.Errorf("Ingress %v = %v, want tailscale to the tool server's reset port only", meta["name"], spec)
		}
	}
	if !slices.Equal(ingresses, []string{"reset"}) {
		t.Errorf("the agent part's Ingresses = %v, want only reset", ingresses)
	}
}

// covers: AC-10, AC-24 (spec 0015). seed entra sync runs as its own hashed
// Job one wave after seed directory, from the same Secret.
func TestEntraJobFollowsDirectory(t *testing.T) {
	docs := renderPart(t, componentDirectory)
	dir, entra := object(docs, "Job", "seed-directory-"), object(docs, "Job", "seed-entra-")
	if dir == nil || entra == nil {
		t.Fatal("the directory part lacks the seed-directory or seed-entra Job")
	}
	wave := func(d map[string]any) any {
		meta, _ := d["metadata"].(map[string]any)
		ann, _ := meta["annotations"].(map[string]any)
		return ann["argocd.argoproj.io/sync-wave"]
	}
	if wave(dir) != "2" || wave(entra) != "3" {
		t.Errorf("waves = %v and %v, want 2 then 3", wave(dir), wave(entra))
	}
	containers, _ := podSpec(entra)["containers"].([]any)
	c, _ := containers[0].(map[string]any)
	if args, _ := c["args"].([]any); len(args) != 2 || args[0] != "entra" || args[1] != "sync" {
		t.Errorf("seed-entra args = %v, want entra sync", c["args"])
	}
}
