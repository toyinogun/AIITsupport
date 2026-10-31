package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			err = smoke(t.Context(), &out, checks, 0)
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
	if err := smoke(t.Context(), &out, checks, 0); err == nil {
		t.Fatal("smoke passed a failed check")
	}
	if strings.Contains(out.String(), "hunter2") || out.String() != "fail migrations other\n" {
		t.Errorf("output = %q", out.String())
	}
}

// refused is the error a dial gets while the node's policy controller has not
// yet learned a new pod's address (the cause token is "network").
func refused() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
}

// covers: AC-17 (spec 0015). A Job pod's first connections can be refused for
// a second or two on a cluster whose network policy controller is not yet
// caught up (stock k3s, journal 0080). Connection failures are retried for
// the settle period, counted from the start of the run; a wrong answer is
// never retried.
func TestSmokeSettlesNewPodNetwork(t *testing.T) {
	t.Run("a refused dial that clears passes", func(t *testing.T) {
		calls := 0
		checks := []smokeCheck{{"c", func(context.Context) (string, error) {
			calls++
			if calls < 3 {
				return "", refused()
			}
			return "", nil
		}}}
		var out bytes.Buffer
		if err := smoke(t.Context(), &out, checks, 10*time.Second); err != nil {
			t.Fatalf("smoke: %v (output %q)", err, out.String())
		}
		if calls != 3 || out.String() != "pass c\n" {
			t.Errorf("calls = %d, output %q; want 3 calls and one pass line", calls, out.String())
		}
	})
	t.Run("a refusal that never clears fails as network after the settle period", func(t *testing.T) {
		calls := 0
		checks := []smokeCheck{{"c", func(context.Context) (string, error) { calls++; return "", refused() }}}
		var out bytes.Buffer
		start := time.Now()
		if err := smoke(t.Context(), &out, checks, time.Second); err == nil {
			t.Fatal("smoke passed, want a failure")
		}
		if out.String() != "fail c network\n" || calls < 2 || time.Since(start) < time.Second {
			t.Errorf("output %q after %v with %d calls; want a network failure after about 1s and at least 2 calls", out.String(), time.Since(start), calls)
		}
	})
	t.Run("a wrong answer is not retried", func(t *testing.T) {
		calls := 0
		checks := []smokeCheck{{"c", func(context.Context) (string, error) { calls++; return "", smokeError("no_tools") }}}
		var out bytes.Buffer
		if err := smoke(t.Context(), &out, checks, 10*time.Second); err == nil || calls != 1 {
			t.Errorf("err = %v, calls = %d; want one call and a failure", err, calls)
		}
	})
}
