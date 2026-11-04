package eval_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	On          map[string]any `yaml:"on"`
	Concurrency struct {
		Group string `yaml:"group"`
	} `yaml:"concurrency"`
	Jobs map[string]struct {
		RunsOn      string `yaml:"runs-on"`
		Environment string `yaml:"environment"`
		Timeout     int    `yaml:"timeout-minutes"`
	} `yaml:"jobs"`
}

// wantPaths is the AC-14 path filter, plus the one exclusion: a committed run
// report under eval/runs must not start another run.
var wantPaths = []string{
	"internal/agent/prompt/**", "internal/tools/**", "internal/mcpserver/**", "cmd/toolserver/**",
	"eval/**", "!eval/runs/**", "cmd/eval/**", "internal/seed/**", "seed/**",
}

// covers: AC-14, AC-16 (spec 0018). The workflow never runs on a pull request,
// uses the eval Environment, a concurrency group, a 90 minute timeout, the
// cluster runner, and exactly the agreed path filter.
func TestEvalWorkflow(t *testing.T) {
	raw, err := os.ReadFile("../.github/workflows/eval.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	for trigger := range w.On {
		if strings.HasPrefix(trigger, "pull_request") {
			t.Errorf("the eval workflow runs on %s: the API key must stay out of pull request builds", trigger)
		}
	}
	if _, ok := w.On["push"]; !ok {
		t.Error("no push trigger")
	}
	if _, ok := w.On["workflow_dispatch"]; !ok {
		t.Error("no workflow_dispatch trigger")
	}
	push, _ := w.On["push"].(map[string]any)
	var paths []string
	for _, p := range push["paths"].([]any) {
		paths = append(paths, p.(string))
	}
	if !slices.Equal(paths, wantPaths) {
		t.Errorf("paths = %v, want %v", paths, wantPaths)
	}
	if w.Concurrency.Group == "" {
		t.Error("no concurrency group: two eval runs could overlap")
	}
	job, ok := w.Jobs["eval"]
	if !ok {
		t.Fatal("no eval job")
	}
	if job.Environment != "eval" || job.Timeout != 90 || !strings.Contains(job.RunsOn, "'k3sprox'") {
		t.Errorf("job = %+v", job)
	}
	if _, ok := w.Jobs["ci-ok"]; ok {
		t.Error("the eval workflow must not define ci-ok")
	}
}
