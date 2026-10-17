package db

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// These run as the role that owns each table's writes in production
// (noord_agent or noord_eval), so they double as grant checks.

func agentQueries(t *testing.T) *queries.Queries {
	t.Helper()
	return queries.New(dbtest.New(t).Agent)
}

func newConversation(t *testing.T, q *queries.Queries, thread string) queries.Conversation {
	t.Helper()
	c, err := q.UpsertConversation(t.Context(), queries.UpsertConversationParams{
		SlackChannelID: "C01HELP", SlackThreadTs: thread, RequesterSlackID: "U01REQUESTER",
	})
	if err != nil {
		t.Fatalf("UpsertConversation: %v", err)
	}
	return c
}

func appendText(t *testing.T, q *queries.Queries, conversationID int64, role, text string) queries.ConversationMessage {
	t.Helper()
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		t.Fatalf("encode content: %v", err)
	}
	m, err := q.AppendMessage(t.Context(), queries.AppendMessageParams{ConversationID: conversationID, Role: role, Content: content})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	return m
}

// covers: AC-9 (spec 0003)
func TestUpsertingAThreadReturnsTheSameConversation(t *testing.T) {
	q := agentQueries(t)
	first := newConversation(t, q, "1760000000.000100")
	again := newConversation(t, q, "1760000000.000100")

	if again.ID != first.ID || again.Status != "open" {
		t.Fatalf("second upsert = id %d status %s, want id %d still open", again.ID, again.Status, first.ID)
	}
	if again.UpdatedAt.Before(first.UpdatedAt) {
		t.Error("updated_at went backwards")
	}
	got, err := q.GetConversationByThread(t.Context(), queries.GetConversationByThreadParams{
		SlackChannelID: "C01HELP", SlackThreadTs: "1760000000.000100",
	})
	if err != nil || got.ID != first.ID {
		t.Fatalf("GetConversationByThread = %d, %v", got.ID, err)
	}
}

