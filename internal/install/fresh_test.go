package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// covers: AC-3 (spec 0015). ArgoCD is installed at the pinned version when
// argocd-server is missing, and left alone when it is there.
func TestArgoCDInstall(t *testing.T) {
	f := newFixture(t)
	must(t, os.MkdirAll(filepath.Join(f.root, "deploy/argocd"), 0o750))
	must(t, os.WriteFile(filepath.Join(f.root, argocdPinFile),
		[]byte("argocd: {repoURL: https://charts.example, chart: argo-cd, version: 9.9.9}\n"), 0o600))
	s := &state{Options: f.options().Defaults()}
	must(t, s.preflightLight())
	f.run.answers["helm --kube-context k3sprox upgrade"] = say("")
	must(t, s.argocd(t.Context()))
	if f.count("helm") != 0 {
		t.Fatal("ArgoCD present: helm must not run")
	}
	f.run.answers["kubectl --context k3sprox get deployment argocd-server"] = say("")
	must(t, s.argocd(t.Context()))
	var got string
	for _, c := range f.run.calls {
		if c.Name == "helm" {
			got = strings.Join(c.Args, " ")
		}
	}
	for _, want := range []string{"--version 9.9.9", "--repo https://charts.example", "argo-cd", "--namespace argocd"} {
		if !strings.Contains(got, want) {
			t.Errorf("helm %q lacks %q", got, want)
		}
	}
}

func TestReadArgoPinRefusesAHole(t *testing.T) {
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "deploy/argocd"), 0o750))
	must(t, os.WriteFile(filepath.Join(dir, argocdPinFile), []byte("argocd: {chart: argo-cd}\n"), 0o600))
	if _, err := ReadArgoPin(dir); err == nil {
		t.Error("a pin without version or repo was accepted")
	}
}

func TestParseQuantity(t *testing.T) {
	for in, want := range map[string]int64{"6Gi": 6 << 30, "512Mi": 512 << 20, "1000": 1000, "2G": 2e9, "100Ki": 100 << 10} {
		if got, err := ParseQuantity(in); err != nil || got != want {
			t.Errorf("ParseQuantity(%q) = %d, %v, want %d", in, got, err, want)
		}
	}
	if _, err := ParseQuantity("lots"); err == nil {
		t.Error("ParseQuantity accepted lots")
	}
}

// covers: AC-2 (spec 0015). The demo needs a node with demo.minFreeMemory
// left after current requests; an installed cluster is not asked again.
func TestMemoryCheck(t *testing.T) {
	kc := "kubectl --context k3sprox "
	f := newFixture(t)
	s := &state{Options: f.options().Defaults()}
	must(t, s.preflightLight())
	s.demo = true
	f.run.answers[kc+"get applications.argoproj.io"] = say("")
	if err := s.checkMemory(t.Context()); err != nil {
		t.Fatalf("12Gi free of 6Gi needed: %v", err)
	}
	s.values.Demo.MinFreeMemory = "20Gi"
	if err := s.checkMemory(t.Context()); err == nil || !strings.Contains(err.Error(), "20Gi") {
		t.Errorf("err = %v, want one naming 20Gi", err)
	}
	f.run.answers[kc+"get applications.argoproj.io"] = say("application/noord-k3sprox\n")
	if err := s.checkMemory(t.Context()); err != nil {
		t.Errorf("an installed cluster was asked for free memory: %v", err)
	}
}

// covers: AC-4 (spec 0015). Every enabled operator is waited for by name;
// Tailscale's operator and the platform Application wait for the last wait.
func TestPlatformWaitSetsTailscaleAside(t *testing.T) {
	f := newFixture(t)
	s := &state{Options: f.options().Defaults()}
	must(t, s.preflightLight())
	s.values.Platform.SealedSecrets.Enabled = true
	s.values.Platform.CertManager.Enabled = true
	s.values.Platform.CNPG.Enabled = true
	s.values.Platform.Tailscale.Enabled = true
	got := strings.Join(s.platformWait(), ",")
	if strings.Contains(got, "tailscale") || strings.Contains(got, "-platform") || !strings.Contains(got, "cnpg") || !strings.Contains(got, "cert-manager") || !strings.Contains(got, "sealed-secrets") {
		t.Errorf("platform wait = %s", got)
	}
	if all := strings.Join(s.platformApps(), ","); !strings.Contains(all, "tailscale") || !strings.Contains(all, "-platform") {
		t.Errorf("final wait = %s, want tailscale and the platform Application", all)
	}
}

