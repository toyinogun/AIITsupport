package server

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthzAnswers200(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Errorf("body = %q, want %q", got, "ok\n")
	}
}

func TestHealthzRejectsOtherMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", rec.Code)
	}
}

func TestRunServesUntilContextIsCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	done := make(chan error, 1)
	go func() { done <- Run(ctx, ln, NewMux(), logger) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// covers: SD-2
func TestHealthzIsPlainText(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", got)
	}
}

// Load balancers and some probes send HEAD.
func TestHealthzAnswersHead(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /healthz = %d, want 200", rec.Code)
	}
}

func TestHealthzRejectionListsAllowedMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/healthz", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /healthz = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to include GET", allow)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", rec.Code)
	}
}

// covers: SD-1. `make dev` and the runbook watch for these two log lines.
func TestRunLogsListeningAndStopped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() { done <- Run(ctx, ln, NewMux(), logger) }()
	waitForHealthz(t, ln.Addr().String())
	cancel()
	if err := waitForRun(t, done); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}

	out := logs.String()
	for _, want := range []string{`"msg":"http server listening"`, `"addr":"` + ln.Addr().String() + `"`, `"msg":"http server stopped"`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs do not contain %s:\n%s", want, out)
		}
	}
}

// Shutdown waits for a request that is already running instead of cutting it off.
func TestRunFinishesInFlightRequestOnShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	mux := NewMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	ctx, cancel := context.WithCancel(t.Context())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	done := make(chan error, 1)
	go func() { done <- Run(ctx, ln, mux, logger) }()

	type result struct {
		status int
		body   string
		err    error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/slow")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		got <- result{status: resp.StatusCode, body: string(body), err: err}
	}()

	<-started
	cancel()
	close(release)

	r := <-got
	if r.err != nil || r.status != http.StatusOK || r.body != "done" {
		t.Fatalf("in flight request got status=%d body=%q err=%v, want 200 done", r.status, r.body, r.err)
	}
	if err := waitForRun(t, done); err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

func TestRunReturnsErrorWhenServingFails(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err = Run(t.Context(), ln, NewMux(), logger)
	if err == nil || !strings.Contains(err.Error(), "http server") {
		t.Fatalf("Run on a closed listener = %v, want an http server error", err)
	}
}

func waitForHealthz(t *testing.T, addr string) {
	t.Helper()
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
}

func waitForRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
		return nil
	}
}
