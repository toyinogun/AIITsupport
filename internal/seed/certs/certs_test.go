package certs

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
)

func TestGenerateMakesAVerifiableServerCert(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(dir, []string{"extra.test"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Written) != 4 || len(res.Skipped) != 0 {
		t.Fatalf("first run wrote %v, skipped %v; want all four written", res.Written, res.Skipped)
	}

	pool, err := noordldap.LoadCA(filepath.Join(dir, CAFile))
	if err != nil {
		t.Fatalf("Pool: %v", err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, ServerFile), filepath.Join(dir, ServerKeyFile))
	if err != nil {
		t.Fatalf("load server pair: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	for _, host := range append(slices.Clone(DefaultHosts), "extra.test") {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: pool}); err != nil {
			t.Errorf("server cert does not verify for %s: %v", host, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "elsewhere.example", Roots: pool}); err == nil {
		t.Error("server cert verified for a host it should not cover")
	}
}

func TestGenerateKeepsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, nil); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, CAFile)) //nolint:gosec // G304: a file in the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}

	res, err := Generate(dir, nil)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if len(res.Written) != 0 {
		t.Errorf("second run wrote %v, want nothing", res.Written)
	}
	after, err := os.ReadFile(filepath.Join(dir, CAFile)) //nolint:gosec // G304: a file in the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("second run replaced the CA")
	}
}

func TestGenerateSignsANewServerCertWithTheExistingCA(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, nil); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ServerFile)); err != nil {
		t.Fatal(err)
	}

	res, err := Generate(dir, nil)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if !slices.Contains(res.Written, ServerFile) || slices.Contains(res.Written, CAFile) {
		t.Fatalf("wrote %v, want only the server pair", res.Written)
	}
	pool, err := noordldap.LoadCA(filepath.Join(dir, CAFile))
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, ServerFile), filepath.Join(dir, ServerKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "samba", Roots: pool}); err != nil {
		t.Errorf("new server cert does not chain to the kept CA: %v", err)
	}
}

func TestKeyFilesArePrivate(t *testing.T) {
	dir := t.TempDir()
	if _, err := Generate(dir, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{CAKeyFile, ServerKeyFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, perm)
		}
	}
}

// A damaged CA must stop Generate with a clear message, never be silently
// replaced: clients may already trust the old one.
func TestGenerateRefusesADamagedCA(t *testing.T) {
	cases := []struct {
		name     string
		damage   func(t *testing.T, dir string)
		wantText string
	}{
		{
			name: "CA files are not PEM",
			damage: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, CAFile), "not a certificate")
			},
			wantText: "are not PEM; delete them and run make certs",
		},
		{
			name: "CA certificate does not parse",
			damage: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, CAFile), "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
			},
			wantText: "parse CA certificate",
		},
		{
			name: "CA key does not parse",
			damage: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, CAKeyFile), "-----BEGIN EC PRIVATE KEY-----\nAAAA\n-----END EC PRIVATE KEY-----\n")
			},
			wantText: "parse CA key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Generate(dir, nil); err != nil {
				t.Fatalf("first Generate: %v", err)
			}
			tc.damage(t, dir)
			// Remove the server pair so Generate has to load the CA to sign a new one.
			for _, f := range []string{ServerFile, ServerKeyFile} {
				if err := os.Remove(filepath.Join(dir, f)); err != nil {
					t.Fatal(err)
				}
			}

			_, err := Generate(dir, nil)

			if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("Generate = %v, want an error containing %q", err, tc.wantText)
			}
			if _, statErr := os.Stat(filepath.Join(dir, ServerFile)); statErr == nil {
				t.Error("a server cert was written despite the damaged CA")
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
