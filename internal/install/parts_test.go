package install

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	env, err := ParseEnv([]byte("# c\n\nA=1\nB=\"x y\"\nexport C='z'\nD=\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"A": "1", "B": "x y", "C": "z", "D": ""} {
		if got := env.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if _, ok := env.Lookup("D"); ok {
		t.Error("a blank value looked set")
	}
	env.Set("D", "new")
	env.Set("E", "has space")
	if got, want := string(env.Bytes()), "# c\n\nA=1\nB=\"x y\"\nexport C='z'\nD=new\nE=\"has space\"\n"; got != want {
		t.Errorf("Bytes() =\n%s\nwant\n%s", got, want)
	}
	env.Set("Q", `say "hi"`)
	again, err := ParseEnv(env.Bytes())
	if err != nil || again.Get("Q") != `say "hi"` {
		t.Errorf("a quoted value did not survive a round trip: %q, %v", again.Get("Q"), err)
	}
}

func TestParseEnvErrorsNameTheLineNotTheValue(t *testing.T) {
	for _, raw := range []string{"secretvalue\n", "A=1\nA=2\n"} {
		_, err := ParseEnv([]byte(raw))
		if err == nil || strings.Contains(err.Error(), "secretvalue") {
			t.Errorf("ParseEnv(%q) err = %v, want a line error without the value", raw, err)
		}
	}
}

func TestEnvWriteIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), secretsFile)
	must(t, os.WriteFile(path, []byte("A=0\n"), 0o644)) //nolint:gosec // the wide mode is the point
	env, _ := ParseEnv([]byte("A=1\n"))
	if err := env.Write(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
}

// fakeLive is a cluster's Secrets, by namespace/name.
type fakeLive map[string]map[string]string

func (f fakeLive) Secret(_ context.Context, ns, name string) (map[string]string, bool, error) {
	d, ok := f[ns+"/"+name]
	return d, ok, nil
}

