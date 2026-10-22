package install

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// covers: AC-15 (spec 0015). With reset on, the reset page's URL is the
// per cluster tailnet host, svc-pwreset's password is generated once and
// reaches both Samba and the tool server, and TAILNET and the DN are
// required; with reset off none of it is sealed.
func TestBuildReset(t *testing.T) {
	tbl := realTable(t)
	comps := components(true)
	on := Conditions{Demo: true, Reset: true}
	env := tracerEnv(t)
	env.Set("TAILNET", "example.ts.net")
	env.Set("RESET_LDAP_BIND_DN", "CN=svc-pwreset,DC=x")

	required := tbl.Required(comps, on)
	for _, want := range []string{"TAILNET", "RESET_LDAP_BIND_DN"} {
		if !slices.Contains(required, want) {
			t.Errorf("Required(reset on) misses %s: %v", want, required)
		}
	}
	if slices.Contains(required, "RESET_LDAP_BIND_PASSWORD") {
		t.Error("Required(demo and reset on) holds RESET_LDAP_BIND_PASSWORD, which demo generates")
	}
	if !slices.Contains(tbl.Required([]string{componentAgent}, Conditions{Reset: true}), "RESET_LDAP_BIND_PASSWORD") {
		t.Error("Required(demo off, reset on) misses RESET_LDAP_BIND_PASSWORD: your directory's account")
	}

	filled, err := tbl.Generate(t.Context(), env, fakeLive{}, comps, on)
	must(t, err)
	if !slices.Contains(filled, "RESET_LDAP_BIND_PASSWORD") {
		t.Errorf("filled = %v, want RESET_LDAP_BIND_PASSWORD", filled)
	}
	src := Sources{Files: map[string]string{"server.pem": "CERT", "server.key": "KEY"}, Hosts: map[string]string{hostReset: "noord-k3sprox-reset"}}
	plain, err := tbl.Build(env, src, comps, on)
	must(t, err)
	ts := plain["noord-toolserver"]
	if got, want := ts["RESET_PUBLIC_URL"], "https://noord-k3sprox-reset.example.ts.net"; got != want {
		t.Errorf("RESET_PUBLIC_URL = %q, want %q", got, want)
	}
	pw := env.Get("RESET_LDAP_BIND_PASSWORD")
	if ts["RESET_LDAP_BIND_PASSWORD"] != pw || plain["noord-samba"]["SAMBA_PWRESET_PASSWORD"] != pw {
		t.Error("svc-pwreset's password differs between the tool server and Samba")
	}

	env.Set("TAILNET", "")
	if _, err := tbl.Build(env, src, comps, on); err == nil || !strings.Contains(err.Error(), "TAILNET") {
		t.Errorf("reset on without TAILNET: err = %v, want TAILNET named", err)
	}

	off, err := tbl.Build(env, src, comps, Conditions{Demo: true})
	must(t, err)
	for _, key := range []string{"RESET_PUBLIC_URL", "RESET_LDAP_BIND_DN", "RESET_LDAP_BIND_PASSWORD"} {
		if _, ok := off["noord-toolserver"][key]; ok {
			t.Errorf("reset off still sealed %s", key)
		}
	}
}

// covers: AC-15 (spec 0015). The reset host is per cluster unless the
// folder names one, and reset is on unless the folder turns it off.
func TestValuesReset(t *testing.T) {
	var v Values
	v.Cluster.Name = "k3sprox"
	if v.ResetHost() != "noord-k3sprox-reset" || !v.ResetEnabled() {
		t.Errorf("defaults: host %q, enabled %t", v.ResetHost(), v.ResetEnabled())
	}
	off := false
	v.Reset.Enabled, v.Reset.TailscaleHost = &off, "pwreset"
	if v.ResetHost() != "pwreset" || v.Conditions().Reset {
		t.Errorf("set: host %q, conditions %+v", v.ResetHost(), v.Conditions())
	}
}

// covers: AC-24 (spec 0015). The certificate file GRAPH_CERT_FILE names is
// sealed as the tool server's mounted key, a relative path is under the
// clone, and an unreadable file is named by its setting, never its content.
func TestBuildGraphCert(t *testing.T) {
	tbl := realTable(t)
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "graph.pem"), []byte("PEM CONTENT"), 0o600))
	env := tracerEnv(t)
	must(t, firstErr(tbl.Generate(t.Context(), env, fakeLive{}, []string{componentAgent}, Conditions{})))

	plain, err := tbl.Build(env, Sources{Root: root}, []string{componentAgent}, Conditions{})
	must(t, err)
	if _, ok := plain["noord-toolserver"]["graph-client.pem"]; ok {
		t.Error("no GRAPH_CERT_FILE, yet a certificate was sealed")
	}

	env.Set("GRAPH_CERT_FILE", "graph.pem")
	plain, err = tbl.Build(env, Sources{Root: root}, []string{componentAgent}, Conditions{})
	must(t, err)
	if plain["noord-toolserver"]["graph-client.pem"] != "PEM CONTENT" {
		t.Error("the certificate file's content was not sealed for the tool server")
	}

	env.Set("GRAPH_CERT_FILE", filepath.Join(root, "absent.pem"))
	if _, err := tbl.Build(env, Sources{Root: root}, []string{componentAgent}, Conditions{}); err == nil || !strings.Contains(err.Error(), "GRAPH_CERT_FILE") {
		t.Errorf("a missing certificate file: err = %v, want GRAPH_CERT_FILE named", err)
	}
}

// covers: AC-24 (spec 0015). Preflight refuses a certificate and a client
// secret at once, and a certificate file it cannot read.
func TestPreflightGraphCert(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "graph.pem"), []byte("PEM"), 0o600))
	s := &state{Options: Options{Root: root}, env: tracerEnv(t)}
	if err := s.checkGraphCert(); err != nil {
		t.Errorf("no certificate: %v", err)
	}
	s.env.Set("GRAPH_CERT_FILE", "graph.pem")
	if err := s.checkGraphCert(); err != nil {
		t.Errorf("a readable certificate: %v", err)
	}
	s.env.Set("GRAPH_CLIENT_SECRET", "s3cret-value")
	err := s.checkGraphCert()
	if err == nil || !strings.Contains(err.Error(), "GRAPH_CLIENT_SECRET") || strings.Contains(err.Error(), "s3cret-value") {
		t.Errorf("both set: err = %v, want both named and no value", err)
	}
	s.env.Set("GRAPH_CLIENT_SECRET", "")
	s.env.Set("GRAPH_CERT_FILE", "absent.pem")
	if err := s.checkGraphCert(); err == nil {
		t.Error("an unreadable certificate passed preflight")
	}
}

// covers: AC-7 (spec 0015). The table refuses a tailnet URL without a host
// and a file key with another source.
func TestTableCheckRefusesBadSources(t *testing.T) {
	for name, k := range map[string]Key{
		"tailnet without host": {Derive: deriveTailnetURL},
		"tailnet with role":    {Derive: deriveTailnetURL, Host: "reset", Role: "r"},
		"fromFile with from":   {FromFile: "GRAPH_CERT_FILE", From: "X"},
	} {
		if err := k.check(); err == nil {
			t.Errorf("%s: check accepted it", name)
		}
	}
}

func firstErr(_ []string, err error) error { return err }
