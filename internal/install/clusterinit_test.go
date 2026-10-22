package install

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codeLines(raw []byte) string {
	var out []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// covers: AC-23 (spec 0015). The generated bootstrap.yaml is k3sprox's
// committed one with the cluster's own name, repository and branch.
func TestBootstrapMatchesK3sprox(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join(repoRoot, ClustersDir, "k3sprox", bootstrapFile))
	must(t, err)
	got := Bootstrap("k3sprox", "https://github.com/toyinogun/AIITsupport.git", "main")
	if codeLines(got) != codeLines(committed) {
		t.Error("bootstrapTemplate drifted from deploy/clusters/k3sprox/bootstrap.yaml")
	}
}

func TestPrefixMath(t *testing.T) {
	p := func(s string) netip.Prefix { return netip.MustParsePrefix(s) }
	if got := cover([]netip.Prefix{p("172.16.70.20/32"), p("172.16.70.23/32")}); got != p("172.16.70.20/30") {
		t.Errorf("cover = %s", got)
	}
	if got := cover([]netip.Prefix{p("10.0.0.5/32")}); got != p("10.0.0.5/32") {
		t.Errorf("cover of one = %s", got)
	}
	if got := widen(p("10.42.1.0/24"), 16); got != p("10.42.0.0/16") {
		t.Errorf("widen = %s", got)
	}
	if got := widen(p("10.0.0.0/8"), 16); got != p("10.0.0.0/8") {
		t.Errorf("widen shrank %s", got)
	}
}

func TestHTTPSRepo(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:Toyin/Repo.git":              "https://github.com/Toyin/Repo.git",
		"https://github.com/Toyin/Repo":              "https://github.com/Toyin/Repo.git",
		"https://user:tok@github.com/Toyin/Repo.git": "https://github.com/Toyin/Repo.git",
	} {
		got, owner, err := httpsRepo(in)
		if err != nil || got != want || owner != "toyin" {
			t.Errorf("httpsRepo(%q) = %q, %q, %v", in, got, owner, err)
		}
	}
	if _, _, err := httpsRepo("nonsense"); err == nil {
		t.Error("accepted nonsense")
	}
}

// covers: AC-19 (spec 0015). cluster-init picks the newest commit with all
// four images, reads the ranges from the cluster, leaves out operators the
// cluster has, and never overwrites a folder.
func TestClusterInit(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, ClustersDir, "_template"), 0o750))
	for _, f := range []string{valuesFile, secretsFile} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, ClustersDir, "_template", f)) //nolint:gosec // a fixed repo file
		must(t, err)
		must(t, os.WriteFile(filepath.Join(root, ClustersDir, "_template", f), raw, 0o600)) //nolint:gosec // t.TempDir
	}
	newest, older := strings.Repeat("b", 40), strings.Repeat("a", 40)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/manifests/"+newest) && strings.Contains(r.URL.Path, "aiitsupport-samba") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(reg.Close)
	kc := "kubectl --context lab "
	run := &fakeRunner{answers: map[string]func(Cmd) ([]byte, error){
		"git remote get-url":             say("git@github.com:Acme/Noord.git\n"),
		"git symbolic-ref":               say("origin/main\n"),
		"git fetch":                      say(""),
		"git log":                        say(newest + "\n" + older + "\n"),
		"kubectl config current-context": say("lab\n"),
		kc + "get nodes": say(`{"items":[
			{"spec":{"podCIDRs":["10.42.0.0/24"]},"status":{"addresses":[{"type":"InternalIP","address":"192.168.1.10"}]}},
			{"spec":{"podCIDRs":["10.42.1.0/24"]},"status":{"addresses":[{"type":"InternalIP","address":"192.168.1.11"}]}}]}`),
		kc + "get service kubernetes":                   say("10.43.0.1"),
		kc + "get endpointslices.discovery.k8s.io":      say(`{"items":[{"endpoints":[{"addresses":["192.168.1.10"]}]}]}`),
		kc + "get daemonset cilium":                     say("daemonset.apps/cilium\n"),
		kc + "get deployment sealed-secrets-controller": say("deployment.apps/sealed-secrets-controller\n"),
		kc + "get crd":                                  say(""),
	}}
	opts := Options{
		Root: root, Cluster: "lab", Out: &strings.Builder{}, Run: run, RegistryHost: strings.TrimPrefix(reg.URL, "http://"),
		Registry: Registry{Client: reg.Client(), Scheme: "http"},
	}
	must(t, ClusterInit(t.Context(), opts))

	v, err := os.ReadFile(filepath.Join(root, ClustersDir, "lab", valuesFile)) //nolint:gosec // t.TempDir
	must(t, err)
	for _, want := range []string{
		"name: lab", "podCIDR: 10.42.0.0/16", "serviceCIDR: 10.43.0.0/16", "nodeCIDR: 192.168.1.10/31", "apiServerCIDR: 192.168.1.10/32",
		"repo:", "url: https://github.com/Acme/Noord.git", "owner: acme", "tag: " + older, "fqdnEgress: true",
	} {
		if !strings.Contains(string(v), want) {
			t.Errorf("values.yaml lacks %q:\n%s", want, v)
		}
	}
	folder := Folder{Name: "lab", Dir: ClustersDir + "/lab", root: root}
	parsed, err := folder.Values()
	must(t, err)
	if parsed.Platform.SealedSecrets.Enabled || !parsed.Platform.CertManager.Enabled || !parsed.Platform.CNPG.Enabled {
		t.Errorf("platform = %+v, want only the operators the cluster lacks", parsed.Platform)
	}
	env, err := ReadEnvFile(filepath.Join(root, ClustersDir, "lab", secretsFile))
	must(t, err)
	info, _ := os.Stat(filepath.Join(root, ClustersDir, "lab", secretsFile))
	if env.Get("KUBE_CONTEXT") != "lab" || info.Mode().Perm() != 0o600 {
		t.Errorf("secrets.env: context %q, mode %o", env.Get("KUBE_CONTEXT"), info.Mode().Perm())
	}
	if err := ClusterInit(t.Context(), opts); err == nil {
		t.Error("a second cluster-init overwrote the folder")
	}
}
