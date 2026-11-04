package tools

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// covers: spec 0016 AC-8. Mode is what is actually enforced: only a file
// holding exactly read_write is read_write; anything else, and an unreadable
// file (with its error), is read_only; with no file READ_ONLY decides.
func TestKillSwitchMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mode")
	sw := NewKillSwitch(path, true)

	for content, want := range map[string]string{
		"read_write":       ModeReadWrite,
		"  read_write\n":   ModeReadWrite,
		"read_only":        ModeReadOnly,
		"":                 ModeReadOnly,
		"read_write maybe": ModeReadOnly,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := sw.Mode(); got != want || err != nil {
			t.Errorf("file %q: Mode = %q, %v; want %q", content, got, err, want)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := sw.Mode(); got != ModeReadOnly || err == nil {
		t.Errorf("missing file: Mode = %q, %v; want read_only and an error", got, err)
	}
	if got, err := NewKillSwitch("", true).Mode(); got != ModeReadOnly || err != nil {
		t.Errorf("READ_ONLY=true: %q, %v", got, err)
	}
	if got, err := NewKillSwitch("", false).Mode(); got != ModeReadWrite || err != nil {
		t.Errorf("READ_ONLY=false: %q, %v", got, err)
	}
}

type fakeSwitch struct {
	mu   sync.Mutex
	mode string
	err  error
}

func (s *fakeSwitch) AccountChangesAllowed() bool { return s.mode == ModeReadWrite }

func (s *fakeSwitch) Mode() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode, s.err
}

func (s *fakeSwitch) set(mode string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode, s.err = mode, err
}

type fakeState struct {
	mu    sync.Mutex
	modes []string
	err   error
}

func (s *fakeState) SyncKillSwitchState(_ context.Context, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modes = append(s.modes, mode)
	return s.err
}

func (s *fakeState) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.modes...)
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func runSync(t *testing.T, sw KillSwitch, st KillSwitchState, logs *logBuf) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		SyncKillSwitch(ctx, sw, st, 5*time.Millisecond, slog.New(slog.NewJSONHandler(logs, nil)))
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("SyncKillSwitch did not stop when its context ended")
		}
	}
}

// covers: spec 0016 AC-8. The loop records the mode at start and on every
// tick, and follows a flip.
func TestSyncKillSwitchRecordsAtStartAndEveryTick(t *testing.T) {
	sw, st, logs := &fakeSwitch{mode: ModeReadOnly}, &fakeState{}, &logBuf{}
	stop := runSync(t, sw, st, logs)
	defer stop()
	waitFor(t, "three ticks", func() bool { return len(st.seen()) >= 3 })
	sw.set(ModeReadWrite, nil)
	waitFor(t, "the flip", func() bool { s := st.seen(); return s[len(s)-1] == ModeReadWrite })
	if first := st.seen()[0]; first != ModeReadOnly {
		t.Errorf("first sync = %q, want read_only at start", first)
	}
}

// covers: spec 0016 AC-8. An unreadable switch records read_only (what is
// enforced) and logs kill_switch_sync_failed with a cause and nothing else.
func TestSyncKillSwitchUnreadableRecordsReadOnly(t *testing.T) {
	sw, st, logs := &fakeSwitch{mode: ModeReadOnly, err: errors.New("open /etc/noord/secret-path: permission denied")}, &fakeState{}, &logBuf{}
	stop := runSync(t, sw, st, logs)
	waitFor(t, "a sync", func() bool { return len(st.seen()) >= 1 })
	stop()
	if st.seen()[0] != ModeReadOnly {
		t.Errorf("recorded %v, want read_only", st.seen())
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"kill_switch_sync_failed"`) || !strings.Contains(out, `"cause"`) {
		t.Errorf("logs lack kill_switch_sync_failed with a cause:\n%s", out)
	}
	if strings.Contains(out, "secret-path") || strings.Contains(out, "permission denied") {
		t.Errorf("the log holds error text:\n%s", out)
	}
}

// covers: spec 0016 AC-8. A failed write is logged and the loop keeps going.
func TestSyncKillSwitchWriteFailureKeepsGoing(t *testing.T) {
	sw, st, logs := &fakeSwitch{mode: ModeReadWrite}, &fakeState{err: errors.New("db down: password=hunter2")}, &logBuf{}
	stop := runSync(t, sw, st, logs)
	waitFor(t, "two attempts", func() bool { return len(st.seen()) >= 2 })
	stop()
	if out := logs.String(); !strings.Contains(out, `"msg":"kill_switch_sync_failed"`) || strings.Contains(out, "hunter2") {
		t.Errorf("logs:\n%s", out)
	}
}
