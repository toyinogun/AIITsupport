package helpdesk

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// servicesOf reads a Compose file's services as YAML nodes, so custom tags
// such as !override survive.
func servicesOf(t *testing.T, path string) map[string]*yaml.Node {
	// yaml.v3 fills map[string]yaml.Node values but leaves *yaml.Node empty.
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: test fixture
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out := make(map[string]*yaml.Node, len(doc.Services))
	for name, n := range doc.Services {
		out[name] = &n
	}
	return out
}

// field returns a mapping node's value for key.
func field(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalars(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	var out []string
	for _, c := range n.Content {
		out = append(out, c.Value)
	}
	return out
}

// covers: AC-1, AC-15. Every vendored Zammad service is in the helpdesk
// profile (backup in an unused one), only nginx publishes a port, on
// loopback, and our files are mounted read only.
func TestZammadCompose(t *testing.T) {
	vendored := servicesOf(t, helpdeskDir+"/zammad/compose.yaml")
	override := servicesOf(t, helpdeskDir+"/zammad/override.yaml")

	for name := range vendored {
		o, ok := override[name]
		if !ok {
			t.Errorf("service %s has no entry in override.yaml, so it would start with make dev", name)
			continue
		}
		want := []string{"helpdesk"}
		if name == "zammad-backup" {
			want = []string{"disabled"}
		}
		if got := scalars(field(o, "profiles")); !slices.Equal(got, want) {
			t.Errorf("%s profiles = %v, want %v", name, got, want)
		}
	}
	for name := range override {
		if _, ok := vendored[name]; !ok {
			t.Errorf("override.yaml names %s, which the vendored file lacks", name)
		}
	}

	for name, o := range override {
		ports := field(o, "ports")
		if name != "zammad-nginx" {
			if ports != nil {
				t.Errorf("%s publishes ports %v", name, scalars(ports))
			}
			if vp := field(vendored[name], "ports"); vp != nil {
				t.Errorf("vendored %s publishes ports %v and the override does not replace them", name, scalars(vp))
			}
			continue
		}
		if ports == nil {
			t.Error("nginx has no ports in override.yaml")
			continue
		}
		if ports.Tag != "!override" || !slices.Equal(scalars(ports), []string{"127.0.0.1:58080:8080"}) {
			t.Errorf("nginx ports = %v (tag %q), want !override [127.0.0.1:58080:8080]", scalars(ports), ports.Tag)
		}
	}

	for _, name := range []string{"zammad-railsserver", "zammad-scheduler"} {
		vols := scalars(field(override[name], "volumes"))
		want := []string{"../rails:/opt/noord-seed/rails:ro", "../../../.dev/certs/ca.pem:/opt/noord-seed/ca.pem:ro"}
		if !slices.Equal(vols, want) {
			t.Errorf("%s volumes = %v, want %v", name, vols, want)
		}
	}

	raw, err := os.ReadFile(helpdeskDir + "/zammad/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	header := strings.Join(strings.SplitN(string(raw), "\n", 4)[:3], "\n")
	if !strings.Contains(header, "commit") || !strings.Contains(header, "zammad-docker-compose") {
		t.Errorf("vendored compose.yaml header must name the source and commit:\n%s", header)
	}

	env, err := os.ReadFile(helpdeskDir + "/zammad/zammad.env")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "VERSION=7.2.2-") {
		t.Errorf("zammad.env does not pin Zammad 7.2.2")
	}
}

// covers: AC-1. The root compose.yaml pulls Zammad in through include with
// its own env file, never .env.
func TestRootComposeIncludesZammad(t *testing.T) {
	raw, err := os.ReadFile(repoRoot + "/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Include []struct {
			Path    []string `yaml:"path"`
			EnvFile string   `yaml:"env_file"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Include) != 1 || doc.Include[0].EnvFile != "seed/helpdesk/zammad/zammad.env" ||
		!slices.Equal(doc.Include[0].Path, []string{"seed/helpdesk/zammad/compose.yaml", "seed/helpdesk/zammad/override.yaml"}) {
		t.Errorf("include = %+v", doc.Include)
	}
}
