package telemetry_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/logging"
)

// collectorFile is the part of the Collector config the drift test reads.
type collectorFile struct {
	Processors struct {
		Redaction struct {
			AllowAllKeys  bool     `yaml:"allow_all_keys"`
			AllowedKeys   []string `yaml:"allowed_keys"`
			BlockedValues []string `yaml:"blocked_values"`
			Summary       string   `yaml:"summary"`
		} `yaml:"redaction"`
	} `yaml:"processors"`
	Service struct {
		Pipelines map[string]struct {
			Processors []string `yaml:"processors"`
		} `yaml:"pipelines"`
	} `yaml:"service"`
}

func loadCollector(t *testing.T) collectorFile {
	t.Helper()
	path := filepath.Join("..", "..", "deploy", "helm", "noord", "files", "observability", "collector.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path inside the repo
	if err != nil {
		t.Fatal(err)
	}
	var c collectorFile
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return c
}

// covers: spec 0016 AC-4. The Collector's allowed_keys are exactly the log
// allow list plus service.name, so a key added in code and not in the
// Collector config fails the build, and the reverse.
func TestCollectorAllowedKeysMatchLogAllowList(t *testing.T) {
	r := loadCollector(t).Processors.Redaction
	want := append(logging.AllowedKeys(), "service.name")
	slices.Sort(want)
	got := slices.Clone(r.AllowedKeys)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("collector allowed_keys differ from the log allow list plus service.name\n got: %v\nwant: %v", got, want)
	}
}

// covers: spec 0016 AC-4. The processor drops unlisted keys, blocks an @ and
// a Slack user id in values, stays silent (any summary adds redaction.* keys
// that are not on the allow list), and runs in the traces pipeline.
func TestCollectorRedactionSettings(t *testing.T) {
	c := loadCollector(t)
	r := c.Processors.Redaction
	if r.AllowAllKeys {
		t.Error("allow_all_keys must be false")
	}
	if r.Summary != "silent" {
		t.Errorf("summary = %q, want silent", r.Summary)
	}
	if len(r.BlockedValues) != 2 {
		t.Fatalf("blocked_values = %v, want an @ pattern and a Slack id pattern", r.BlockedValues)
	}
	for _, v := range []string{"jan.devries@noord.example", "U0JANSLACK1", "W01ABCDEF2G"} {
		if !slices.ContainsFunc(r.BlockedValues, func(p string) bool { return regexp.MustCompile(p).MatchString(v) }) {
			t.Errorf("no blocked_values pattern matches %q", v)
		}
	}
	// A pattern that matched only the @ would leave the name and domain behind.
	if m := regexp.MustCompile(r.BlockedValues[0]).FindString("jan.devries@noord.example"); m != "jan.devries@noord.example" {
		t.Errorf("the @ pattern matches %q, want the whole address", m)
	}
	if !slices.Contains(c.Service.Pipelines["traces"].Processors, "redaction") {
		t.Errorf("traces pipeline processors = %v, want redaction in it", c.Service.Pipelines["traces"].Processors)
	}
}
