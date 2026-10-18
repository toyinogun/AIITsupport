package ldap_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	noordldap "github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/seed/certs"
)

func TestLoadCA(t *testing.T) {
	t.Run("reads the generated CA", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := certs.Generate(dir, nil); err != nil {
			t.Fatal(err)
		}
		pool, err := noordldap.LoadCA(filepath.Join(dir, certs.CAFile))
		if err != nil || pool == nil {
			t.Fatalf("LoadCA = %v, %v; want a pool", pool, err)
		}
	})

	t.Run("a missing CA points at make certs", func(t *testing.T) {
		_, err := noordldap.LoadCA(filepath.Join(t.TempDir(), "missing.pem"))
		if err == nil || !strings.Contains(err.Error(), "run make certs") {
			t.Fatalf("LoadCA = %v, want an error pointing at make certs", err)
		}
	})

	t.Run("a file with no certificate is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(path, []byte("nothing to see here"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := noordldap.LoadCA(path)
		if err == nil || !strings.Contains(err.Error(), "holds no PEM certificate") {
			t.Fatalf("LoadCA = %v, want a no certificate error", err)
		}
	})
}

func TestDialerRefusesPlainLDAPAndMissingCA(t *testing.T) {
	if _, err := (noordldap.Dialer{URL: "ldap://127.0.0.1:1"}).Dial(); err == nil || !strings.Contains(err.Error(), "ldaps://") {
		t.Errorf("Dial over ldap:// = %v, want a refusal naming ldaps://", err)
	}
	if _, err := (noordldap.Dialer{URL: "ldaps://127.0.0.1:1"}).Dial(); err == nil || !strings.Contains(err.Error(), "no CA") {
		t.Errorf("Dial with no CA = %v, want a refusal", err)
	}
}
