package install

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/toyinogun/AIITsupport/internal/seed/certs"
)

// writeServerCert writes a self signed server.pem expiring at notAfter.
func writeServerCert(t *testing.T, dir string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "samba"}, NotBefore: notAfter.Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, certs.ServerFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, certs.ServerKeyFile), []byte("key"), 0o600))
}

// covers: AC-10 (spec 0015). The Samba server certificate is made again
// within 60 days of expiry, and kept otherwise.
func TestRenewServerCert(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		expires time.Time
		kept    bool
	}{
		"far off": {now.Add(400 * 24 * time.Hour), true},
		"soon":    {now.Add(30 * 24 * time.Hour), false},
		"expired": {now.Add(-time.Hour), false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeServerCert(t, dir, tc.expires)
			must(t, renewServerCert(dir, now))
			_, err := os.Stat(filepath.Join(dir, certs.ServerFile))
			if kept := err == nil; kept != tc.kept {
				t.Errorf("server.pem kept = %t, want %t", kept, tc.kept)
			}
		})
	}
	if err := renewServerCert(t.TempDir(), now); err != nil {
		t.Errorf("no cert yet: %v, want nil", err)
	}
}

// covers: AC-6, AC-10 (spec 0015). Samba's Secret carries its accounts'
// passwords and the server pair from the cert dir; a missing file is named.
func TestBuildDirectory(t *testing.T) {
	tbl := realTable(t)
	env := tracerEnv(t)
	comps := components(true)
	_, err := tbl.Generate(t.Context(), env, fakeLive{}, comps, true)
	must(t, err)
	files := map[string]string{certs.ServerFile: "CERT", certs.ServerKeyFile: "KEY"}
	plain, err := tbl.Build(env, files, comps, true)
	must(t, err)
	samba := plain["noord-samba"]
	if samba["tls.crt"] != "CERT" || samba["tls.key"] != "KEY" || samba["SAMBA_ADMIN_PASSWORD"] != env.Get("SEED_ADMIN_PASSWORD") ||
		samba["SAMBA_SVC_PASSWORD"] != env.Get("LDAP_BIND_PASSWORD") {
		t.Error("noord-samba does not carry the passwords and the server pair")
	}
	if _, ok := samba["SAMBA_PWRESET_PASSWORD"]; ok {
		t.Error("svc-pwreset's password was built with the reset feature off")
	}
	if _, ok := plain["noord-toolserver"]["LDAP_URL"]; ok {
		t.Error("demo on still sealed LDAP_URL; the chart sets it")
	}
	if _, err := tbl.Build(env, nil, comps, true); err == nil || !strings.Contains(err.Error(), "the cert file server.pem") {
		t.Errorf("no cert files: err = %v, want server.pem named", err)
	}
}

// covers: AC-6 (spec 0015). The overlays Secret holds exactly the overlay
// files that exist, built in memory, and is empty without them.
func TestOverlays(t *testing.T) {
	for name, present := range map[string][]string{"none": nil, "one": {"seed/directory/overlay.local.yaml"}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			for _, p := range present {
				must(t, os.MkdirAll(filepath.Join(f.root, filepath.Dir(p)), 0o750))
				must(t, os.WriteFile(filepath.Join(f.root, p), []byte("users: []\n"), 0o600))
			}
			var applied []byte
			f.run.answers["kubectl --context k3sprox apply"] = func(c Cmd) ([]byte, error) { applied = c.Stdin; return nil, nil }
			s := &state{Options: f.options().Defaults(), demo: true, kube: Kube{run: f.run, context: "k3sprox"}}
			s.Cluster = "k3sprox"
			must(t, s.overlays(t.Context()))
			var secret struct {
				Metadata struct{ Name, Namespace string }
				Data     map[string]string
			}
			must(t, yaml.Unmarshal(applied, &secret))
			if secret.Metadata.Name != overlaysSecret || secret.Metadata.Namespace != directoryNamespace || len(secret.Data) != len(present) {
				t.Fatalf("applied %+v, want %s with %d keys", secret, overlaysSecret, len(present))
			}
			if len(present) == 1 {
				raw, _ := base64.StdEncoding.DecodeString(secret.Data["overlay.local.yaml"])
				if string(raw) != "users: []\n" {
					t.Errorf("overlay.local.yaml = %q", raw)
				}
			}
		})
	}
}

// covers: AC-6, AC-10 (spec 0015). A demo run makes the CA and server pair
// in ~/.noord/<cluster>/, writes the CA into values.yaml, seals the
// directory's Secrets, applies the overlays, and waits for noord-directory.
func TestRunDemo(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, ClustersDir, "k3sprox", valuesFile)
	raw, err := os.ReadFile(path) //nolint:gosec // a fixed path in t.TempDir
	must(t, err)
	must(t, os.WriteFile(path, []byte(strings.Replace(string(raw), "demo: {enabled: false}", "demo: {enabled: true}", 1)), 0o600)) //nolint:gosec // a fixed path in t.TempDir
	if err := Run(context.Background(), f.options()); err != nil {
		t.Fatalf("demo run: %v\n%s", err, f.out.String())
	}
	ca, err := os.ReadFile(filepath.Join(f.root, ".noord", "k3sprox", certs.CAFile))
	must(t, err)
	values, err := os.ReadFile(path) //nolint:gosec // a fixed path in t.TempDir
	must(t, err)
	if !strings.Contains(string(values), strings.TrimSpace(strings.Split(string(ca), "\n")[1])) {
		t.Error("values.yaml does not hold the CA certificate made in the cert dir")
	}
	sealed, err := Folder{Dir: filepath.Join(ClustersDir, "k3sprox"), root: f.root}.ReadSealed()
	must(t, err)
	for _, name := range []string{"noord-samba", "noord-seed-directory"} {
		if len(sealed[name]) == 0 {
			t.Errorf("no %s block sealed", name)
		}
	}
	if !slices.ContainsFunc(f.run.calls, func(c Cmd) bool { return strings.Contains(string(c.Stdin), overlaysSecret) }) {
		t.Error("the overlays Secret was never applied")
	}
	if !slices.ContainsFunc(f.run.calls, func(c Cmd) bool {
		return slices.Contains(c.Args, "noord-directory") && slices.Contains(c.Args, "applications.argoproj.io")
	}) {
		t.Error("the wait never asked for noord-directory")
	}
}
