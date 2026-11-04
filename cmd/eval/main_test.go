package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/eval/report"
)

// covers: AC-18
func TestRepeatFlag(t *testing.T) {
	f, err := parseRunFlags(nil)
	if err != nil || f.repeat != 1 {
		t.Fatalf("default = %+v, %v", f, err)
	}
	if f, err = parseRunFlags([]string{"--repeat", "3"}); err != nil || f.repeat != 3 {
		t.Fatalf("--repeat 3 = %+v, %v", f, err)
	}
	for _, bad := range []string{"0", "6", "-1"} {
		if _, err := parseRunFlags([]string{"--repeat", bad}); err == nil {
			t.Errorf("--repeat %s was accepted", bad)
		}
	}
}

// covers: AC-15
func TestFreezeNeedsALabel(t *testing.T) {
	if _, err := parseRunFlags([]string{"--freeze"}); err == nil {
		t.Fatal("--freeze without --label was accepted")
	}
	if f, err := parseRunFlags([]string{"--freeze", "--label", "v1"}); err != nil || !f.freeze {
		t.Fatalf("--freeze --label v1 = %+v, %v", f, err)
	}
}

// covers: AC-1
func TestLabelCollision(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "v1.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkLabel(runFlags{label: "v1", out: dir}); err == nil {
		t.Error("a label with a report file was accepted")
	}
	if err := checkLabel(runFlags{label: "v1", out: dir, force: true}); err != nil {
		t.Errorf("--force did not allow it: %v", err)
	}
	if err := checkLabel(runFlags{label: "../evil", out: dir}); err == nil {
		t.Error("a label with a path in it was accepted")
	}
	if err := checkLabel(runFlags{label: "v2", out: dir}); err != nil {
		t.Errorf("a new label was refused: %v", err)
	}
}

// covers: AC-13
func TestReportAndCompareCommands(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b"} {
		f := report.File{
			Header:  report.Header{Label: name, Status: "completed", TicketSetHash: "h"},
			Metrics: map[string]float64{"tickets": 1},
		}
		if _, _, err := report.Write(dir, name, f); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := reportCommand([]string{filepath.Join(dir, "a.json")}, &buf); err != nil || !strings.Contains(buf.String(), "Eval run a") {
		t.Fatalf("report = %q, %v", buf.String(), err)
	}
	buf.Reset()
	if err := compareCommand([]string{filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")}, &buf); err != nil || !strings.Contains(buf.String(), "tickets") {
		t.Fatalf("compare = %q, %v", buf.String(), err)
	}
	if err := compareCommand([]string{"only-one"}, &buf); err == nil {
		t.Error("compare with one argument was accepted")
	}
}

// covers: AC-12
func TestExitCodes(t *testing.T) {
	failed := fmt.Errorf("%w: %w", errRunFailed, errors.New("boom"))
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"clean run", nil, 0},
		{"unsafe or wrong action", fmt.Errorf("%w (unsafe 1, wrong 0)", errUnsafe), 3},
		{"failed run", failed, 1},
		{"failed and unsafe: one wins", errors.Join(failed, errUnsafe), 1},
		{"usage error", errUsage, 1},
	}
	for _, tc := range tests {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("%s: exit code %d, want %d", tc.name, got, tc.want)
		}
	}
}
