package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

// clearEnv blanks the settings run reads, so the developer's shell never leaks in.
// A blank value counts as unset in internal/config.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"TOOLSERVER_DATABASE_URL", "TOOLSERVER_TOKEN", "TOOLSERVER_URL", "OTEL_EXPORTER_OTLP_ENDPOINT", "LOG_LEVEL",
		"READ_ONLY", "TOOLSERVER_HTTP_ADDR", "KILL_SWITCH_FILE", "LDAP_URL", "LDAP_BASE_DN", "LDAP_BIND_DN",
		"LDAP_BIND_PASSWORD", "LDAP_CA_CERT", "ZAMMAD_URL", "ZAMMAD_TOKEN", "ZAMMAD_DEFAULT_GROUP",
		"TOOL_USER_OUS", "TOOL_GROUP_OUS", "TOOL_DENIED_GROUPS",
	} {
		t.Setenv(name, "")
	}
}

// requiredEnv sets every required setting, pointing the database at
// databaseURL. Nothing it names is dialed at startup.
func requiredEnv(t *testing.T, databaseURL string) {
	t.Helper()
	t.Setenv("TOOLSERVER_DATABASE_URL", databaseURL)
	t.Setenv("TOOLSERVER_TOKEN", "test-token-0123456789abcdefghijk")
	t.Setenv("LDAP_URL", "ldaps://127.0.0.1:1")
	t.Setenv("LDAP_BASE_DN", "DC=ad,DC=noord,DC=example")
	t.Setenv("LDAP_BIND_DN", "CN=svc-itsupport,OU=Service Accounts,OU=Noord,DC=ad,DC=noord,DC=example")
	t.Setenv("LDAP_BIND_PASSWORD", "bind-test")
	t.Setenv("LDAP_CA_CERT", writeCA(t))
	t.Setenv("ZAMMAD_URL", "http://127.0.0.1:1")
	t.Setenv("ZAMMAD_TOKEN", "dev-only-zammad-token-change-me-0001")
	t.Setenv("ZAMMAD_DEFAULT_GROUP", "IT Support")
}

// writeCA writes a throwaway self signed CA, since startup reads LDAP_CA_CERT.
func writeCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// covers: SD-2
func TestRunRefusesToStartWithoutRequiredSettings(t *testing.T) {
	clearEnv(t)

	err := run()
	if err == nil {
		t.Fatal("run should refuse to start with no settings")
	}
	for _, name := range []string{"TOOLSERVER_DATABASE_URL", "TOOLSERVER_TOKEN", "LDAP_URL", "LDAP_CA_CERT", "ZAMMAD_URL", "ZAMMAD_DEFAULT_GROUP"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

// covers: SD-2
func TestRunRefusesToStartWhenDatabaseIsUnreachable(t *testing.T) {
	clearEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	requiredEnv(t, "postgres://noord:x@"+addr+"/noord?sslmode=disable&connect_timeout=2")

	err = run()
	if err == nil || !strings.Contains(err.Error(), "reach database") {
		t.Fatalf("run = %v, want a reach database error", err)
	}
}

// Runs only when TEST_DATABASE_URL is set (see internal/db/dbtest).
func TestRunNamesTheAddressSettingWhenListenFails(t *testing.T) {
	tdb := dbtest.New(t)
	clearEnv(t)
	requiredEnv(t, tdb.URL(dbtest.RoleToolServer))
	t.Setenv("TOOLSERVER_HTTP_ADDR", "not-an-address")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "TOOLSERVER_HTTP_ADDR") {
		t.Fatalf("run = %v, want an error naming TOOLSERVER_HTTP_ADDR", err)
	}
}

// covers: SD-2
func TestRunRejectsAMalformedReadOnly(t *testing.T) {
	clearEnv(t)
	requiredEnv(t, "postgres://noord:x@127.0.0.1:1/noord")
	t.Setenv("READ_ONLY", "maybe")

	err := run()
	if err == nil || !strings.Contains(err.Error(), "READ_ONLY: not true or false") {
		t.Fatalf("run = %v, want a READ_ONLY error", err)
	}
}

// covers: AC-16 (spec 0007). A bad OU list or a short token stops startup,
// before any database or directory is reached.
func TestRunRefusesBadToolSettings(t *testing.T) {
	for name, set := range map[string][2]string{
		"OU with DC=": {"TOOL_USER_OUS", "OU=Users,OU=Noord,DC=ad,DC=noord,DC=example"},
		"empty OU":    {"TOOL_GROUP_OUS", "OU=Groups,OU=Noord;"},
		"short token": {"TOOLSERVER_TOKEN", "short"},
		"missing CA":  {"LDAP_CA_CERT", "/nonexistent/ca.pem"},
		"not a DN":    {"TOOL_USER_OUS", "Users"},
	} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			requiredEnv(t, "postgres://noord:x@127.0.0.1:1/noord?connect_timeout=1")
			t.Setenv(set[0], set[1])
			err := run()
			if err == nil || !strings.Contains(err.Error(), set[0]) || strings.Contains(err.Error(), "reach database") {
				t.Fatalf("run = %v, want a refusal naming %s before the database", err, set[0])
			}
		})
	}
}
