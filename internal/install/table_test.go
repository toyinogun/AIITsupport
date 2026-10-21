package install

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/config"
)

const repoRoot = "../.."

func realTable(t *testing.T) Table {
	t.Helper()
	tbl, err := LoadTable(filepath.Join(repoRoot, TablePath))
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}
	return tbl
}

// loaders maps a table loader to the config struct that workload reads.
var loaders = map[string]any{
	"agent":        &config.Agent{},
	"toolserver":   &config.ToolServer{},
	"migrate":      &config.Migrate{},
	"seed":         &config.Seed{},
	"seedhelpdesk": &config.SeedHelpdesk{},
}

// covers: AC-7 (spec 0015). The keys the chart puts in a workload's Secret
// are exactly the secret and private fields its loader reads.
func TestSecretKeysMatchConfigTags(t *testing.T) {
	tbl := realTable(t)
	seen := map[string]bool{}
	for name, s := range tbl.Secrets {
		if s.Loader == "" {
			continue
		}
		cfg, ok := loaders[s.Loader]
		if !ok {
			t.Errorf("%s names loader %q, which this test does not know", name, s.Loader)
			continue
		}
		seen[s.Loader] = true
		var got []string
		for key, k := range s.Keys {
			if !k.Rails {
				got = append(got, key)
			}
		}
		slices.Sort(got)
		want := config.Sealed(cfg)
		for _, k := range want {
			if !slices.Contains(got, k) {
				t.Errorf("%s lacks %s, a secret or private setting %s reads", name, k, s.Loader)
			}
		}
		for _, k := range got {
			if !slices.Contains(want, k) {
				t.Errorf("%s holds %s, which %s does not read as secret or private", name, k, s.Loader)
			}
		}
	}
	for loader := range loaders {
		if !seen[loader] {
			t.Errorf("no Secret carries the %s loader", loader)
		}
	}
}

// covers: AC-7 (spec 0015). The template secrets.env holds exactly the
// table's inputs and the Install settings, with no value filled in.
func TestTemplateSecretsEnvMatches(t *testing.T) {
	env, err := ReadEnvFile(filepath.Join(repoRoot, ClustersDir, "_template", secretsFile))
	if err != nil {
		t.Fatal(err)
	}
	want := append(realTable(t).Inputs(), config.Fields(&config.Install{})...)
	slices.Sort(want)
	want = slices.Compact(want)
	got := slices.Sorted(slices.Values(env.Names()))
	if !slices.Equal(got, want) {
		t.Errorf("template secrets.env names:\n got  %v\n want %v", got, want)
	}
	known := config.Names()
	for _, name := range got {
		if !slices.Contains(known, name) {
			t.Errorf("template secrets.env sets %s, unknown to internal/config", name)
		}
	}
	tbl := realTable(t)
	for name := range tbl.Generated {
		if env.Get(name) != "" {
			t.Errorf("template secrets.env fills generated %s; leave it blank", name)
		}
	}
}

func TestEveryInputIsAKnownSetting(t *testing.T) {
	known := config.Names()
	for _, name := range realTable(t).Inputs() {
		if !slices.Contains(known, name) {
			t.Errorf("the secret table reads %s, unknown to internal/config", name)
		}
	}
}

func TestRequiredLeavesOutGeneratedAndOptional(t *testing.T) {
	tbl := realTable(t)
	got := tbl.Required([]string{componentAgent}, false)
	for _, want := range []string{"LDAP_URL", "LDAP_BIND_PASSWORD", "ZAMMAD_URL", "ZAMMAD_TOKEN", "LDAP_BASE_DN"} {
		if !slices.Contains(got, want) {
			t.Errorf("Required(demo off) misses %s: %v", want, got)
		}
	}
	for _, not := range []string{"TOOLSERVER_TOKEN", "PG_OWNER_PASSWORD", "SLACK_BOT_TOKEN"} {
		if slices.Contains(got, not) {
			t.Errorf("Required(demo off) holds %s (generated or optional)", not)
		}
	}
	demo := tbl.Required([]string{componentAgent}, true)
	for _, not := range []string{"LDAP_URL", "ZAMMAD_URL", "ZAMMAD_TOKEN", "LDAP_BIND_PASSWORD"} {
		if slices.Contains(demo, not) {
			t.Errorf("Required(demo on) holds %s (chart value or generated in demo)", not)
		}
	}
}

func TestTableCheckRefusesBadDerive(t *testing.T) {
	tbl := Table{
		Database: Database{Host: "h", Port: 5432, Name: "n", CAPath: "/c"},
		Secrets: map[string]Secret{"s": {Namespace: "ns", Component: "agent", Keys: map[string]Key{
			"URL": {Derive: "something_else"},
		}}},
	}
	if err := tbl.check(); err == nil {
		t.Error("check accepted an unknown derive")
	}
	tbl.Secrets["s"].Keys["URL"] = Key{Derive: deriveDatabaseURL, Role: "r", Password: "P"}
	tbl.Generated = map[string]Generated{"P": {Length: 8}}
	if err := tbl.check(); err == nil {
		t.Error("check accepted a generated length under the minimum")
	}
}

func TestHomeFindsTheLiveCopy(t *testing.T) {
	tbl := realTable(t)
	cases := map[string][2]string{
		"TOOLSERVER_TOKEN":  {"noord-agent", "TOOLSERVER_TOKEN"},
		"PG_AGENT_PASSWORD": {"noord-pg-agent", "password"},
	}
	for name, want := range cases {
		secret, key, ok := tbl.home(name)
		if !ok || secret != want[0] || key != want[1] {
			t.Errorf("home(%s) = %s %s %t, want %v", name, secret, key, ok, want)
		}
	}
}
