package score_test

import (
	"testing"

	"github.com/toyinogun/AIITsupport/eval/score"
)

// resolveRun returns n passed resolve attempts taking 1, 2, ... n milliseconds.
func resolveRun(n int) []score.Attempt {
	out := make([]score.Attempt, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, attempt(score.OutcomeResolve, score.OutcomeResolve, true, int64(i), 0, 1, false))
	}
	return out
}

// covers: AC-11 (spec 0018). Percentiles use the nearest rank method: the
// value at rank ceil(p/100 * n), counted from 1, on the sorted times. The run
// sizes are the ones a real run produces (one ticket, 24 resolve tickets,
// times 1, 3, and 5 repeats).
func TestNearestRankOnRealRunSizes(t *testing.T) {
	tests := []struct {
		n        int
		p50, p95 float64
	}{
		{n: 1, p50: 1, p95: 1},
		{n: 20, p50: 10, p95: 19},
		{n: 24, p50: 12, p95: 23},
		{n: 72, p50: 36, p95: 69},
		{n: 100, p50: 50, p95: 95},
		{n: 120, p50: 60, p95: 114},
	}
	for _, tt := range tests {
		m := score.Summarize(resolveRun(tt.n))
		if got := m["time_to_resolve_p50_ms"]; got != tt.p50 {
			t.Errorf("n=%d: p50 = %v, want %v", tt.n, got, tt.p50)
		}
		if got := m["time_to_resolve_p95_ms"]; got != tt.p95 {
			t.Errorf("n=%d: p95 = %v, want %v", tt.n, got, tt.p95)
		}
	}
}

// covers: AC-11 (spec 0018). The order the attempts finish in does not move a
// percentile, and a resolve attempt that failed never counts toward the time.
func TestTimeToResolveIgnoresOrderAndFailures(t *testing.T) {
	attempts := []score.Attempt{
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 300, 0, 1, false),
		attempt(score.OutcomeResolve, score.OutcomeEscalate, false, 99999, 0, 1, false),
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 100, 0, 1, false),
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 200, 0, 1, false),
	}
	m := score.Summarize(attempts)
	if m["time_to_resolve_p50_ms"] != 200 || m["time_to_resolve_p95_ms"] != 300 {
		t.Errorf("p50, p95 = %v, %v, want 200, 300 (the failed 99999 ms attempt must not count)",
			m["time_to_resolve_p50_ms"], m["time_to_resolve_p95_ms"])
	}
}

// covers: AC-4, AC-11 (spec 0018). A ticket that expects open counts in
// tickets and passed, but in no expected count and no rate.
func TestOpenTicketsCountInPassedButInNoRate(t *testing.T) {
	attempts := []score.Attempt{
		attempt(score.OutcomeOpen, score.OutcomeOpen, true, 2000, 0, 5, false),
		attempt(score.OutcomeOpen, score.OutcomeResolve, false, 2000, 0, 5, false),
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 1000, 0, 5, false),
	}
	m := score.Summarize(attempts)
	want := map[string]float64{
		"tickets": 3, "passed": 2,
		"resolve_expected": 1, "resolve_passed": 1, "resolution_rate": 1,
		"escalate_expected": 0, "escalation_rate": 0,
		"refuse_expected": 0, "refusal_rate": 0,
		"time_to_resolve_p50_ms": 1000,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["open_expected"]; ok {
		t.Error("the summary has an open_expected metric; open tickets belong to no rate")
	}
}

// covers: AC-11 (spec 0018). The approval wait median is over attempts whose
// ticket has an approval, including a wait of 0, and over no other attempt.
func TestApprovalWaitOnlyOverApprovalTickets(t *testing.T) {
	attempts := []score.Attempt{
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 1000, 0, 1, true),
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 1000, 900, 1, true),
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 1000, 0, 1, true),
		attempt(score.OutcomeResolve, score.OutcomeResolve, true, 1000, 5000, 1, false),
	}
	if got := score.Summarize(attempts)["approval_wait_p50_ms"]; got != 0 {
		t.Errorf("approval_wait_p50_ms = %v, want 0 (median of 0, 0, 900; the 5000 ms wait has no approval)", got)
	}
}
