package env

import (
	"strings"
	"testing"
)

// covers: AC-2 (spec 0018). The child's environment is built from an empty
// list: a LDAP_URL in the harness's own environment never reaches it, and the
// settings it does get are the ones the harness made.
func TestChildEnvIgnoresTheHarnessEnvironment(t *testing.T) {
	t.Setenv("LDAP_URL", "ldaps://production.example:636")
	t.Setenv("ZAMMAD_URL", "https://zammad.production.example")
	t.Setenv("GRAPH_BASE_URL", "https://graph.production.example")
	got := childEnv(ChildConfig{LDAPURL: "ldaps://127.0.0.1:1", ZammadURL: "http://127.0.0.1:2"},
		"127.0.0.1:3", "127.0.0.1:4", "/tmp/kill")
	seen := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		seen[k] = v
	}
	for k, v := range seen {
		if strings.Contains(v, "production.example") {
			t.Errorf("%s=%s came from the harness's own environment", k, v)
		}
	}
	if seen["LDAP_URL"] != "ldaps://127.0.0.1:1" || seen["ZAMMAD_URL"] != "http://127.0.0.1:2" {
		t.Errorf("child settings = %q", seen)
	}
	if seen["RESET_PUBLIC_URL"] != "http://127.0.0.1:4" || seen["TOOLSERVER_HTTP_ADDR"] != "127.0.0.1:3" {
		t.Errorf("child addresses = %q, %q", seen["RESET_PUBLIC_URL"], seen["TOOLSERVER_HTTP_ADDR"])
	}
	if _, ok := seen["PATH"]; ok {
		t.Error("PATH leaked into the child")
	}
}