func tracerEnv(t *testing.T) *EnvFile {
	t.Helper()
	env, err := ParseEnv([]byte(strings.Join([]string{
		"KUBE_CONTEXT=k3sprox",
		"LDAP_URL=ldaps://dc.example:636",
		"LDAP_BASE_DN=DC=x",
		"LDAP_BIND_DN=CN=svc,DC=x",
		"LDAP_BIND_PASSWORD=pw",
		"ZAMMAD_URL=https://zammad.example",
		"ZAMMAD_TOKEN=" + strings.Repeat("z", 40),
		"ZAMMAD_PUBLIC_URL=https://noord-k3sprox-helpdesk.example.ts.net",
		"SLACK_BOT_TOKEN=",
		`SEED_ADMIN_USER="CN=Administrator,CN=Users,DC=ad,DC=noord,DC=example"`,
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// covers: AC-5 (spec 0015). Missing generated values come from the live
// Secret when it exists, else are made fresh; set ones are kept.
func TestGenerate(t *testing.T) {
	tbl := realTable(t)
	env := tracerEnv(t)
	env.Set("PG_EVAL_PASSWORD", "kept")
	live := fakeLive{"noord-agent/noord-pg-agent": {"username": "noord_agent", "password": "fromlive"}}
	filled, err := tbl.Generate(t.Context(), env, live, []string{componentAgent}, Conditions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"PG_AGENT_PASSWORD", "PG_OWNER_PASSWORD", "PG_TOOLSERVER_PASSWORD", "TOOLSERVER_TOKEN"}; strings.Join(filled, ",") != strings.Join(want, ",") {
		t.Errorf("filled = %v, want %v", filled, want)
	}
	if env.Get("PG_AGENT_PASSWORD") != "fromlive" || env.Get("PG_EVAL_PASSWORD") != "kept" {
		t.Error("a live or set value was replaced")
	}
	if got := env.Get("TOOLSERVER_TOKEN"); len(got) != 48 {
		t.Errorf("TOOLSERVER_TOKEN has %d characters, want 48", len(got))
	}
	if env.Get("ZAMMAD_TOKEN") != strings.Repeat("z", 40) {
		t.Error("demo off must not generate ZAMMAD_TOKEN over the client's")
	}
}

func TestRandomValueClasses(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		v := randomValue(24)
		if len(v) != 24 || !strings.ContainsAny(v, lower) || !strings.ContainsAny(v, upper) || !strings.ContainsAny(v, digits) {
			t.Fatalf("randomValue = %q, want 24 letters and digits of every class", v)
		}
		if seen[v] {
			t.Fatal("randomValue repeated itself")
		}
		seen[v] = true
	}
}

// covers: AC-5, AC-9 (spec 0015). Build derives each role's URL, leaves out
// unset optional keys, and names a missing required one.
func TestBuild(t *testing.T) {
	tbl := realTable(t)
	env := tracerEnv(t)
	if _, err := tbl.Generate(t.Context(), env, fakeLive{}, []string{componentAgent}, Conditions{}); err != nil {
		t.Fatal(err)
	}
	plain, err := tbl.Build(env, Sources{}, []string{componentAgent}, Conditions{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(plain["noord-toolserver"]["TOOLSERVER_DATABASE_URL"])
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	if u.User.Username() != "noord_toolserver" || pw != env.Get("PG_TOOLSERVER_PASSWORD") || u.Host != "noord-pg-rw.noord-agent.svc:5432" ||
		u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") != "/etc/noord/pg-ca/ca.crt" {
		t.Errorf("TOOLSERVER_DATABASE_URL = %s, want the toolserver role on noord-pg with verify-full", u.Redacted())
	}
	if _, ok := plain["noord-agent"]["SLACK_BOT_TOKEN"]; ok {
		t.Error("an unset optional key was built")
	}
	if plain["noord-pg-owner"]["username"] != "noord_owner" {
		t.Error("the owner Secret lost its fixed username")
	}
	env.Set("LDAP_BIND_PASSWORD", "")
	if _, err := tbl.Build(env, Sources{}, []string{componentAgent}, Conditions{}); err == nil || !strings.Contains(err.Error(), "LDAP_BIND_PASSWORD") {
		t.Errorf("err = %v, want LDAP_BIND_PASSWORD named", err)
	}
}

type fakeSealer struct{ n int }

func (f *fakeSealer) Seal(_ context.Context, ns, name, _ string) (string, error) {
	f.n++
	return "sealed-" + ns + "-" + name, nil
}

// covers: AC-5, AC-8 (spec 0015). A value the live Secret already holds
// keeps its ciphertext; a changed or new one is sealed; a gone one drops.
func TestReseal(t *testing.T) {
	tbl := realTable(t)
	plain := Plain{"noord-migrate": {"MIGRATE_DATABASE_URL": "same"}, "noord-agent": {"TOOLSERVER_TOKEN": "changed"}}
	old := Sealed{
		"noord-migrate": {"MIGRATE_DATABASE_URL": "old-cipher"},
		"noord-agent":   {"TOOLSERVER_TOKEN": "old-token-cipher", "SLACK_BOT_TOKEN": "gone"},
		"noord-removed": {"X": "y"},
	}
	live := fakeLive{
		"noord-agent/noord-migrate": {"MIGRATE_DATABASE_URL": "same"},
		"noord-agent/noord-agent":   {"TOOLSERVER_TOKEN": "before"},
	}
	seal := &fakeSealer{}
	next, plan, err := tbl.Reseal(t.Context(), plain, old, live, seal)
	if err != nil {
		t.Fatal(err)
	}
	if next["noord-migrate"]["MIGRATE_DATABASE_URL"] != "old-cipher" {
		t.Error("an unchanged value was sealed again")
	}
	if next["noord-agent"]["TOOLSERVER_TOKEN"] != "sealed-noord-agent-noord-agent" {
		t.Error("a changed value kept its old ciphertext")
	}
	if plan != (SealPlan{Kept: 1, Resealed: 1, Dropped: 2}) || seal.n != 1 {
		t.Errorf("plan = %+v (seals %d), want 1 kept, 1 sealed, 2 dropped", plan, seal.n)
	}
	if _, ok := next["noord-removed"]; ok {
		t.Error("a Secret no longer rendered was kept")
	}
}

func TestSealedFileRoundTrip(t *testing.T) {
	f := Folder{Name: "c", Dir: ".", root: t.TempDir()}
	s := Sealed{"noord-agent": {"B": "2", "A": "1"}}
	if wrote, err := f.WriteSealed(s); err != nil || !wrote {
		t.Fatalf("first write = %t, %v", wrote, err)
	}
	if wrote, err := f.WriteSealed(s); err != nil || wrote {
		t.Errorf("second write = %t, %v, want no change", wrote, err)
	}
	got, err := f.ReadSealed()
	if err != nil || got["noord-agent"]["A"] != "1" {
		t.Errorf("ReadSealed = %v, %v", got, err)
	}
}

// fakeRunner answers commands by their first words.
type fakeRunner struct {
	answers map[string]func(Cmd) ([]byte, error)
	calls   []Cmd
}

func (f *fakeRunner) Run(_ context.Context, c Cmd) ([]byte, error) {
	f.calls = append(f.calls, c)
	words := append([]string{c.Name}, c.Args...)
	for n := len(words); n > 0; n-- {
		if fn, ok := f.answers[strings.Join(words[:n], " ")]; ok {
			return fn(c)
		}
	}
	return nil, errors.New("unexpected command: " + strings.Join(words, " "))
}

func say(out string) func(Cmd) ([]byte, error) {
	return func(Cmd) ([]byte, error) { return []byte(out), nil }
}

// covers: AC-5, AC-6 (spec 0015). Only values.yaml and sealed-values.yaml
// in the cluster folder may differ; a stray file there or anything outside
// is refused, look alike folders included.
func TestUnexpected(t *testing.T) {
	r := &fakeRunner{answers: map[string]func(Cmd) ([]byte, error){
		"git status": say(" M deploy/clusters/k3sprox/values.yaml\n?? deploy/clusters/k3sprox/sealed-values.yaml\n" +
			"?? deploy/clusters/k3sprox/secrets.env.bak\n M Makefile\nR  a.go -> deploy/clusters/k3sproxy/x\n"),
	}}
	changed, err := Git{run: r}.Changed(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got := Unexpected(changed, "deploy/clusters/k3sprox")
	if strings.Join(got, ",") != "Makefile,deploy/clusters/k3sprox/secrets.env.bak,deploy/clusters/k3sproxy/x" {
		t.Errorf("Unexpected = %v, want Makefile, the stray backup, and the look alike folder", got)
	}
}

func TestSameRepo(t *testing.T) {
	same := [][2]string{
		{"https://github.com/toyinogun/AIITsupport.git", "https://github.com/toyinogun/aiitsupport"},
		{"git@github.com:toyinogun/AIITsupport.git", "https://github.com/toyinogun/AIITsupport.git"},
		{"https://x:token@github.com/toyinogun/AIITsupport.git", "https://github.com/toyinogun/AIITsupport/"},
	}
	for _, c := range same {
		if !sameRepo(c[0], c[1]) {
			t.Errorf("sameRepo(%s, %s) = false", c[0], c[1])
		}
	}
	if sameRepo("https://github.com/fork/AIITsupport.git", "https://github.com/toyinogun/AIITsupport.git") {
		t.Error("a fork matched the original")
	}
}

func TestLastLineRedactsURLCredentials(t *testing.T) {
	got := lastLine("noise\nfatal: unable to access 'https://user:ghp_abc@github.com/o/r.git/': 403\n")
	if strings.Contains(got, "ghp_abc") || !strings.Contains(got, "https://[redacted]@github.com") {
		t.Errorf("lastLine = %q, want the credentials redacted", got)
	}
}

// covers: AC-5, AC-6 (spec 0015). The commit stages and commits only
// values.yaml and sealed-values.yaml, with the git hooks off.
func TestCommit(t *testing.T) {
	staged := "deploy/clusters/cc/sealed-values.yaml"
	r := &fakeRunner{answers: map[string]func(Cmd) ([]byte, error){
		"git add":    say(""),
		"git diff":   func(Cmd) ([]byte, error) { return []byte(staged), nil },
		"git commit": say(""),
	}}
	g := Git{run: r}
	ok, err := g.Commit(t.Context(), "deploy/clusters/cc", "msg")
	if err != nil || !ok {
		t.Fatalf("Commit = %t, %v", ok, err)
	}
	want := "deploy/clusters/cc/values.yaml deploy/clusters/cc/sealed-values.yaml"
	for _, c := range r.calls {
		if (c.Args[0] == "add" || c.Args[0] == "commit") && !strings.HasSuffix(strings.Join(c.Args, " "), "-- "+want) {
			t.Errorf("git %s args = %v, want only the two files", c.Args[0], c.Args)
		}
	}
	last := r.calls[len(r.calls)-1]
	if last.Args[0] != "commit" || !slices.Contains(last.Env, "LEFTHOOK=0") {
		t.Errorf("commit call = %+v, want LEFTHOOK=0", last)
	}
	staged = "deploy/clusters/cc/secrets.env"
	if _, err := g.Commit(t.Context(), "deploy/clusters/cc", "msg"); err == nil {
		t.Error("Commit took a file other than the two")
	}
	staged = ""
	if ok, err := g.Commit(t.Context(), "deploy/clusters/cc", "msg"); err != nil || ok {
		t.Errorf("nothing staged: Commit = %t, %v, want no commit", ok, err)
	}
}

func TestPushNamesTheUpstreamBranch(t *testing.T) {
	r := &fakeRunner{answers: map[string]func(Cmd) ([]byte, error){"git push": say("")}}
	if err := (Git{run: r}).Push(t.Context(), Tracking{Remote: "origin", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.calls[0].Args, " "); got != "push --quiet origin HEAD:refs/heads/main" {
		t.Errorf("push args = %q", got)
	}
}

// covers: AC-2 (spec 0015). The registry check follows the anonymous token
// flow and tells a missing tag from a present one.
func TestRegistryExists(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token" && strings.Contains(r.URL.Query().Get("scope"), "nopkg"):
			w.WriteHeader(http.StatusForbidden) // what GHCR does for a package that does not exist
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:o/aiitsupport-agent:pull" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t"}`))
		case r.Header.Get("Authorization") != "Bearer t":
			scope := "repository:o/aiitsupport-agent:pull"
			if strings.Contains(r.URL.Path, "nopkg") {
				scope = "repository:o/nopkg:pull"
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="`+scope+`"`)
			w.WriteHeader(http.StatusUnauthorized)
		case strings.HasSuffix(r.URL.Path, "/manifests/good"):
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	reg := Registry{Client: srv.Client(), Scheme: "http"}
	host := strings.TrimPrefix(srv.URL, "http://")
	for tag, want := range map[string]bool{"good": true, "missing": false} {
		got, err := reg.Exists(t.Context(), host, "o/aiitsupport-agent", tag)
		if err != nil || got != want {
			t.Errorf("Exists(%s) = %t, %v, want %t", tag, got, err, want)
		}
	}
	if got, err := reg.Exists(t.Context(), host, "o/nopkg", "good"); err != nil || got {
		t.Errorf("Exists(no package) = %t, %v, want false and no error", got, err)
	}
}

func TestSetCACertificateKeepsTheRest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ClustersDir, "cc")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, valuesFile), []byte("# keep me\ncluster:\n  name: cc\nca:\n  certificate: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFolder(root, "cc")
	if err != nil {
		t.Fatal(err)
	}
	pem := "-----BEGIN CERTIFICATE-----\nAAA\n-----END CERTIFICATE-----\n"
	if changed, err := f.SetCACertificate(pem); err != nil || !changed {
		t.Fatalf("SetCACertificate = %t, %v", changed, err)
	}
	if changed, err := f.SetCACertificate(pem); err != nil || changed {
		t.Errorf("same PEM again = %t, %v, want no change", changed, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, valuesFile)) //nolint:gosec // a fixed path in t.TempDir
	if !strings.Contains(string(raw), "# keep me") || !strings.Contains(string(raw), "certificate: |") {
		t.Errorf("values.yaml =\n%s\nwant the comment kept and a literal block", raw)
	}
}

func TestOpenFolderRefusesBadNames(t *testing.T) {
	for _, name := range []string{"", "../etc", "Upper", "a"} {
		if _, err := OpenFolder(t.TempDir(), name); err == nil {
			t.Errorf("OpenFolder(%q) succeeded", name)
		}
	}
}