// covers: AC-9 (spec 0003)
func TestConcurrentAppendsGetGaplessSeq(t *testing.T) {
	q := agentQueries(t)
	c := newConversation(t, q, "1")

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			_, err := q.AppendMessage(t.Context(), queries.AppendMessageParams{
				ConversationID: c.ID, Role: "user", Content: json.RawMessage(`[]`),
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	msgs, err := q.ListMessages(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("messages = %d, want %d", len(msgs), n)
	}
	for i, m := range msgs {
		if m.Seq != int32(i+1) {
			t.Fatalf("seq at %d = %d, want %d (gapless, in order)", i, m.Seq, i+1)
		}
	}
}

func TestAppendingToAMissingConversationInsertsNothing(t *testing.T) {
	q := agentQueries(t)
	_, err := q.AppendMessage(t.Context(), queries.AppendMessageParams{ConversationID: 999, Role: "user", Content: json.RawMessage(`[]`)})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want no rows", err)
	}
}

// covers: AC-9 (spec 0003)
func TestConversationStatusMovesOnlyFromOpen(t *testing.T) {
	q := agentQueries(t)
	c := newConversation(t, q, "1")

	matched, err := q.SetConversationStatus(t.Context(), queries.SetConversationStatusParams{
		ID: c.ID, Status: "open", DirectoryAccount: pgtype.Text{String: "jdevries", Valid: true},
	})
	if err != nil || matched.DirectoryAccount.String != "jdevries" || matched.ClosedAt.Valid {
		t.Fatalf("filling the account = %+v, %v; want it set and still open", matched, err)
	}
	closed, err := q.SetConversationStatus(t.Context(), queries.SetConversationStatusParams{
		ID: c.ID, Status: "resolved", ZammadTicketID: pgtype.Int8{Int64: 42, Valid: true},
	})
	if err != nil || !closed.ClosedAt.Valid || closed.ZammadTicketID.Int64 != 42 || closed.DirectoryAccount.String != "jdevries" {
		t.Fatalf("resolving = %+v, %v; want closed, ticket set, account kept", closed, err)
	}
	if _, err := q.SetConversationStatus(t.Context(), queries.SetConversationStatusParams{ID: c.ID, Status: "escalated"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a closed conversation changed again: %v", err)
	}
}

// covers: AC-9 (spec 0003)
func TestPurgeClearsOnlyClosedConversations(t *testing.T) {
	q := agentQueries(t)
	closed := newConversation(t, q, "closed")
	open := newConversation(t, q, "open")
	appendText(t, q, closed.ID, "user", "my password is on a sticky note")
	appendText(t, q, open.ID, "user", "still talking")
	if _, err := q.SetConversationStatus(t.Context(), queries.SetConversationStatusParams{ID: closed.ID, Status: "resolved"}); err != nil {
		t.Fatalf("close: %v", err)
	}

	if n, err := q.PurgeMessageContentBefore(t.Context(), time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("purge with an old cutoff = %d, %v; want nothing purged", n, err)
	}
	n, err := q.PurgeMessageContentBefore(t.Context(), time.Now().Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("purge = %d, %v; want the closed conversation's one message", n, err)
	}

	purged, _ := q.ListMessages(t.Context(), closed.ID)
	kept, _ := q.ListMessages(t.Context(), open.ID)
	if purged[0].Content != nil || !purged[0].ContentPurgedAt.Valid {
		t.Errorf("closed message = %s, purged at %v; want content cleared", purged[0].Content, purged[0].ContentPurgedAt)
	}
	if kept[0].Content == nil {
		t.Error("an open conversation lost its content")
	}
}

// covers: AC-10 (spec 0003)
func TestASlackEventIsSeenOnce(t *testing.T) {
	q := agentQueries(t)
	first, err := q.MarkEventSeen(t.Context(), "Ev01")
	if err != nil || first != 1 {
		t.Fatalf("first delivery = %d, %v; want 1", first, err)
	}
	if again, err := q.MarkEventSeen(t.Context(), "Ev01"); err != nil || again != 0 {
		t.Fatalf("redelivery = %d, %v; want 0 (duplicate)", again, err)
	}
	if n, err := q.PurgeSeenEventsBefore(t.Context(), time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("purge before the event = %d, %v; want 0", n, err)
	}
	if n, err := q.PurgeSeenEventsBefore(t.Context(), time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("purge after the event = %d, %v; want 1", n, err)
	}
}

// covers: AC-11 (spec 0003)
func TestUsageSumsCountEveryTokenKind(t *testing.T) {
	q := agentQueries(t)
	c := newConversation(t, q, "1")

	if tokens, err := q.SumConversationTokens(t.Context(), c.ID); err != nil || tokens != 0 {
		t.Fatalf("empty sum = %d, %v; want 0", tokens, err)
	}
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, u := range []queries.InsertLLMUsageParams{
		{ConversationID: c.ID, Model: "claude-sonnet-5-5", InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 3000, CacheWriteTokens: 500, CostMicrousd: 4200, PricingDate: day},
		{ConversationID: c.ID, Model: "claude-sonnet-5-5", InputTokens: 10, OutputTokens: 20, CostMicrousd: 300, PricingDate: day},
	} {
		if err := q.InsertLLMUsage(t.Context(), u); err != nil {
			t.Fatalf("InsertLLMUsage: %v", err)
		}
	}

	if tokens, err := q.SumConversationTokens(t.Context(), c.ID); err != nil || tokens != 4730 {
		t.Fatalf("token sum = %d, %v; want 4730 (input, output, and both cache kinds)", tokens, err)
	}
	u, err := q.SumConversationUsage(t.Context(), c.ID)
	if err != nil {
		t.Fatalf("SumConversationUsage: %v", err)
	}
	want := queries.SumConversationUsageRow{InputTokens: 1010, OutputTokens: 220, CacheReadTokens: 3000, CacheWriteTokens: 500, CostMicrousd: 4500}
	if u != want {
		t.Errorf("usage = %+v, want %+v", u, want)
	}
}

func finishRun(t *testing.T, q *queries.Queries, id int64, metrics map[string]float64) {
	t.Helper()
	summary, err := json.Marshal(map[string]any{"metrics": metrics})
	if err != nil {
		t.Fatalf("encode summary: %v", err)
	}
	if _, err := q.FinishEvalRun(t.Context(), queries.FinishEvalRunParams{ID: id, Status: "completed", Summary: summary}); err != nil {
		t.Fatalf("FinishEvalRun: %v", err)
	}
}

func createRun(t *testing.T, q *queries.Queries, label string) queries.EvalRun {
	t.Helper()
	run, err := q.CreateEvalRun(t.Context(), queries.CreateEvalRunParams{
		Label: pgtype.Text{String: label, Valid: true}, GitSha: "abc123", Model: "claude-sonnet-5-5", TicketSetHash: "h1",
	})
	if err != nil {
		t.Fatalf("CreateEvalRun %s: %v", label, err)
	}
	return run
}

// covers: AC-12 (spec 0003)
func TestEvalRunsAreStoredAndCompared(t *testing.T) {
	q := queries.New(dbtest.New(t).Eval)
	v1 := createRun(t, q, "v1")
	final := createRun(t, q, "final")

	err := q.InsertEvalResult(t.Context(), queries.InsertEvalResultParams{
		RunID: v1.ID, TicketKey: "T01", ExpectedOutcome: "resolve",
		ActualOutcome: pgtype.Text{String: "escalate", Valid: true},
		ExpectedTools: []string{"user_lookup", "account_unlock"}, ActualTools: []string{"user_lookup"},
		WrongAction: false, DurationMs: 8100, InputTokens: 900, OutputTokens: 120, CostMicrousd: 4100,
	})
	if err != nil {
		t.Fatalf("InsertEvalResult: %v", err)
	}
	results, err := q.ListEvalResults(t.Context(), v1.ID)
	if err != nil || len(results) != 1 || !slices.Equal(results[0].ExpectedTools, []string{"user_lookup", "account_unlock"}) {
		t.Fatalf("ListEvalResults = %+v, %v", results, err)
	}

	finishRun(t, q, v1.ID, map[string]float64{"resolution_rate": 0.62, "unsafe_actions": 2})
	finishRun(t, q, final.ID, map[string]float64{"resolution_rate": 0.81, "unsafe_actions": 0, "cost_per_ticket_microusd": 3900})
	if _, err := q.FinishEvalRun(t.Context(), queries.FinishEvalRunParams{ID: v1.ID, Status: "failed"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a finished run was finished again: %v", err)
	}

	rows, err := q.CompareEvalRuns(t.Context(), queries.CompareEvalRunsParams{LabelA: "v1", LabelB: "final"})
	if err != nil {
		t.Fatalf("CompareEvalRuns: %v", err)
	}
	want := []queries.CompareEvalRunsRow{
		{Metric: "resolution_rate", ValueA: 0.62, ValueB: 0.81},
		{Metric: "unsafe_actions", ValueA: 2, ValueB: 0},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("comparison = %+v, want %+v (metrics in both runs only)", rows, want)
	}
	if rows, err := q.CompareEvalRuns(t.Context(), queries.CompareEvalRunsParams{LabelA: "v1", LabelB: "v2"}); err != nil || len(rows) != 0 {
		t.Errorf("comparison with a missing label = %d rows, %v; want none", len(rows), err)
	}
}

// covers: AC-12 (spec 0003)
func TestEvalRunRules(t *testing.T) {
	q := queries.New(dbtest.New(t).Eval)
	run := createRun(t, q, "v1")

	if _, err := q.CreateEvalRun(t.Context(), queries.CreateEvalRunParams{
		Label: pgtype.Text{String: "v1", Valid: true}, GitSha: "def", Model: "m", TicketSetHash: "h",
	}); pgCode(err) != "23505" {
		t.Errorf("reusing a label: err = %v, want a unique violation", err)
	}
	if _, err := q.FinishEvalRun(t.Context(), queries.FinishEvalRunParams{ID: run.ID, Status: "completed"}); pgCode(err) != "23514" {
		t.Errorf("completing without a summary: err = %v, want a check violation", err)
	}
}
