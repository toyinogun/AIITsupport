package report_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toyinogun/AIITsupport/eval/report"
)

func sample(label string, passed bool) report.File {
	return report.File{
		Header: report.Header{Label: label, GitSHA: "abc", Model: "m", Status: "completed", TicketSetHash: "h", Repeats: 1, PlannedAttempts: 2},
		Results: []report.Result{
			{Ticket: "ev-001", Attempt: 1, ExpectedOutcome: "resolve", ActualOutcome: "resolve", Passed: true},
			{Ticket: "ev-002", Attempt: 1, ExpectedOutcome: "escalate", ActualOutcome: "", Passed: passed, FailedChecks: []string{"outcome"}},
		},
		Metrics:     map[string]float64{"tickets": 2, "resolution_rate": 1},
		Transcripts: []report.Transcript{{Ticket: "ev-002", Attempt: 1, Posts: []string{"mail jan@gmail.com or ana@noord.example, see https://evil.example.org/x and https://kb.noord.example/a"}}},
	}
}

// covers: AC-13
func TestRedact(t *testing.T) {
	got, n := report.Redact("jan@gmail.com ana@noord.example https://evil.example.org/x https://kb.noord.example/a")
	want := "[redacted] ana@noord.example [redacted] https://kb.noord.example/a"
	if got != want || n != 2 {
		t.Fatalf("Redact = %q, %d; want %q, 2", got, n, want)
	}
}

// covers: AC-13
func TestWriteLoadRoundTripAndScan(t *testing.T) {
	dir := t.TempDir()
	jsonPath, mdPath, err := report.Write(dir, "v1", sample("v1", false))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(jsonPath) //nolint:gosec // G304: a path the test just wrote
	if strings.Contains(string(raw), "gmail.com") || strings.Contains(string(raw), "evil.example.org") {
		t.Fatal("a real address reached the run file")
	}
	f, err := report.Load(dir, "v1")
	if err != nil || f.Header.Redactions != 2 || len(f.Results) != 2 {
		t.Fatalf("Load = %+v, %v", f.Header, err)
	}
	md, _ := os.ReadFile(mdPath) //nolint:gosec // G304: a path the test just wrote
	if string(md) != report.Markdown(f) {
		t.Fatal("the Markdown on disk is not the Markdown rebuilt from the JSON")
	}
	if !strings.Contains(string(md), "ev-002#1") || !strings.Contains(string(md), "Attempts with no outcome") {
		t.Fatalf("the summary does not list the failed and null attempts:\n%s", md)
	}
}

// covers: AC-13
func TestLoadRejectsOtherSchemaVersions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte(`{"schema_version": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := report.Load(dir, "old"); err == nil {
		t.Fatal("schema_version 2 was accepted")
	}
	if _, err := report.Load(dir, "missing"); err == nil {
		t.Fatal("a missing label was accepted")
	}
}

// covers: AC-13
func TestCompare(t *testing.T) {
	a, b := sample("a", false), sample("b", true)
	b.Metrics["resolution_rate"] = 0.5
	c := report.Compare(a, b)
	if len(c.Changed) != 1 || !strings.HasPrefix(c.Changed[0], "ev-002") {
		t.Fatalf("changed = %v", c.Changed)
	}
	if len(c.Metrics) != 2 || c.Metrics[1].A != 1 || c.Metrics[1].B != 0.5 {
		t.Fatalf("metrics = %+v", c.Metrics)
	}
	b.Header.TicketSetHash = "other"
	b.Header.Partial = true
	if w := report.Compare(a, b).Warnings; len(w) != 2 {
		t.Fatalf("warnings = %v, want a set mismatch and a partial run", w)
	}
	if out := c.Format("a", "b"); !strings.Contains(out, "resolution_rate") {
		t.Fatalf("format:\n%s", out)
	}
}
