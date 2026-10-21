package install

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const testRev = "0123456789abcdef0123456789abcdef01234567"

// fixture is a clone with one cluster folder and a fake cluster behind it.
type fixture struct {
	root             string
	run              *fakeRunner
	live             fakeLive
	reg              *httptest.Server
	missing          string // an image name the registry lacks
	dirty            string // git status output
	context          string
	remote, upstream string
	out              bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{root: t.TempDir(), live: fakeLive{}, context: "k3sprox", remote: "git@example.invalid:r.git", upstream: "origin/main"}
	dir := filepath.Join(f.root, ClustersDir, "k3sprox")
	must(t, os.MkdirAll(dir, 0o750))
	must(t, os.MkdirAll(filepath.Join(f.root, filepath.Dir(TablePath)), 0o750))
	table, err := os.ReadFile(filepath.Join(repoRoot, TablePath))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(f.root, TablePath), table, 0o600)) //nolint:gosec // a fixed path in t.TempDir
	values := strings.Join([]string{
		"cluster: {name: k3sprox}",
		"repo: {url: https://example.invalid/r.git, revision: main}",
		"image: {registry: REG, owner: o, tag: " + testRev + "}",
		"demo: {enabled: false}",
		"platform: {sealedSecrets: {namespace: kube-system, name: sealed-secrets-controller}}",
		"ca:",
		"  certificate: |",
		"    -----BEGIN CERTIFICATE-----",
		"",
	}, "\n")
	f.reg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.missing != "" && strings.Contains(r.URL.Path, "aiitsupport-"+f.missing+"/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.reg.Close)
	values = strings.Replace(values, "REG", strings.TrimPrefix(f.reg.URL, "http://"), 1)
	must(t, os.WriteFile(filepath.Join(dir, valuesFile), []byte(values), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, bootstrapFile), []byte("# apps\n"), 0o600))
	env := tracerEnv(t)
	must(t, env.Write(filepath.Join(dir, secretsFile)))
	f.run = &fakeRunner{answers: f.answers()}
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) answers() map[string]func(Cmd) ([]byte, error) {
	kc := "kubectl --context k3sprox "
	return map[string]func(Cmd) ([]byte, error){
		"kubectl config current-context":    func(Cmd) ([]byte, error) { return []byte(f.context + "\n"), nil },
		kc + "get storageclass":             say(`{"items":[{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}]}`),
		kc + "get deployment argocd-server": say("deployment.apps/argocd-server\n"),
		kc + "get secret": func(c Cmd) ([]byte, error) {
			ns := c.Args[slices.Index(c.Args, "-n")+1]
			data, ok := f.live[ns+"/"+c.Args[4]]
			if !ok {
				return nil, nil
			}
			enc := map[string]string{}
			for k, v := range data {
				enc[k] = base64.StdEncoding.EncodeToString([]byte(v))
			}
			return json.Marshal(map[string]any{"data": enc})
		},
		kc + "apply":              say(""),
		kc + "-n argocd annotate": say(""),
		kc + "-n argocd get applications.argoproj.io": f.apps,
		kc + "get sealedsecrets.bitnami.com":          f.sealedSecrets,
		"kubeseal --context k3sprox":                  say("-----BEGIN CERTIFICATE-----\n"),
		"kubeseal --raw":                              func(c Cmd) ([]byte, error) { return []byte("Ag" + base64.StdEncoding.EncodeToString(c.Stdin)[:4]), nil },
		"git status":                                  func(Cmd) ([]byte, error) { return []byte(f.dirty), nil },
		"git rev-parse --abbrev-ref":                  func(Cmd) ([]byte, error) { return []byte(f.upstream + "\n"), nil },
		"git remote get-url":                          func(Cmd) ([]byte, error) { return []byte(f.remote + "\n"), nil },
		"git fetch":                                   say(""),
		"git rev-list":                                say("0\t0\n"),
		"git rev-parse HEAD":                          say(testRev + "\n"),
		"git add":                                     say(""),
		"git diff":                                    f.staged,
		"git commit":                                  say(""),
		"git push":                                    say(""),
	}
}

