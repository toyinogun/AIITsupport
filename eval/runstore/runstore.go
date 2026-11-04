// Package runstore saves an eval run and its results through the noord_eval
// role (spec 0018, AC-10). It only writes; scoring lives in eval/score.
package runstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// Header names what a run was made from.
type Header struct {
	Label         string
	GitSHA        string
	Model         string
	TicketSetHash string
	Repeats       int
	PromptHash    string
	ToolsHash     string
}

// Result is one finished ticket attempt.
type Result struct {
	TicketKey       string
	Attempt         int
	ExpectedOutcome string
	// ActualOutcome and RefusalCode are empty for null.
	ActualOutcome  string
	RefusalCode    string
	ExpectedTools  []string
	ActualTools    []string
	Passed         bool
	Unsafe         bool
	Wrong          bool
	FailedChecks   []string
	DurationMS     int
	ApprovalWaitMS int
	InputTokens    int
	OutputTokens   int
	CostMicroUSD   int64
	SlackChannelID string
	SlackThreadTS  string
}

// Store writes one run.
type Store struct {
	q      *queries.Queries
	runID  int64
	header Header
}

// Header is what the run was started with.
func (s *Store) Header() Header { return s.header }

// Copy writes a finished run, with all its results, to another database as one
// new run (EVAL_DATABASE_URL, AC-10). A label already used there fails the copy.
func Copy(ctx context.Context, pool *pgxpool.Pool, h Header, results []Result, status string,
	metrics map[string]float64, extra map[string]any,
) error {
	s, err := Start(ctx, pool, h)
	if err != nil {
		return err
	}
	for _, r := range results {
		if err := s.Add(ctx, r); err != nil {
			return err
		}
	}
	return s.Finish(ctx, status, metrics, extra)
}

// Start inserts the run as running.
func Start(ctx context.Context, pool *pgxpool.Pool, h Header) (*Store, error) {
	q := queries.New(pool)
	run, err := q.CreateEvalRun(ctx, queries.CreateEvalRunParams{
		Label:         text(h.Label),
		GitSha:        h.GitSHA,
		Model:         h.Model,
		TicketSetHash: h.TicketSetHash,
		Repeats:       int32(h.Repeats), //nolint:gosec // bounded 1 to 5 by the CLI and a CHECK
		PromptHash:    h.PromptHash,
		ToolsHash:     h.ToolsHash,
	})
	if err != nil {
		return nil, fmt.Errorf("insert the eval run: %w", err)
	}
	return &Store{q: q, runID: run.ID, header: h}, nil
}

// RunID is the saved run's id.
func (s *Store) RunID() int64 { return s.runID }

// Add saves one finished attempt.
func (s *Store) Add(ctx context.Context, r Result) error {
	err := s.q.InsertEvalResult(ctx, queries.InsertEvalResultParams{
		RunID: s.runID, TicketKey: r.TicketKey, ExpectedOutcome: r.ExpectedOutcome,
		ActualOutcome: text(r.ActualOutcome), ExpectedTools: nonNil(r.ExpectedTools), ActualTools: nonNil(r.ActualTools),
		Passed: r.Passed, UnsafeAction: r.Unsafe, WrongAction: r.Wrong,
		DurationMs: int32(r.DurationMS), InputTokens: int32(r.InputTokens), OutputTokens: int32(r.OutputTokens), //nolint:gosec // small counts
		CostMicrousd: r.CostMicroUSD, SlackChannelID: text(r.SlackChannelID), SlackThreadTs: text(r.SlackThreadTS),
		Attempt: int32(r.Attempt), FailedChecks: nonNil(r.FailedChecks), //nolint:gosec // bounded 1 to 5
		ActualRefusalCode: text(r.RefusalCode), ApprovalWaitMs: int32(r.ApprovalWaitMS), //nolint:gosec // milliseconds of a bounded wait
	})
	if err != nil {
		return fmt.Errorf("insert an eval result: %w", err)
	}
	return nil
}

// Finish closes the run as completed or failed. extra holds keys outside
// metrics, such as stop_reason; CompareEvalRuns ignores them (AC-11).
func (s *Store) Finish(ctx context.Context, status string, metrics map[string]float64, extra map[string]any) error {
	summary := map[string]any{"metrics": metrics}
	for k, v := range extra {
		summary[k] = v
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode the summary: %w", err)
	}
	if _, err := s.q.FinishEvalRun(ctx, queries.FinishEvalRunParams{ID: s.runID, Status: status, Summary: raw}); err != nil {
		return fmt.Errorf("finish the eval run: %w", err)
	}
	return nil
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
