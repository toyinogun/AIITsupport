package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/toyinogun/AIITsupport/internal/readiness"
	"github.com/toyinogun/AIITsupport/internal/slack"
)

func dbOK(context.Context) error { return nil }

// covers: AC-16 (spec 0015). The agent's report checks the database, calls
// the tool server's own report with the token, and checks the Slack socket
// only when Slack is configured.
func TestReadyHandler(t *testing.T) {
	const token = "test-token-0123456789abcdefghijk"
	toolStatus := http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != readiness.Path || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(toolStatus)
	}))
	t.Cleanup(ts.Close)
	down := slack.New("xoxb-test", "xapp-test", nil, slog.New(slog.DiscardHandler), time.Hour) // never run, so never connected

	cases := []struct {
		name     string
		status   int
		listener *slack.Listener
		code     int
		want     map[string]readiness.Result
	}{
		{"ready without slack", http.StatusOK, nil, http.StatusOK, map[string]readiness.Result{
			"database":   {Status: readiness.OK, Cause: "none"},
			"toolserver": {Status: readiness.OK, Cause: "none"},
			"slack":      {Status: readiness.NotConfigured, Cause: "none"},
		}},
		{"tool server not ready", http.StatusServiceUnavailable, nil, http.StatusServiceUnavailable, map[string]readiness.Result{
			"toolserver": {Status: readiness.Unavailable, Cause: "http_503"},
		}},
		{"socket down", http.StatusOK, down, http.StatusServiceUnavailable, map[string]readiness.Result{
			"slack": {Status: readiness.Unavailable, Cause: "slack_not_connected"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toolStatus = tc.status
			rec := httptest.NewRecorder()
			readyHandler(ts.URL+readiness.Path, token, dbOK, tc.listener).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, readiness.Path, nil))
			var r readiness.Report
			if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
				t.Fatalf("body %q: %v", rec.Body, err)
			}
			if rec.Code != tc.code {
				t.Errorf("status = %d, want %d (%s)", rec.Code, tc.code, rec.Body)
			}
			for name, w := range tc.want {
				if r.Checks[name] != w {
					t.Errorf("%s = %+v, want %+v", name, r.Checks[name], w)
				}
			}
		})
	}
}
