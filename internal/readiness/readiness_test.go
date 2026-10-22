package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/cause"
)

func ok(context.Context) error { return nil }

// covers: AC-16 (spec 0015). The report is 200 only when every configured
// check is ok; a failed check shows its cause token and never its text; an
// unconfigured one is not_configured and does not fail the report.
func TestHandler(t *testing.T) {
	failing := func(context.Context) error { return errors.New("dial ldaps://secret-host: refused") }
	cases := []struct {
		name   string
		checks []Check
		code   int
		want   map[string]Result
	}{
		{"all ok", []Check{{"database", ok}, {"graph", nil}}, http.StatusOK, map[string]Result{
			"database": {OK, "none"}, "graph": {NotConfigured, "none"},
		}},
		{"one down", []Check{{"database", ok}, {"ldap", failing}}, http.StatusServiceUnavailable, map[string]Result{
			"database": {OK, "none"}, "ldap": {Unavailable, "other"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Handler(tc.checks...).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, Path, nil))
			if rec.Code != tc.code {
				t.Errorf("status = %d, want %d", rec.Code, tc.code)
			}
			if strings.Contains(rec.Body.String(), "secret-host") {
				t.Errorf("the body carries error text: %s", rec.Body)
			}
			var got Report
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body %q: %v", rec.Body, err)
			}
			for name, want := range tc.want {
				if got.Checks[name] != want {
					t.Errorf("%s = %+v, want %+v", name, got.Checks[name], want)
				}
			}
		})
	}
}

// covers: AC-16 (spec 0015). A check that outlives its timeout is
// unavailable with the cause deadline, and the others still report.
func TestRunBoundsEachCheck(t *testing.T) {
	slow := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	r := Run(t.Context(), 50*time.Millisecond, []Check{{"slow", slow}, {"fast", ok}})
	if time.Since(start) > 2*time.Second {
		t.Errorf("Run took %v", time.Since(start))
	}
	if r.Checks["slow"] != (Result{Unavailable, "deadline"}) || r.Checks["fast"] != (Result{OK, "none"}) {
		t.Errorf("report = %+v", r.Checks)
	}
}

// covers: AC-16, AC-17 (spec 0015). Fetch sends the token, returns the
// report, and turns any status but 200 into a StatusError whose cause is
// http_<status>, keeping a 503's report so the failed check shows.
func TestFetch(t *testing.T) {
	var gotAuth string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(Report{Checks: map[string]Result{"database": {OK, "none"}}})
	}))
	t.Cleanup(srv.Close)

	r, err := Fetch(t.Context(), srv.Client(), srv.URL+Path, "tok")
	if err != nil || gotAuth != "Bearer tok" || r.Checks["database"].Status != OK {
		t.Fatalf("Fetch = %+v, %v (auth %q)", r, err, gotAuth)
	}

	status = http.StatusServiceUnavailable
	r, err = Fetch(t.Context(), srv.Client(), srv.URL+Path, "")
	if cause.Of(err) != "http_503" || r.Checks["database"].Status != OK || gotAuth != "" {
		t.Errorf("Fetch on 503 = %+v, %v (auth %q), want the report and http_503", r, err, gotAuth)
	}

	status = http.StatusUnauthorized
	if _, err := Fetch(t.Context(), srv.Client(), srv.URL+Path, "wrong"); cause.Of(err) != "http_401" {
		t.Errorf("Fetch on 401 = %v, want http_401", err)
	}
}

func TestURL(t *testing.T) {
	got, err := URL("http://toolserver.noord-agent.svc:8080/mcp?x=1")
	if err != nil || got != "http://toolserver.noord-agent.svc:8080/readyz" {
		t.Errorf("URL = %q, %v", got, err)
	}
	if _, err := URL("toolserver:8080"); err == nil {
		t.Error("URL accepted an address with no scheme")
	}
}
