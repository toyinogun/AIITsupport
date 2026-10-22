package install

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/config"
)

var smokeTime = time.Date(2026, 10, 22, 14, 5, 9, 0, time.UTC)

func (f *fixture) smokeOptions() Options {
	o := f.options()
	o.Now = func() time.Time { return smokeTime }
	return o
}

// covers: AC-1, AC-17 (spec 0015). After the wait, the install runs one Job
// from the suspended smoke CronJob and prints only its pass and fail lines.
func TestRunEndsWithTheSmokeCheck(t *testing.T) {
	f := newFixture(t)
	if err := Run(t.Context(), f.smokeOptions()); err != nil {
		t.Fatalf("Run: %v\n%s", err, f.out.String())
	}
	var created []string
	for _, c := range f.run.calls {
		if slices.Contains(c.Args, "create") {
			created = c.Args
		}
	}
	if !slices.Contains(created, "noord-smoke-20261022-140509") || !slices.Contains(created, "--from=cronjob/noord-smoke") {
		t.Errorf("create job args = %v, want a Job named by the time, from the CronJob", created)
	}
	out := f.out.String()
	for _, want := range []string{"install k3sprox: smoke\n", "install k3sprox:   pass migrations\n", "the smoke check passed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// covers: AC-17 (spec 0015). A failed smoke Job fails the install at the
// smoke step, with its fail line shown and nothing else from its log.
func TestFailedSmokeFailsTheRun(t *testing.T) {
	f := newFixture(t)
	kc := "kubectl --context k3sprox -n noord-agent "
	f.run.answers[kc+"get job"] = say(`{"status":{"failed":1}}`)
	f.run.answers[kc+"logs"] = say("pass agent_readyz\nfail toolserver_readyz http_503 ldap=network\nseed: 1 of 4 smoke checks failed\n")
	err := Run(t.Context(), f.smokeOptions())
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Step != "smoke" || exit.Code != ExitFailed {
		t.Fatalf("Run = %v, want exit 1 at the smoke step", err)
	}
	out := f.out.String()
	if !strings.Contains(out, "  fail toolserver_readyz http_503 ldap=network\n") || strings.Contains(out, "smoke checks failed") {
		t.Errorf("output = %s, want the fail line and none of the rest of the log", out)
	}
}

// covers: AC-17 (spec 0015). make smoke runs only the smoke Job, and only
// on the cluster KUBE_CONTEXT names.
func TestSmokeAlone(t *testing.T) {
	f := newFixture(t)
	if err := Smoke(t.Context(), f.smokeOptions()); err != nil {
		t.Fatalf("Smoke: %v\n%s", err, f.out.String())
	}
	if f.count("kubectl --context k3sprox -n noord-agent create job") != 1 || f.count("kubectl --context k3sprox apply") != 0 || f.count("git") != 0 {
		t.Errorf("calls = %v, want one Job and nothing else changed", f.run.calls)
	}

	f = newFixture(t)
	f.context = "another-cluster"
	err := Smoke(t.Context(), f.smokeOptions())
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != ExitPreflight || f.count("kubectl --context k3sprox -n noord-agent create job") != 0 {
		t.Errorf("Smoke on the wrong context = %v, want exit 2 and no Job", err)
	}
}

// covers: AC-1 (spec 0015). The report names the tailnet URLs of what this
// cluster runs.
func TestReportNamesTheURLs(t *testing.T) {
	var out bytes.Buffer
	s := &state{Options: Options{Cluster: "k3sprox", Out: &out}, cfg: config.Install{Tailnet: "example.ts.net"}, demo: true, conds: Conditions{Demo: true, Reset: true}}
	s.values.Cluster.Name = "k3sprox"
	s.report()
	for _, want := range []string{
		"the helpdesk: https://noord-k3sprox-helpdesk.example.ts.net",
		"the reset page: https://noord-k3sprox-reset.example.ts.net",
		"noord-kill-switch",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
}
