package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/ldap"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/readiness"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

// covers: AC-16 (spec 0015). The tool server's readiness checks reach each
// client system as its own account: Zammad answers, an LDAP nobody listens
// on is unavailable with a network cause, and Graph is not_configured
// without the MFA settings. The report sits behind the MCP token.
func TestToolLayerReadinessChecks(t *testing.T) {
	desk := zammadtest.New(t)
	env := map[string]string{
		"TOOLSERVER_DATABASE_URL": "postgres://noord:x@127.0.0.1:1/noord",
		"TOOLSERVER_TOKEN":        "test-token-0123456789abcdefghijk",
		"LDAP_URL":                "ldaps://127.0.0.1:1",
		"LDAP_BASE_DN":            "DC=ad,DC=noord,DC=example",
		"LDAP_BIND_DN":            "CN=svc-itsupport,OU=Service Accounts,OU=Noord,DC=ad,DC=noord,DC=example",
		"LDAP_BIND_PASSWORD":      "bind-test",
		"LDAP_CA_CERT":            writeCA(t),
		"ZAMMAD_URL":              desk.URL(),
		"ZAMMAD_TOKEN":            zammadtest.Token,
		"ZAMMAD_DEFAULT_GROUP":    "IT Support",
	}
	cfg, err := config.LoadToolServer(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	zone, err := tools.LoadKBZone()
	if err != nil {
		t.Fatal(err)
	}
	rootCAs, err := ldap.LoadCA(cfg.LDAPCACert)
	if err != nil {
		t.Fatal(err)
	}
	l, err := toolLayer(cfg, toolConfig(cfg, zone), rootCAs, audit.NewPgStore(nil), nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	h := mcpserver.RequireToken(cfg.ToolServerToken, readiness.Handler(l.checks...))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, readiness.Path, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("without the token: %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, readiness.Path, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.ToolServerToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	var r readiness.Report
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	want := map[string]readiness.Result{
		"zammad": {Status: readiness.OK, Cause: "none"},
		"ldap":   {Status: readiness.Unavailable, Cause: "network"},
		"graph":  {Status: readiness.NotConfigured, Cause: "none"},
	}
	if rec.Code != http.StatusServiceUnavailable || len(r.Checks) != len(want) {
		t.Errorf("report = %d %s, want 503 with %d checks", rec.Code, body, len(want))
	}
	for name, w := range want {
		if r.Checks[name] != w {
			t.Errorf("%s = %+v, want %+v", name, r.Checks[name], w)
		}
	}
}