// staged reports the folder as changed when sealed-values.yaml differs
// from what the last "commit" saw.
func (f *fixture) staged(Cmd) ([]byte, error) {
	raw, _ := os.ReadFile(filepath.Join(f.root, ClustersDir, "k3sprox", sealedFile))
	committed, _ := os.ReadFile(filepath.Join(f.root, "committed"))
	if bytes.Equal(raw, committed) {
		return nil, nil
	}
	_ = os.WriteFile(filepath.Join(f.root, "committed"), raw, 0o600) //nolint:gosec // a fixed path in t.TempDir
	return []byte("deploy/clusters/k3sprox/sealed-values.yaml\n"), nil
}

func (f *fixture) apps(c Cmd) ([]byte, error) {
	var items []map[string]any
	for _, name := range c.Args[slices.Index(c.Args, "--ignore-not-found")+1:] {
		items = append(items, map[string]any{
			"metadata": map[string]any{"name": name},
			"status":   map[string]any{"sync": map[string]any{"status": "Synced", "revision": testRev}, "health": map[string]any{"status": "Healthy"}},
		})
	}
	return json.Marshal(map[string]any{"kind": "List", "items": items})
}

func (f *fixture) sealedSecrets(Cmd) ([]byte, error) {
	var items []map[string]any
	for name, s := range realTableAt(f.root).Secrets {
		items = append(items, map[string]any{
			"metadata": map[string]any{"name": name, "namespace": s.Namespace},
			"status":   map[string]any{"conditions": []map[string]any{{"type": "Synced", "status": "True"}}},
		})
	}
	return json.Marshal(map[string]any{"kind": "List", "items": items})
}

func realTableAt(root string) Table {
	t, _ := LoadTable(filepath.Join(root, TablePath))
	return t
}

func (f *fixture) options() Options {
	return Options{
		Root: f.root, Cluster: "k3sprox", Out: &f.out, Run: f.run, Home: f.root, TempDir: f.root,
		Registry: Registry{Client: f.reg.Client(), Scheme: "http"}, Poll: time.Millisecond,
		LookPath: func(string) (string, error) { return "/bin/x", nil },
	}
}

func (f *fixture) count(prefix string) int {
	n := 0
	for _, c := range f.run.calls {
		if strings.HasPrefix(strings.Join(append([]string{c.Name}, c.Args...), " "), prefix) {
			n++
		}
	}
	return n
}

// covers: AC-5, AC-6, AC-8 (spec 0015). A first run generates, seals,
// commits, pushes, and applies; a rerun with the cluster holding those
// values seals nothing and commits nothing.
func TestRunThenRerun(t *testing.T) {
	f := newFixture(t)
	if err := Run(t.Context(), f.options()); err != nil {
		t.Fatalf("first run: %v\n%s", err, f.out.String())
	}
	env, err := ReadEnvFile(filepath.Join(f.root, ClustersDir, "k3sprox", secretsFile))
	must(t, err)
	token := env.Get("TOOLSERVER_TOKEN")
	if len(token) != 48 {
		t.Fatalf("TOOLSERVER_TOKEN not generated into secrets.env")
	}
	if strings.Contains(f.out.String(), token) || strings.Contains(f.out.String(), env.Get("LDAP_BIND_PASSWORD")) {
		t.Error("the output printed a secret value")
	}
	sealed, err := Folder{Dir: filepath.Join(ClustersDir, "k3sprox"), root: f.root}.ReadSealed()
	must(t, err)
	for _, name := range []string{"noord-agent", "noord-toolserver", "noord-migrate", "noord-pg-owner"} {
		if len(sealed[name]) == 0 {
			t.Errorf("sealed-values.yaml has no %s block", name)
		}
	}
	if f.count("git commit") != 1 || f.count("git push") != 1 {
		t.Errorf("commits %d, pushes %d, want 1 and 1", f.count("git commit"), f.count("git push"))
	}
	if f.count("kubectl --context k3sprox apply") != 2 {
		t.Errorf("applies = %d, want the platform part and the apps part", f.count("kubectl --context k3sprox apply"))
	}

	// The cluster now holds what was sealed.
	plain, err := realTable(t).Build(env, []string{componentAgent}, false)
	must(t, err)
	for name, data := range plain {
		f.live[realTable(t).Secrets[name].Namespace+"/"+name] = data
	}
	f.run.calls = nil
	if err := Run(t.Context(), f.options()); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if n := f.count("kubeseal --raw"); n != 0 {
		t.Errorf("rerun sealed %d values, want 0", n)
	}
	if f.count("git commit") != 0 || f.count("git push") != 0 {
		t.Error("rerun committed or pushed")
	}
}

