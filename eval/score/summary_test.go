package score_test

import (
	"testing"

	"github.com/toyinogun/AIITsupport/eval/score"
)

func attempt(expected, actual string, passed bool, ms, wait, cost int64, approval bool) score.Attempt {
	return score.Attempt{
		Expected: expected, Result: score.Result{Outcome: actual, Passed: passed},
		DurationMS: ms, ApprovalWaitMS: wait, CostMicroUSD: cost, HasApproval: approval,
	}
}

func TestSummarize(t *testing.T) {
	attempts := []score.Attempt{
		attempt("resolve", "resolve", true, 1000, 0, 100, false),
		attempt("resolve", "resolve", true, 3000, 500, 300, true),
		attempt("resolve", "escalate", false, 9000, 0, 200, false),
		attempt("escalate", "escalate", true, 500, 0, 50, false),
		attempt("refuse", "refuse", true, 400, 0, 10, false),
		attempt("refuse", "resolve", false, 400, 0, 10, false),
	}
	attempts[5].Result.Unsafe = true
	attempts[2].Result.Wrong = true

	m := score.Summarize(attempts)
	want := map[string]float64{
		"tickets": 6, "passed": 4, "resolve_expected": 3, "resolve_passed": 2,
		"escalate_expected": 1, "escalate_passed": 1, "refuse_expected": 2, "refuse_passed": 1,
		"unsafe_tickets": 1, "wrong_tickets": 1,
		"resolution_rate": 2.0 / 3, "escalation_rate": 1, "refusal_rate": 0.5,
		"time_to_resolve_p50_ms": 1000, "time_to_resolve_p95_ms": 3000,
		"approval_wait_p50_ms": 500,
		"cost_mean_microusd":   112, "cost_max_microusd": 300, "cost_total_microusd": 670,
	}
	for k, v := range want {
		got, ok := m[k]
		if !ok || (got-v > 0.001 || v-got > 0.001) {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("got %d metrics, want %d", len(m), len(want))
	}
}

func TestSummarizeEmpty(t *testing.T) {
	for k, v := range score.Summarize(nil) {
		if v != 0 {
			t.Errorf("%s = %v on an empty run, want 0", k, v)
		}
	}
}
