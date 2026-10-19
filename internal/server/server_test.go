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
	"sync"
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
	logger := slog.New(slog.DiscardHandler)

	done := make(chan error, 1)
	go func() { done <- Run(ctx, ln, NewMux(), logger) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
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
	logger := slog.New(slog.DiscardHandler)

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
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	logger := slog.New(slog.DiscardHandler)

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
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
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

// covers: AC-3 (spec 0010). A panic in a handler, which the standard library
// logs with its value and a stack, becomes one fixed http_server_error line.
func TestServerErrorsLogAFixedLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var logs syncWriter
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	mux := NewMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("ZEBRA alice@noord.example") })
	done := make(chan error, 1)
	go func() { done <- Run(ctx, ln, mux, logger) }()

	if resp, err := http.Get("http://" + ln.Addr().String() + "/boom"); err == nil {
		_ = resp.Body.Close()
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, `"level":"ERROR","msg":"http_server_error"}`) {
		t.Errorf("logs lack a bare http_server_error line:\n%s", out)
	}
	if strings.Contains(out, "ZEBRA") || strings.Contains(out, "noord.example") || strings.Contains(out, "goroutine") {
		t.Errorf("logs carry the standard library's text:\n%s", out)
	}
}

// covers: AC-3 (spec 0010). The bridge drops the text of a TLS handshake
// error line, the other thing http.Server writes to ErrorLog.
func TestErrorLogBridgeDropsText(t *testing.T) {
	var buf bytes.Buffer
	l := slog.NewLogLogger(fixedMessage{slog.NewJSONHandler(&buf, nil)}, slog.LevelError)
	l.Printf("http: TLS handshake error from %s: %s", "10.0.0.7:5555", "remote error: tls: bad certificate")
	if got := buf.String(); strings.Contains(got, "10.0.0.7") || strings.Contains(got, "tls") || !strings.Contains(got, `"msg":"http_server_error"`) {
		t.Errorf("line = %s", got)
	}
}

type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// covers: spec 0011 AC-16. WithLimits sets the reset listener's timeouts
// and header cap; without it the server keeps only ReadHeaderTimeout.
func TestWithLimits(t *testing.T) {
	var srv http.Server
	WithLimits(Limits{ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 8 << 10})(&srv)
	if srv.ReadTimeout != 10*time.Second || srv.WriteTimeout != 20*time.Second || srv.IdleTimeout != time.Minute || srv.MaxHeaderBytes != 8<<10 {
		t.Errorf("limits = %s %s %s %d", srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
}
