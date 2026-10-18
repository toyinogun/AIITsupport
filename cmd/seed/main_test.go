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
	var out bytes.Buffer
	if err := run([]string{"validate"}, &out); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(out.String(), "is valid: 85 users, 1 admins, 22 groups") {
		t.Errorf("output = %q", out.String())
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
