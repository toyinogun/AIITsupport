// Package report writes, reads, and compares eval run files (spec 0018,
// AC-13). A run file is plain JSON with schema_version 1; the Markdown is
// always rebuilt from it. Files hold fictional data only: any email or link
// outside noord.example in a transcript is replaced before the file is written.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the only run file version this package reads and writes.
const SchemaVersion = 1

const fictionalDomain = "noord.example"

// Header names what a run was made from.
type Header struct {
	Label           string    `json:"label"`
	GitSHA          string    `json:"git_sha"`
	Model           string    `json:"model"`
	TicketSetHash   string    `json:"ticket_set_hash"`
	PromptHash      string    `json:"prompt_hash"`
	ToolsHash       string    `json:"tools_hash"`
	Repeats         int       `json:"repeats"`
	CreatedAt       time.Time `json:"created_at"`
	Status          string    `json:"status"`
	StopReason      string    `json:"stop_reason,omitempty"`
	Partial         bool      `json:"partial"`
	PlannedAttempts int       `json:"planned_attempts"`
	// Redactions counts the tokens replaced by the fictional data scan.
	Redactions int `json:"redactions"`
}

// Result is one finished ticket attempt.
type Result struct {
	Ticket          string   `json:"ticket"`
	Attempt         int      `json:"attempt"`
	Category        string   `json:"category"`
	ExpectedOutcome string   `json:"expected_outcome"`
	ActualOutcome   string   `json:"actual_outcome"`
	RefusalCode     string   `json:"actual_refusal_code,omitempty"`
	ExpectedTools   []string `json:"expected_tools"`
	ActualTools     []string `json:"actual_tools"`
	Passed          bool     `json:"passed"`
	Unsafe          bool     `json:"unsafe_action"`
	Wrong           bool     `json:"wrong_action"`
	FailedChecks    []string `json:"failed_checks"`
	DurationMS      int64    `json:"duration_ms"`
	ApprovalWaitMS  int64    `json:"approval_wait_ms"`
	InputTokens     int      `json:"input_tokens"`
	OutputTokens    int      `json:"output_tokens"`
	CostMicroUSD    int64    `json:"cost_microusd"`
}

// Transcript is the thread and the tool calls of a failed or unsafe attempt.
type Transcript struct {
	Ticket  string   `json:"ticket"`
	Attempt int      `json:"attempt"`
	Posts   []string `json:"posts"`
	Tools   []string `json:"tools"`
}

// File is the whole run file.
type File struct {
	SchemaVersion int                `json:"schema_version"`
	Header        Header             `json:"header"`
	Results       []Result           `json:"results"`
	Metrics       map[string]float64 `json:"metrics"`
	Transcripts   []Transcript       `json:"transcripts"`
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*)`)
	urlRe   = regexp.MustCompile(`https?://[^\s"'<>)]+`)
)

// Redact replaces every email address and link that does not end in
// noord.example with [redacted], and returns the text and how many it replaced.
func Redact(s string) (string, int) {
	n := 0
	s = emailRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasSuffix(strings.ToLower(m), "@"+fictionalDomain) || strings.HasSuffix(strings.ToLower(m), "."+fictionalDomain) {
			return m
		}
		n++
		return "[redacted]"
	})
	s = urlRe.ReplaceAllStringFunc(s, func(m string) string {
		if fictionalURL(m) {
			return m
		}
		n++
		return "[redacted]"
	})
	return s, n
}

func fictionalURL(u string) bool {
	host := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(host, "/?#:"); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	return host == fictionalDomain || strings.HasSuffix(host, "."+fictionalDomain)
}

// Write scans the transcripts, then writes <dir>/<label>.json and
// <dir>/<label>.md. It returns the two paths.
func Write(dir, name string, f File) (jsonPath, mdPath string, err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fmt.Errorf("make the report directory: %w", err)
	}
	f.SchemaVersion = SchemaVersion
	f.Transcripts, f.Header.Redactions = scanTranscripts(f.Transcripts)
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode the run file: %w", err)
	}
	jsonPath, mdPath = filepath.Join(dir, name+".json"), filepath.Join(dir, name+".md")
	if err := os.WriteFile(jsonPath, append(raw, '\n'), 0o600); err != nil {
		return "", "", fmt.Errorf("write the run file: %w", err)
	}
	if err := os.WriteFile(mdPath, []byte(Markdown(f)), 0o600); err != nil {
		return "", "", fmt.Errorf("write the run summary: %w", err)
	}
	return jsonPath, mdPath, nil
}

func scanTranscripts(in []Transcript) ([]Transcript, int) {
	total := 0
	out := make([]Transcript, 0, len(in))
	for _, t := range in {
		posts := make([]string, 0, len(t.Posts))
		for _, p := range t.Posts {
			clean, n := Redact(p)
			total += n
			posts = append(posts, clean)
		}
		t.Posts = posts
		out = append(out, t)
	}
	return out, total
}

// Load reads a run file from a path, or from <dir>/<ref>.json for a bare label.
// It rejects any other schema_version.
func Load(dir, ref string) (File, error) {
	path := ref
	if !strings.ContainsAny(ref, `/\`) && !strings.HasSuffix(ref, ".json") {
		path = filepath.Join(dir, ref+".json")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the path comes from the command line
	if err != nil {
		return File{}, fmt.Errorf("read the run file: %w", err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return File{}, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	if f.SchemaVersion != SchemaVersion {
		return File{}, fmt.Errorf("%s has schema_version %d, this build reads %d", filepath.Base(path), f.SchemaVersion, SchemaVersion)
	}
	return f, nil
}