// covers: AC-5 (spec 0015). A push refused by a branch rule goes to a new
// branch with a pull request and exit 3; the local branch is put back; an
// ordinary refusal is a plain failure.
func TestPullRequestPath(t *testing.T) {
	f := dayTwo(t)
	f.run.answers["git push"] = func(c Cmd) ([]byte, error) {
		if strings.Contains(strings.Join(c.Args, " "), "deploy/k3sprox-") {
			return nil, nil
		}
		return []byte("!\tHEAD:refs/heads/main\t[remote rejected] (protected branch hook declined)\n"), &CmdError{Name: "git", Sub: "push", Err: errors.New("exit 1")}
	}
	f.run.answers["git reset"] = say("")
	f.run.answers["gh pr create"] = say("https://example.invalid/pull/7\n")
	opts := f.options()
	opts.Now = func() time.Time { return time.Date(2026, 10, 23, 1, 2, 3, 0, time.UTC) }
	s := &state{Options: opts.Defaults()}
	must(t, s.preflightLight())
	s.track = Tracking{Remote: "origin", Branch: "main"}
	err := s.commitAndPush(t.Context())
	var pr *prError
	if !errors.As(err, &pr) || pr.url != "https://example.invalid/pull/7" {
		t.Fatalf("err = %v, want the pull request", err)
	}
	if f.count("git reset --keep origin/main") != 1 || f.count("git push --quiet origin HEAD:refs/heads/deploy/k3sprox-20261023T010203Z") != 1 {
		t.Errorf("calls = %v", f.run.calls)
	}

	f.run.answers["git push"] = func(Cmd) ([]byte, error) {
		return []byte("!\tHEAD\t[rejected] (fetch first)\n"), &CmdError{Name: "git", Sub: "push", Err: errors.New("exit 1")}
	}
	err = s.commitAndPush(t.Context())
	if err == nil || errors.As(err, &pr) {
		t.Errorf("err = %v, want a plain failure for a non protected rejection", err)
	}
}

// covers: AC-5 (spec 0015). After a pull request merges and before ArgoCD
// applied anything, a rerun keeps the old ciphertext instead of sealing again.
func TestResealKeepsRecordedValueWithoutLiveSecret(t *testing.T) {
	tbl := realTable(t)
	name := tbl.Enabled([]string{componentAgent})[0]
	ns := tbl.Secrets[name].Namespace
	plain := Plain{name: {"K": "v1"}}
	old := Sealed{name: {"K": "cipher"}}
	seal := &fakeSealer{}
	rec := tbl.Remember(plain)
	next, plan, err := tbl.Reseal(t.Context(), plain, old, fakeLive{}, seal, rec)
	must(t, err)
	if next[name]["K"] != "cipher" || plan.Kept != 1 || seal.n != 0 {
		t.Errorf("plan = %+v, sealed %d times, want the old ciphertext kept", plan, seal.n)
	}
	_, plan, err = tbl.Reseal(t.Context(), Plain{name: {"K": "v2"}}, old, fakeLive{}, seal, rec)
	must(t, err)
	if plan.Resealed != 1 {
		t.Errorf("a changed value was kept (%s): %+v", ns, plan)
	}
	path := filepath.Join(t.TempDir(), "r.json")
	must(t, WriteRecord(path, rec))
	info, err := os.Stat(path)
	must(t, err)
	if info.Mode().Perm() != 0o600 || !ReadRecord(path).Matches(ns, name, "K", "v1") {
		t.Error("the record is not kept at mode 0600 or does not read back")
	}
}
