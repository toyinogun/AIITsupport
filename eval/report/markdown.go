package report

import (
	"fmt"
	"slices"
	"strings"
)

// headline is the order the Markdown lists the metrics in.
var headline = []string{
	"tickets", "passed", "resolution_rate", "escalation_rate", "refusal_rate",
	"unsafe_tickets", "wrong_tickets", "time_to_resolve_p50_ms", "time_to_resolve_p95_ms",
	"approval_wait_p50_ms", "cost_mean_microusd", "cost_max_microusd", "cost_total_microusd",
}

// Markdown renders the run file as a summary a person can read and commit.
func Markdown(f File) string {
	var b strings.Builder
	h := f.Header
	fmt.Fprintf(&b, "# Eval run %s\n\n", h.Label)
	fmt.Fprintf(&b, "- commit `%s`, model `%s`, status %s", h.GitSHA, h.Model, h.Status)
	if h.StopReason != "" {
		fmt.Fprintf(&b, " (stopped: %s)", h.StopReason)
	}
	if h.Partial {
		b.WriteString(", partial")
	}
	fmt.Fprintf(&b, "\n- ticket set `%s`, prompt `%s`, tools `%s`\n", short(h.TicketSetHash), short(h.PromptHash), short(h.ToolsHash))
	fmt.Fprintf(&b, "- attempts per ticket %d, planned %d, finished %d\n", h.Repeats, h.PlannedAttempts, len(f.Results))
	if h.Redactions > 0 {
		fmt.Fprintf(&b, "- %d token(s) in the transcripts were replaced with [redacted]\n", h.Redactions)
	}
	b.WriteString("\n## Metrics\n\n| metric | value |\n|---|---|\n")
	for _, k := range headline {
		if v, ok := f.Metrics[k]; ok {
			fmt.Fprintf(&b, "| %s | %g |\n", k, v)
		}
	}
	writeList(&b, "Failed or unsafe attempts", failedLines(f.Results))
	writeList(&b, "Attempts with no outcome", nullLines(f.Results))
	return b.String()
}

func failedLines(rs []Result) []string {
	var out []string
	for _, r := range rs {
		if r.Passed {
			continue
		}
		flags := ""
		if r.Unsafe {
			flags += " UNSAFE"
		}
		if r.Wrong {
			flags += " WRONG"
		}
		out = append(out, fmt.Sprintf("%s#%d expected %s, got %s%s: %s",
			r.Ticket, r.Attempt, r.ExpectedOutcome, orNone(r.ActualOutcome), flags, strings.Join(r.FailedChecks, ", ")))
	}
	return out
}

func nullLines(rs []Result) []string {
	var out []string
	for _, r := range rs {
		if r.ActualOutcome == "" {
			out = append(out, fmt.Sprintf("%s#%d (expected %s)", r.Ticket, r.Attempt, r.ExpectedOutcome))
		}
	}
	return out
}

func writeList(b *strings.Builder, title string, lines []string) {
	fmt.Fprintf(b, "\n## %s\n\n", title)
	if len(lines) == 0 {
		b.WriteString("None.\n")
		return
	}
	for _, l := range lines {
		fmt.Fprintf(b, "- %s\n", l)
	}
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Comparison is two runs side by side.
type Comparison struct {
	Warnings []string
	Metrics  []MetricChange
	// Changed lists tickets whose count of passed attempts differs.
	Changed []string
}

// MetricChange is one metric in both runs.
type MetricChange struct {
	Name string
	A, B float64
}

// Compare lines up the metrics of two runs and lists the tickets whose passed
// attempts differ.
func Compare(a, b File) Comparison {
	var c Comparison
	if a.Header.TicketSetHash != b.Header.TicketSetHash {
		c.Warnings = append(c.Warnings, "the two runs used different ticket sets")
	}
	for _, f := range []File{a, b} {
		if f.Header.Partial || f.Header.Status != "completed" {
			c.Warnings = append(c.Warnings, fmt.Sprintf("run %s is partial or failed; compare with care", f.Header.Label))
		}
	}
	for _, k := range headline {
		va, oka := a.Metrics[k]
		vb, okb := b.Metrics[k]
		if oka && okb {
			c.Metrics = append(c.Metrics, MetricChange{k, va, vb})
		}
	}
	pa, pb := passedByTicket(a), passedByTicket(b)
	for t := range pa {
		if pa[t] != pb[t] {
			c.Changed = append(c.Changed, fmt.Sprintf("%s: %d passed, now %d", t, pa[t], pb[t]))
		}
	}
	for t := range pb {
		if _, seen := pa[t]; !seen && pb[t] != 0 {
			c.Changed = append(c.Changed, fmt.Sprintf("%s: 0 passed, now %d", t, pb[t]))
		}
	}
	slices.Sort(c.Changed)
	return c
}

func passedByTicket(f File) map[string]int {
	out := map[string]int{}
	for _, r := range f.Results {
		if r.Passed {
			out[r.Ticket]++
		} else if _, ok := out[r.Ticket]; !ok {
			out[r.Ticket] = 0
		}
	}
	return out
}

// Format renders the comparison for a terminal.
func (c Comparison) Format(labelA, labelB string) string {
	var b strings.Builder
	for _, w := range c.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	fmt.Fprintf(&b, "%-26s %14s %14s %14s\n", "metric", labelA, labelB, "change")
	for _, m := range c.Metrics {
		fmt.Fprintf(&b, "%-26s %14g %14g %+14g\n", m.Name, m.A, m.B, m.B-m.A)
	}
	if len(c.Changed) > 0 {
		b.WriteString("\ntickets that changed:\n")
		for _, l := range c.Changed {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	return b.String()
}
