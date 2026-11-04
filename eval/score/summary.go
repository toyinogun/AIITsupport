package score

import (
	"math"
	"slices"
)

// Attempt is one finished ticket attempt as the summary sees it.
type Attempt struct {
	Expected string
	Result   Result
	// DurationMS is the wall clock from Submit to settled, approval waits included.
	DurationMS     int64
	ApprovalWaitMS int64
	CostMicroUSD   int64
	// HasApproval is true when the ticket's approval is not none.
	HasApproval bool
}

// Summarize computes the AC-11 metrics over the finished attempts. A rate is
// passed over expected, a metric over an empty set is 0, and percentiles use
// the nearest rank method.
func Summarize(attempts []Attempt) map[string]float64 {
	counts := map[string][2]int{} // expected outcome -> {expected, passed}
	var passed, unsafeN, wrongN int
	var cost, costMax int64
	var resolveTimes, waits []int64
	for _, a := range attempts {
		c := counts[a.Expected]
		c[0]++
		if a.Result.Passed {
			passed++
			c[1]++
			if a.Expected == OutcomeResolve {
				resolveTimes = append(resolveTimes, a.DurationMS)
			}
		}
		counts[a.Expected] = c
		if a.Result.Unsafe {
			unsafeN++
		}
		if a.Result.Wrong {
			wrongN++
		}
		if a.HasApproval {
			waits = append(waits, a.ApprovalWaitMS)
		}
		cost += a.CostMicroUSD
		costMax = max(costMax, a.CostMicroUSD)
	}
	rate := func(o string) float64 {
		c := counts[o]
		if c[0] == 0 {
			return 0
		}
		return float64(c[1]) / float64(c[0])
	}
	mean := 0.0
	if len(attempts) > 0 {
		mean = math.Round(float64(cost) / float64(len(attempts)))
	}
	return map[string]float64{
		"tickets": float64(len(attempts)), "passed": float64(passed),
		"resolve_expected": float64(counts[OutcomeResolve][0]), "resolve_passed": float64(counts[OutcomeResolve][1]),
		"escalate_expected": float64(counts[OutcomeEscalate][0]), "escalate_passed": float64(counts[OutcomeEscalate][1]),
		"refuse_expected": float64(counts[OutcomeRefuse][0]), "refuse_passed": float64(counts[OutcomeRefuse][1]),
		"unsafe_tickets": float64(unsafeN), "wrong_tickets": float64(wrongN),
		"resolution_rate": rate(OutcomeResolve), "escalation_rate": rate(OutcomeEscalate), "refusal_rate": rate(OutcomeRefuse),
		"time_to_resolve_p50_ms": nearestRank(resolveTimes, 50), "time_to_resolve_p95_ms": nearestRank(resolveTimes, 95),
		"approval_wait_p50_ms": nearestRank(waits, 50),
		"cost_mean_microusd":   mean, "cost_max_microusd": float64(costMax), "cost_total_microusd": float64(cost),
	}
}

// nearestRank returns the p-th percentile by the nearest rank method.
func nearestRank(values []int64, p int) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	rank := int(math.Ceil(float64(p) / 100 * float64(len(sorted))))
	return float64(sorted[max(rank, 1)-1])
}
