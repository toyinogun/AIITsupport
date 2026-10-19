package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNewWritesJSONTaggedWithService(t *testing.T) {
	var buf bytes.Buffer
	New(&buf, "info", "agent").Info("hello", "ticket", 42)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	if entry["service"] != "agent" || entry["msg"] != "hello" || entry["level"] != "INFO" {
		t.Errorf("entry = %v, want service=agent msg=hello level=INFO", entry)
	}
	if entry["ticket"] != float64(42) {
		t.Errorf("ticket = %v, want 42", entry["ticket"])
	}
}

func TestNewHonoursLevel(t *testing.T) {
	cases := []struct {
		level     string
		wantDebug bool
		wantInfo  bool
		wantWarn  bool
	}{
		{"debug", true, true, true},
		{"info", false, true, true},
		{"warn", false, false, true},
		{"error", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger := New(&buf, tc.level, "agent")
			logger.Debug("d-line")
			logger.Info("i-line")
			logger.Warn("w-line")

			out := buf.String()
			if got := strings.Contains(out, "d-line"); got != tc.wantDebug {
				t.Errorf("debug written = %v, want %v", got, tc.wantDebug)
			}
			if got := strings.Contains(out, "i-line"); got != tc.wantInfo {
				t.Errorf("info written = %v, want %v", got, tc.wantInfo)
			}
			if got := strings.Contains(out, "w-line"); got != tc.wantWarn {
				t.Errorf("warn written = %v, want %v", got, tc.wantWarn)
			}
		})
	}
}

func TestUnknownLevelFallsBackToInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "verbose", "toolserver")
	logger.Debug("d-line")
	logger.Info("i-line")

	out := buf.String()
	if strings.Contains(out, "d-line") {
		t.Error("an unknown level should not enable debug output")
	}
	if !strings.Contains(out, "i-line") {
		t.Error("an unknown level should still write info output")
	}
}

// covers: AC-1 (spec 0010). Only a listed key outside any group, holding a
// short plain value, prints as is; every other attribute keeps its key and
// prints Redacted. The built in keys and service pass.
func TestNewRedactsOutsideTheAllowList(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "info", "agent").With("tool", "unlock_account", "user", "ZEBRA")
	logger.Info("line",
		"kind", "post",
		"username", "jdevries",
		slog.Group("g", slog.String("kind", "post")), //nolint:sloglint // a grouped attribute must be redacted
		"cause", errors.New("ZEBRA failed"),
		"reason", struct{ A string }{"ZEBRA"},
		"code", "alice@noord.example",
		"outcome", "Jan de Vries",
		"model_calls", 3,
		"latency_ms", 1500*time.Millisecond,
		"read_only", true,
		"channel_id", strings.Repeat("C", 129),
	)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	want := map[string]any{
		"msg": "line", "level": "INFO", "service": "agent", "tool": "unlock_account", "kind": "post",
		"model_calls": float64(3), "latency_ms": float64(1500 * time.Millisecond), "read_only": true,
		"user": Redacted, "username": Redacted, "cause": Redacted, "reason": Redacted,
		"code": Redacted, "outcome": Redacted, "channel_id": Redacted,
	}
	for k, v := range want {
		if entry[k] != v {
			t.Errorf("%s = %v, want %v", k, entry[k], v)
		}
	}
	if entry["g"] != Redacted {
		t.Errorf("group = %v, want %s", entry["g"], Redacted)
	}
	if _, ok := entry["time"]; !ok {
		t.Errorf("time missing: %s", buf.String())
	}
	if strings.Contains(buf.String(), "ZEBRA") || strings.Contains(buf.String(), "noord.example") {
		t.Errorf("a value leaked: %s", buf.String())
	}
}

// covers: AC-2 (spec 0010). AllowedKeys is a sorted copy of the list behind
// Allowed, and error is not on it.
func TestAllowedKeys(t *testing.T) {
	keys := AllowedKeys()
	if !slices.IsSorted(keys) || len(keys) == 0 {
		t.Fatalf("keys = %v", keys)
	}
	for _, k := range keys {
		if !Allowed(k) {
			t.Errorf("AllowedKeys has %q but Allowed says no", k)
		}
	}
	keys[0] = "error"
	if Allowed("error") || Allowed("email") || Allowed("user_id") {
		t.Error("error, email, or user_id is allowed")
	}
}

// covers: AC-1 (spec 0010), the review of 2026-10-19. An attribute keyed
// like a built in (msg, time, level) is redacted, and so is everything after
// WithGroup; the record's own time, level, and message still print.
func TestBuiltinKeysAndGroupsCannotBypass(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, "info", "agent")
	logger.Info("line", "msg", "jan@noord.example", "time", "Jan", "level", "Vries")
	logger.WithGroup("req").Info("grouped", "kind", "post")
	out := buf.String()
	if strings.Contains(out, "noord.example") || strings.Contains(out, "Jan") || strings.Contains(out, "Vries") || strings.Contains(out, `"kind":"post"`) {
		t.Errorf("a value bypassed the allow list:\n%s", out)
	}
	if !strings.Contains(out, `"msg":"line"`) || !strings.Contains(out, `"msg":"grouped"`) || !strings.Contains(out, `"level":"INFO"`) {
		t.Errorf("the record's own fields are missing:\n%s", out)
	}
}
