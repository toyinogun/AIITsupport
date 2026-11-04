package runstore_test

import (
	"testing"

	"github.com/toyinogun/AIITsupport/eval/runstore"
	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// covers: AC-10
func TestSaveRun(t *testing.T) {
	pool := dbtest.New(t).Eval
	s, err := runstore.Start(t.Context(), pool, runstore.Header{
		Label: "t1", GitSHA: "abc", Model: "m", TicketSetHash: "h", Repeats: 2, PromptHash: "p", ToolsHash: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		err := s.Add(t.Context(), runstore.Result{
			TicketKey: "ev-001", Attempt: attempt, ExpectedOutcome: "refuse", ActualOutcome: "refuse", RefusalCode: "blocked",
			FailedChecks: nil, CostMicroUSD: 5,
		})
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if err := s.Finish(t.Context(), "completed", map[string]float64{"tickets": 2}, map[string]any{"partial": true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), "failed", nil, nil); err == nil {
		t.Error("a finished run was finished again")
	}
	q := queries.New(pool)
	rows, err := q.ListEvalResults(t.Context(), s.RunID())
	if err != nil || len(rows) != 2 || rows[0].ActualRefusalCode.String != "blocked" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE eval_results SET passed = true"); err == nil {
		t.Error("noord_eval changed a result row")
	}
	if _, err := pool.Exec(t.Context(), "DELETE FROM eval_results"); err == nil {
		t.Error("noord_eval deleted a result row")
	}
}

// covers: AC-10
func TestCopyRunToAnotherDatabase(t *testing.T) {
	to := dbtest.New(t).Eval
	h := runstore.Header{Label: "copy1", GitSHA: "abc", Model: "m", TicketSetHash: "h", Repeats: 1, PromptHash: "p", ToolsHash: "t"}
	results := []runstore.Result{{TicketKey: "ev-001", Attempt: 1, ExpectedOutcome: "resolve", ActualOutcome: "resolve", Passed: true}}
	if err := runstore.Copy(t.Context(), to, h, results, "completed", map[string]float64{"tickets": 1}, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := to.QueryRow(t.Context(), "SELECT count(*) FROM eval_results").Scan(&n); err != nil || n != 1 {
		t.Fatalf("copied results = %d, %v", n, err)
	}
	if err := runstore.Copy(t.Context(), to, h, results, "completed", nil, nil); err == nil {
		t.Fatal("a second copy with the same label succeeded")
	}
}
