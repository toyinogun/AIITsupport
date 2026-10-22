package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dayTwo is a fixture whose git commit always stages values.yaml, the file
// every day two command changes.
func dayTwo(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.run.answers["git diff"] = say("deploy/clusters/k3sprox/values.yaml\n")
	f.run.answers["git rev-parse --verify"] = say(strings.Repeat("ab", 20) + "\n")
	return f
}

func (f *fixture) values(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, ClustersDir, "k3sprox", valuesFile))
	must(t, err)
	return string(raw)
}

func (f *fixture) commits() []string {
	var msgs []string
	for _, c := range f.run.calls {
		if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "commit" {
			msgs = append(msgs, c.Args[3])
		}
	}
	return msgs
}

// covers: AC-19 (spec 0015). release writes image.tag, commits with the
// short SHA and pushes, only when all four images exist.
func TestRelease(t *testing.T) {
	f := dayTwo(t)
	sha := strings.Repeat("ab", 20)
	must(t, Release(t.Context(), f.options(), sha))
	if !strings.Contains(f.values(t), "tag: "+sha) {
		t.Errorf("values.yaml = %q, want image.tag %s", f.values(t), sha)
	}
	if got := f.commits(); len(got) != 1 || got[0] != "chore(release): k3sprox to abababa" {
		t.Errorf("commits = %q, want one release commit", got)
	}
	if f.count("git push") != 1 {
		t.Error("release did not push once")
	}
}

func TestReleaseMissingImageWritesNothing(t *testing.T) {
	f := dayTwo(t)
	f.missing = "samba"
	before := f.values(t)
	err := Release(t.Context(), f.options(), strings.Repeat("ab", 20))
	if err == nil || !strings.Contains(err.Error(), "samba") {
		t.Fatalf("err = %v, want one naming samba", err)
	}
	if f.values(t) != before || len(f.commits()) != 0 || f.count("git push") != 0 {
		t.Error("a failed release wrote, committed or pushed")
	}
}

func TestReleaseUnknownSHA(t *testing.T) {
	f := dayTwo(t)
	delete(f.run.answers, "git rev-parse --verify")
	before := f.values(t)
	if err := Release(t.Context(), f.options(), "nope"); err == nil {
		t.Fatal("want an error for an unknown SHA")
	}
	if f.values(t) != before {
		t.Error("values.yaml changed")
	}
}

func TestReleaseSameTagIsANoOp(t *testing.T) {
	f := dayTwo(t)
	f.run.answers["git rev-parse --verify"] = say(testRev + "\n")
	must(t, Release(t.Context(), f.options(), testRev))
	if len(f.commits()) != 0 || f.count("git push") != 0 {
		t.Error("releasing the current tag committed or pushed")
	}
}

// covers: AC-20 (spec 0015). pause sets paused, commits, pushes and waits
// until the agent pod is gone.
func TestPauseAndResume(t *testing.T) {
	f := dayTwo(t)
	pods := 2
	f.run.answers["kubectl --context k3sprox -n noord-agent get pods"] = func(Cmd) ([]byte, error) {
		pods--
		if pods > 0 {
			return []byte("pod/agent-x\n"), nil
		}
		return nil, nil
	}
	must(t, Pause(t.Context(), f.options()))
	if !strings.Contains(f.values(t), "paused: true") {
		t.Errorf("values.yaml = %q, want paused: true", f.values(t))
	}
	if pods > 0 {
		t.Error("pause did not wait for the agent pod to go")
	}
	must(t, Resume(t.Context(), f.options()))
	v := f.values(t)
	if !strings.Contains(v, "paused: false") || !strings.Contains(v, "seedRun: 1") {
		t.Errorf("values.yaml = %q, want paused: false and seedRun: 1", v)
	}
	must(t, Pause(t.Context(), f.options()))
	must(t, Resume(t.Context(), f.options()))
	if !strings.Contains(f.values(t), "seedRun: 2") {
		t.Errorf("a second resume left %q, want seedRun: 2", f.values(t))
	}
}

