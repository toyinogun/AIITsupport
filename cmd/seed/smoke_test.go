package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/internal/config"
	"github.com/toyinogun/AIITsupport/internal/readiness"
)

const smokeToken = "test-token-0123456789abcdefghijk"

// readyServer answers /readyz with status and, for a 503, one failed check;
// it refuses a call whose token differs from want.
func readyServer(t *testing.T, want string, status *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r2 := readiness.Report{Checks: map[string]readiness.Result{"database": {Status: readiness.OK, Cause: "none"}}}
		if *status != http.StatusOK {
			r2.Checks["ldap"] = readiness.Result{Status: readiness.Unavailable, Cause: "network"}
		}
		w.WriteHeader(*status)
		_ = json.NewEncoder(w).Encode(r2)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// covers: AC-17 (spec 0015). seed smoke passes only when both reports are
// 200, the tool list is not empty, and the database is at the newest
// embedded migration; each line is pass or fail with fixed tokens only.
func TestSmoke(t *testing.T) {
	toolStatus, agentStatus := http.StatusOK, http.StatusOK
	ts := readyServer(t, "Bearer "+smokeToken, &toolStatus)
	ag := readyServer(t, "", &agentStatus)
	cfg := config.Smoke{ToolServerToken: smokeToken, ToolServerURL: ts.URL + "/mcp", AgentURL: ag.URL}

	cases := []struct {
		name       string
		tool, agt  int
		tools      int
		toolsErr   error
		current    int64
		wantErr    bool
		wantOutput []string
	}{
		{"all pass", 200, 200, 3, nil, 22, false, []string{
			"pass toolserver_readyz", "pass agent_readyz", "pass tools_list", "pass migrations",
		}},
		{"tool server not ready", 503, 200, 3, nil, 22, true, []string{
			"fail toolserver_readyz http_503 ldap=network", "pass agent_readyz",
		}},
		{"no tools and an old database", 200, 200, 0, nil, 21, true, []string{
			"fail tools_list no_tools", "fail migrations version_mismatch database=21 embedded=22",
		}},
		{"MCP down", 200, 200, 0, context.DeadlineExceeded, 22, true, []string{"fail tools_list deadline"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toolStatus, agentStatus = tc.tool, tc.agt
			checks, err := smokeChecks(cfg, smokeDeps{
				http:      &http.Client{},
				listTools: func(context.Context) (int, error) { return tc.tools, tc.toolsErr },
				current:   func(context.Context) (int64, error) { return tc.current, nil },
				latest:    func() (int64, error) { return 22, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = smoke(t.Context(), &out, checks)
			if (err != nil) != tc.wantErr {
				t.Errorf("smoke = %v, want an error %v", err, tc.wantErr)
			}
			for _, line := range tc.wantOutput {
				if !strings.Contains(out.String(), line+"\n") {
					t.Errorf("output lacks %q:\n%s", line, out.String())
				}
			}
		})
	}
}

// covers: AC-17 (spec 0015). A database failure prints its cause token,
// never the error text.
func TestSmokeHidesErrorText(t *testing.T) {
	checks := []smokeCheck{{"migrations", func(context.Context) (string, error) {
		return "", errors.New("connect postgres://noord_owner:hunter2@db")
	}}}
	var out bytes.Buffer
	if err := smoke(t.Context(), &out, checks); err == nil {
		t.Fatal("smoke passed a failed check")
	}
	if strings.Contains(out.String(), "hunter2") || out.String() != "fail migrations other\n" {
		t.Errorf("output = %q", out.String())
	}
}
