package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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