// covers: AC-2 (spec 0015). Each problem stops the run with exit 2 before
// anything is applied, named without its value.
func TestPreflightStops(t *testing.T) {
	cases := map[string]struct {
		change func(*fixture)
		want   string
	}{
		"wrong context": {func(f *fixture) { f.context = "other" }, "KUBE_CONTEXT"},
		"dirty tree":    {func(f *fixture) { f.dirty = " M Makefile\n" }, "changes the installer would not commit"},
		"stray file":    {func(f *fixture) { f.dirty = "?? deploy/clusters/k3sprox/secrets.env.bak\n" }, "first deploy/clusters/k3sprox/secrets.env.bak"},
		"wrong branch":  {func(f *fixture) { f.upstream = "origin/feat/x" }, "ArgoCD follows repo.revision main"},
		"wrong remote":  {func(f *fixture) { f.remote = "https://github.com/fork/r.git" }, "not repo.url"},
		"missing image": {func(f *fixture) { f.missing = "samba" }, "no public samba image"},
		"change me": {func(f *fixture) {
			setEnv(f, "LDAP_BIND_PASSWORD", placeholder)
		}, "LDAP_BIND_PASSWORD in secrets.env still holds CHANGE_ME"},
		"missing setting": {func(f *fixture) { setEnv(f, "ZAMMAD_URL", "") }, "ZAMMAD_URL is not set"},
		"no folder":       {func(f *fixture) { _ = os.RemoveAll(filepath.Join(f.root, ClustersDir, "k3sprox")) }, "make cluster-init"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			tc.change(f)
			err := Run(t.Context(), f.options())
			exit, ok := errors.AsType[*ExitError](err)
			if !ok || exit.Code != ExitPreflight || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want exit 2 naming %q", err, tc.want)
			}
			if f.count("kubectl --context k3sprox apply") != 0 || f.count("git commit") != 0 {
				t.Error("preflight changed something")
			}
			if strings.Contains(err.Error(), strings.Repeat("z", 40)) {
				t.Error("an error printed a secret value")
			}
		})
	}
}

func setEnv(f *fixture, name, value string) {
	path := filepath.Join(f.root, ClustersDir, "k3sprox", secretsFile)
	env, _ := ReadEnvFile(path)
	env.Set(name, value)
	_ = env.Write(path)
}

// covers: AC-1 (spec 0015). An Application that never turns Healthy ends
// the wait at INSTALL_TIMEOUT with exit 1, naming it.
func TestWaitTimesOutNamingTheApp(t *testing.T) {
	f := newFixture(t)
	setEnv(f, "INSTALL_TIMEOUT", "5m")
	healthy := f.run.answers["kubectl --context k3sprox -n argocd get applications.argoproj.io"]
	f.run.answers["kubectl --context k3sprox -n argocd get applications.argoproj.io"] = func(c Cmd) ([]byte, error) {
		out, err := healthy(c)
		return bytes.ReplaceAll(out, []byte(`"noord-agent"},"status":{"health":{"status":"Healthy"}`), []byte(`"noord-agent"},"status":{"health":{"status":"Degraded"}`)), err
	}
	s := &state{Options: f.options().Defaults()}
	must(t, s.preflight(t.Context()))
	// Run sets one deadline for every step; here the wait gets it directly.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := s.wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "noord-agent Synced and Degraded") {
		t.Fatalf("err = %v, want the degraded app named", err)
	}
}

// A failed read during the wait (an API blip) is retried, not fatal.
func TestWaitRidesOutAFailedRead(t *testing.T) {
	f := newFixture(t)
	key := "kubectl --context k3sprox -n argocd get applications.argoproj.io"
	healthy, fails := f.run.answers[key], 2
	f.run.answers[key] = func(c Cmd) ([]byte, error) {
		if fails > 0 {
			fails--
			return nil, errors.New("connection reset")
		}
		return healthy(c)
	}
	s := &state{Options: f.options().Defaults()}
	must(t, s.preflight(t.Context()))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.wait(ctx); err != nil || fails != 0 {
		t.Fatalf("wait = %v after %d failures left, want success", err, fails)
	}
}
