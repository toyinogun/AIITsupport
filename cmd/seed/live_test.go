package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/seed/directory/sambatest"
)

// covers: AC-1, AC-8, AC-9, AC-13. The real commands against one throwaway
// directory, in the order you would run them by hand. Runs only with
// TEST_SAMBA=1.
func TestCommandsAgainstALiveDirectory(t *testing.T) {
	s := sambatest.New(t, "../../seed/directory/samba")
	t.Setenv("SEED_LDAP_URL", s.URL)
	t.Setenv("SEED_CA_CERT", s.CAPath)
	t.Setenv("SEED_ADMIN_USER", sambatest.AdminDN)
	t.Setenv("SEED_ADMIN_PASSWORD", sambatest.AdminPassword)
	t.Setenv("SEED_USER_PASSWORD", sambatest.UserPassword)
	t.Setenv("SEED_ZAMMAD_BIND_PASSWORD", sambatest.ZammadPassword)
	t.Setenv("SEED_FILE", "../../seed/directory/noord.yaml")
	t.Setenv("SEED_OVERLAY_FILE", "../../seed/directory/does-not-exist.yaml")

	// A failed command must never echo a password back.
	noSecrets := func(t *testing.T, text string) {
		t.Helper()
		for _, secret := range []string{sambatest.AdminPassword, sambatest.UserPassword} {
			if strings.Contains(text, secret) {
				t.Errorf("output leaks a password: %q", text)
			}
		}
	}

	t.Run("a wrong Administrator password fails without retrying or leaking it", func(t *testing.T) {
		t.Setenv("SEED_ADMIN_PASSWORD", sambatest.AdminPassword+"-wrong")
		err := run([]string{"directory"}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("directory with a wrong Administrator password: want an error")
		}
		noSecrets(t, err.Error())
	})

	t.Run("directory seeds a fresh directory and logs the counts", func(t *testing.T) {
		var out bytes.Buffer
		if err := run([]string{"directory"}, &out); err != nil {
			t.Fatalf("directory: %v", err)
		}
		for _, want := range []string{`"msg":"directory seeded"`, `"users":85`, `"admins":1`, `"groups":23`, `"locked":3`, `"duration_ms":`} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("log %q lacks %s", out.String(), want)
			}
		}
	})

	t.Run("directory again says already seeded", func(t *testing.T) {
		var out bytes.Buffer
		if err := run([]string{"directory"}, &out); err != nil {
			t.Fatalf("second directory: %v", err)
		}
		if !strings.Contains(out.String(), `"msg":"already seeded"`) {
			t.Errorf("log = %q, want already seeded", out.String())
		}
	})

	t.Run("login as an unlocked user reports the lock state", func(t *testing.T) {
		var out bytes.Buffer
		if err := run([]string{"login", "eva.peters"}, &out); err != nil {
			t.Fatalf("login: %v", err)
		}
		if !strings.Contains(out.String(), "bound as CN=Eva Peters,OU=Sales,OU=Users,OU=Noord,") ||
			!strings.Contains(out.String(), "locked: false, bad password count: 0") {
			t.Errorf("output = %q", out.String())
		}
	})

	t.Run("login as a seeded locked user says locked", func(t *testing.T) {
		for _, user := range []string{"jan.devries2", "yara.schouten", "kevin.smits"} {
			err := run([]string{"login", user}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "the account is locked") {
				t.Errorf("login %s = %v, want the account is locked", user, err)
			}
		}
	})

	t.Run("a wrong password counts but does not lock", func(t *testing.T) {
		err := run([]string{"login", "--password", "Not-The-Password-1", "fleur.dijkstra"}, &bytes.Buffer{})
		if err == nil {
			t.Fatal("login with a wrong password: want an error")
		}
		if !strings.Contains(err.Error(), "locked: false, bad password count: 1") || !strings.Contains(err.Error(), "invalid credentials") {
			t.Errorf("error = %q", err)
		}
		noSecrets(t, err.Error())
	})

	t.Run("an unknown user is named, not bound", func(t *testing.T) {
		for _, cmd := range [][]string{{"login", "nobody.here"}, {"lock", "nobody.here"}} {
			err := run(cmd, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), `unknown user "nobody.here"`) {
				t.Errorf("run(%q) = %v, want unknown user", cmd, err)
			}
		}
	})

	t.Run("lock locks a user and login then says locked", func(t *testing.T) {
		var out bytes.Buffer
		if err := run([]string{"lock", "eva.peters"}, &out); err != nil {
			t.Fatalf("lock: %v", err)
		}
		if strings.TrimSpace(out.String()) != "locked eva.peters" {
			t.Errorf("lock output = %q", out.String())
		}
		err := run([]string{"login", "eva.peters"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "the account is locked") {
			t.Fatalf("login after lock = %v, want the account is locked", err)
		}
		noSecrets(t, err.Error())
	})
}
