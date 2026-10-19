// Package server runs the small HTTP server each binary exposes: health
// checks now, and the MCP endpoint on the tool server from feature #9.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// NewMux returns a mux with GET /healthz registered. Callers add their own
// routes to it.
func NewMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ok") // a failed write means the client is gone; nothing to do
	})
	return mux
}

// Limits are per listener server settings beyond ReadHeaderTimeout. Zero
// keeps the standard library's default (none) for that field.
type Limits struct {
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	MaxHeaderBytes int
}

// Option changes one listener's server.
type Option func(*http.Server)

// WithLimits sets the listener's timeouts and header cap (the reset
// listener, spec 0011 AC-16).
func WithLimits(l Limits) Option {
	return func(s *http.Server) {
		s.ReadTimeout, s.WriteTimeout, s.IdleTimeout, s.MaxHeaderBytes = l.ReadTimeout, l.WriteTimeout, l.IdleTimeout, l.MaxHeaderBytes
	}
}

// Run serves h on ln until ctx is cancelled, then shuts down gracefully.
// It returns nil after a clean shutdown.
func Run(ctx context.Context, ln net.Listener, h http.Handler, logger *slog.Logger, opts ...Option) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(fixedMessage{logger.Handler()}, slog.LevelError),
	}
	for _, opt := range opts {
		opt(srv)
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", ln.Addr().String())
		serveErr <- srv.Serve(ln)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("http server stopped")
	return nil
}

// errorMessage is the one line the standard library's server errors become
// (spec 0010, AC-3). Their text can quote a TLS peer or a panic value, so it
// never reaches the log.
const errorMessage = "http_server_error"

// fixedMessage is the bridge behind http.Server.ErrorLog: it replaces each
// record's message with errorMessage and drops its attributes.
type fixedMessage struct{ inner slog.Handler }

func (h fixedMessage) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h fixedMessage) Handle(ctx context.Context, r slog.Record) error {
	return h.inner.Handle(ctx, slog.NewRecord(r.Time, r.Level, errorMessage, r.PC))
}

func (h fixedMessage) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h fixedMessage) WithGroup(string) slog.Handler      { return h }
