package resetlink

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeCleaner records each run and answers with n and err. When stop is
// set, the first run cancels it, so Cleanup returns after one pass.
type fakeCleaner struct {
	mu        sync.Mutex
	n         int64
	err       error
	stop      context.CancelFunc
	days      []int
	deadlines []time.Duration
}

func (f *fakeCleaner) DeleteExpired(ctx context.Context, retentionDays int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.days = append(f.days, retentionDays)
	if d, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, time.Until(d))
	}
	if f.stop != nil {
		f.stop()
	}
	return f.n, f.err
}

func newLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

// covers: spec 0011 AC-18. Cleanup runs once at start with the retention
// days and a deadline of at most 5 minutes, logs the row count, and returns
// when its context ends.
func TestCleanupRunsAtStartAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &fakeCleaner{n: 3, stop: cancel}
	logger, logs := newLogger()

	done := make(chan struct{})
	go func() { Cleanup(ctx, c, 30, logger); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Cleanup did not return after its context ended")
	}

	if len(c.days) != 1 || c.days[0] != 30 {
		t.Errorf("runs = %v, want one run with 30 days", c.days)
	}
	if len(c.deadlines) != 1 || c.deadlines[0] > cleanupTimeout || c.deadlines[0] <= 0 {
		t.Errorf("deadline = %v, want at most %s", c.deadlines, cleanupTimeout)
	}
	if !strings.Contains(logs.String(), `"msg":"reset_links_cleaned"`) || !strings.Contains(logs.String(), `"rows":3`) {
		t.Errorf("logs = %s", logs.String())
	}
}

// covers: spec 0011 AC-18, AC-19. A failed run logs reset_links_cleanup_failed
// with a cause token, never the error text, and does not stop anything.
func TestCleanupFailureIsLogged(t *testing.T) {
	c := &fakeCleaner{err: &pgconn.PgError{Code: "57P01", Message: "terminating connection for eva.peters"}}
	logger, logs := newLogger()

	cleanOnce(t.Context(), c, 30, logger)

	out := logs.String()
	if !strings.Contains(out, `"msg":"reset_links_cleanup_failed"`) || !strings.Contains(out, `"cause":"postgres_57P01"`) {
		t.Errorf("logs = %s", out)
	}
	if strings.Contains(out, "eva.peters") || strings.Contains(out, "terminating") {
		t.Errorf("the log holds the error text: %s", out)
	}
	if strings.Contains(out, "reset_links_cleaned") {
		t.Error("a failed run also logged success")
	}
}

// covers: spec 0011 AC-18. A run cut short by shutdown is not a failure.
func TestCleanupQuietOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := &fakeCleaner{err: errors.New("context canceled")}
	logger, logs := newLogger()

	cleanOnce(ctx, c, 30, logger)

	if logs.Len() != 0 {
		t.Errorf("logs = %s, want nothing on shutdown", logs.String())
	}
}
