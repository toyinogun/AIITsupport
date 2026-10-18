package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRunUsageErrors(t *testing.T) {
	cases := [][]string{
		nil,
		{"frobnicate"},
		{"login"},
		{"lock"},
		{"lock", "a.b", "c.d"},
		{"validate", "extra"},
		{"helpdesk", "extra"},
	}
	for _, args := range cases {
		err := run(args, &bytes.Buffer{})
		if !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want the usage error", args, err)
		}
	}
}

// covers: AC-14. validate needs no passwords and no network.
func TestRunValidate(t *testing.T) {
	t.Setenv("SEED_FILE", "../../seed/directory/noord.yaml")
	t.Setenv("SEED_OVERLAY_FILE", "../../seed/directory/does-not-exist.yaml")
	t.Setenv("SEED_ENTRA_DEMO_FILE", "../../seed/entra/does-not-exist.yaml")
	t.Setenv("SEED_HELPDESK_DIR", "../../seed/helpdesk")
	var out bytes.Buffer
	if err := run([]string{"validate"}, &out); err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, want := range []string{
		"is valid: 85 users, 1 admins, 23 groups, 0 Entra demo users",
		"is valid: 6 KB categories, 15 KB articles, 20 tickets",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, want %q", out.String(), want)
		}
	}
}

func TestRunCerts(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"certs", "--dir", dir, "--host", "extra.test"}, &out); err != nil {
		t.Fatalf("certs: %v", err)
	}
	if !strings.Contains(out.String(), "wrote ca.pem, ca.key, server.pem, server.key") {
		t.Errorf("output = %q", out.String())
	}
}

// Commands that reach the directory fail before any network call when a
// setting is missing or the CA file is absent, and say which one.
func TestRunNamesTheMissingSetting(t *testing.T) {
	t.Setenv("SEED_FILE", "../../seed/directory/noord.yaml")
	t.Setenv("SEED_OVERLAY_FILE", "../../seed/directory/does-not-exist.yaml")

	t.Run("passwords unset", func(t *testing.T) {
		t.Setenv("SEED_ADMIN_PASSWORD", "")
		t.Setenv("SEED_USER_PASSWORD", "")
		for _, cmd := range [][]string{{"directory"}, {"login", "eva.peters"}, {"lock", "eva.peters"}} {
			err := run(cmd, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "SEED_ADMIN_PASSWORD") {
				t.Errorf("run(%q) = %v, want an error naming SEED_ADMIN_PASSWORD", cmd, err)
			}
		}
	})

	t.Run("CA file missing", func(t *testing.T) {
		t.Setenv("SEED_ADMIN_PASSWORD", "admin-test")
		t.Setenv("SEED_USER_PASSWORD", "user-test")
		t.Setenv("SEED_CA_CERT", t.TempDir()+"/missing.pem")
		err := run([]string{"directory"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "run make certs") {
			t.Fatalf("directory = %v, want an error pointing at make certs", err)
		}
	})
}

func unsetEntra(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"SEED_GRAPH_TENANT_ID", "SEED_GRAPH_CLIENT_ID", "SEED_GRAPH_CLIENT_SECRET", "SEED_ENTRA_DOMAIN",
		"GRAPH_TENANT_ID", "GRAPH_CLIENT_ID", "GRAPH_CLIENT_SECRET",
	} {
		t.Setenv(name, "")
	}
}

func TestRunEntraUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"entra"}, {"entra", "frob"}, {"entra", "mfa"}, {"entra", "demo-password"}, {"entra", "sync", "extra"}} {
		if err := run(args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("run(%q) = %v, want the usage error", args, err)
		}
	}
}

// covers: AC-13 (spec 0005). With SEED_GRAPH_* unset the sync skips and
// succeeds, without reaching Samba.
func TestRunEntraSyncUnconfigured(t *testing.T) {
	unsetEntra(t)
	t.Setenv("SEED_ADMIN_PASSWORD", "admin-test")
	t.Setenv("SEED_USER_PASSWORD", "user-test")
	t.Setenv("SEED_LDAP_URL", "ldaps://127.0.0.1:1")
	var out bytes.Buffer
	if err := run([]string{"entra", "sync"}, &out); err != nil {
		t.Fatalf("entra sync: %v", err)
	}
	if !strings.Contains(out.String(), "Entra not configured, skipping") {
		t.Errorf("output = %q, want the skip line", out.String())
	}
}

// covers: AC-9, AC-12 (spec 0005). The MFA proof needs the tool server app
// and says which setting is missing.
func TestRunEntraNamesMissingSettings(t *testing.T) {
	unsetEntra(t)
	err := run([]string{"entra", "mfa", "eva.peters"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "GRAPH_TENANT_ID") {
		t.Errorf("entra mfa = %v, want an error naming GRAPH_TENANT_ID", err)
	}
}

// covers: AC-12 (spec 0005). Samba not answering fails naming the URL
// setting before any Graph call.
func TestRunEntraSyncSambaDown(t *testing.T) {
	t.Setenv("SEED_FILE", "../../seed/directory/noord.yaml")
	t.Setenv("SEED_OVERLAY_FILE", "../../seed/directory/does-not-exist.yaml")
	t.Setenv("SEED_ENTRA_DEMO_FILE", "../../seed/entra/does-not-exist.yaml")
	t.Setenv("SEED_ADMIN_PASSWORD", "admin-test")
	t.Setenv("SEED_USER_PASSWORD", "user-test")
	t.Setenv("SEED_GRAPH_TENANT_ID", "tenant")
	t.Setenv("SEED_GRAPH_CLIENT_ID", "client")
	t.Setenv("SEED_GRAPH_CLIENT_SECRET", "secret")
	t.Setenv("SEED_ENTRA_DOMAIN", "noordtest.onmicrosoft.com")
	t.Setenv("SEED_LDAP_URL", "ldaps://127.0.0.1:1")
	dir := t.TempDir()
	if err := run([]string{"certs", "--dir", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEED_CA_CERT", dir+"/ca.pem")

	err := run([]string{"entra", "sync"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "SEED_LDAP_URL") || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("entra sync = %v, want an error naming SEED_LDAP_URL and the URL", err)
	}
}