// covers: AC-21 (spec 0015). uninstall wants the cluster name typed, and
// refuses an Application another Application owns.
func TestUninstall(t *testing.T) {
	kc := "kubectl --context k3sprox "
	f := dayTwo(t)
	gone := false
	f.run.answers[kc+"-n argocd get applications.argoproj.io noord-k3sprox"] = say(`{"metadata":{"name":"noord-k3sprox"}}`)
	f.run.answers[kc+"-n argocd delete"] = func(Cmd) ([]byte, error) { gone = true; return nil, nil }
	delete(f.run.answers, kc+"get namespace noord-directory")
	f.run.answers[kc+"get namespace"] = func(Cmd) ([]byte, error) {
		if gone {
			return nil, nil
		}
		return []byte("namespace/x\n"), nil
	}
	f.run.answers[kc+"delete pvc"] = say("")
	opts := f.options()
	opts.In = strings.NewReader("wrong\n")
	if err := Uninstall(t.Context(), opts); err == nil || f.count("kubectl --context k3sprox -n argocd delete") != 0 {
		t.Fatalf("err = %v: a wrong name must stop before any delete", err)
	}
	opts.In = strings.NewReader("k3sprox\n")
	must(t, Uninstall(t.Context(), opts))
	if f.count(kc+"-n argocd delete") != 2 {
		t.Errorf("deletes = %d, want the apps and the platform Application", f.count(kc+"-n argocd delete"))
	}
}

func TestUninstallRefusesOwnedApplication(t *testing.T) {
	kc := "kubectl --context k3sprox "
	f := dayTwo(t)
	f.run.answers[kc+"-n argocd get applications.argoproj.io noord-k3sprox"] = say(
		`{"metadata":{"name":"noord-k3sprox","labels":{"argocd.argoproj.io/instance":"root"}}}`)
	opts := f.options()
	opts.In = strings.NewReader("k3sprox\n")
	err := Uninstall(t.Context(), opts)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("err = %v, want one naming root", err)
	}
	if f.count(kc+"-n argocd delete") != 0 {
		t.Error("uninstall deleted an owned Application")
	}
}

// covers: AC-22 (spec 0015). seal-key-backup writes the controller's keys
// outside the repo with mode 0600; the install warns when they differ.
func TestSealKeyBackup(t *testing.T) {
	kc := "kubectl --context k3sprox "
	f := dayTwo(t)
	f.run.answers[kc+"-n kube-system get secret"] = say("items:\n- metadata:\n    name: sealed-secrets-key1\n")
	opts := f.options()
	opts.Home = t.TempDir()
	must(t, SealKeyBackup(t.Context(), opts))
	path := filepath.Join(opts.Home, ".noord", "k3sprox", "sealing-key.yaml")
	info, err := os.Stat(path)
	must(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	if got := keyNotice(t, f, opts); got != "" {
		t.Errorf("notice = %q, want none for a matching backup", got)
	}
	f.run.answers[kc+"-n kube-system get secret"] = say("items:\n- metadata:\n    name: sealed-secrets-key2\n")
	if got := keyNotice(t, f, opts); !strings.Contains(got, "differ") {
		t.Errorf("notice = %q, want a warning that the keys differ", got)
	}
	_ = os.Remove(path)
	if got := keyNotice(t, f, opts); !strings.Contains(got, "seal-key-backup") {
		t.Errorf("notice = %q, want the reminder to run seal-key-backup", got)
	}
}

func keyNotice(t *testing.T, f *fixture, opts Options) string {
	t.Helper()
	f.out.Reset()
	s := &state{Options: opts.Defaults()}
	must(t, s.preflightLight())
	s.keyNotice(t.Context())
	return f.out.String()
}

// covers: AC-20 (spec 0015). A paused cluster renders the agent at 0
// replicas and no seed Job.
func TestPausedChart(t *testing.T) {
	docs := renderPart(t, componentAgent, "--set", "paused=true")
	agent := object(docs, "Deployment", "agent")
	spec, _ := agent["spec"].(map[string]any)
	if spec["replicas"] != 0 && spec["replicas"] != float64(0) && spec["replicas"] != int64(0) {
		t.Errorf("replicas = %v (%T), want 0", spec["replicas"], spec["replicas"])
	}
	for _, c := range []string{componentDirectory, componentClient} {
		extra := []string{"--set", "paused=true"}
		if c == componentClient {
			extra = append(extra, "--set", "zammad.enabled=true")
		}
		for _, d := range renderPart(t, c, extra...) {
			meta, _ := d["metadata"].(map[string]any)
			if name, _ := meta["name"].(string); d["kind"] == "Job" && strings.HasPrefix(name, "seed-") {
				t.Errorf("paused %s part renders Job %s", c, name)
			}
		}
	}
}
