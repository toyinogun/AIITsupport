package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/agent/agenttest"
)

// covers: AC-8 (spec 0010). A pass closes an idle conversation and purges
// its text in the same pass, and logs each step's rows.
func TestRetentionPassClosesThenPurges(t *testing.T) {
	clock := now
	store := agenttest.NewStore(func() time.Time { return clock })
	ctx := t.Context()
	idle, _ := store.UpsertConversation(ctx, janDM, "1.0", janSlack)
	if err := store.AppendMessage(ctx, idle.ID, agent.RoleUser, json.RawMessage(`[{"type":"text","text":"hi"}]`)); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(31 * 24 * time.Hour)
	active, _ := store.UpsertConversation(ctx, janDM, "2.0", janSlack)
	if err := store.AppendMessage(ctx, active.ID, agent.RoleUser, json.RawMessage(`[{"type":"text","text":"hi"}]`)); err != nil {
		t.Fatal(err)
	}

	logs := &syncBuffer{}
	r := agent.NewRetention(store, 30, time.Hour, func() time.Time { return clock }, slog.New(slog.NewJSONHandler(logs, nil)))
	r.Pass(ctx)

	if c, _ := store.Conversation(janDM, "1.0"); c.Status != agent.StatusResolved {
		t.Errorf("idle conversation = %+v, want resolved", c)
	}
	if m := store.Messages(idle.ID); m[0].Content != nil {
		t.Errorf("idle conversation's text kept: %s", m[0].Content)
	}
	if c, _ := store.Conversation(janDM, "2.0"); c.Status != agent.StatusOpen || store.Messages(active.ID)[0].Content == nil {
		t.Errorf("active conversation changed: %+v", c)
	}
	for _, want := range []string{
		`"msg":"retention_purged","kind":"idle_conversations","rows":1`,
		`"msg":"retention_purged","kind":"messages","rows":1`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %s:\n%s", want, logs)
		}
	}
}

// covers: AC-8 (spec 0010). A failed step logs retention_purge_failed with a
// postgres_<sqlstate> cause and no error text, and the next tick tries again.
func TestRetentionFailureRetriesNextTick(t *testing.T) {
	store := agenttest.NewStore(func() time.Time { return now })
	store.FailWith("PurgeMessageContent", fmt.Errorf("purge: %w", &pgconn.PgError{Code: "57P01", Message: "ZEBRA"}))
	logs := &syncBuffer{}
	r := agent.NewRetention(store, 30, 10*time.Millisecond, func() time.Time { return now }, slog.New(slog.NewJSONHandler(logs, nil)))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	failed := `"msg":"retention_purge_failed","kind":"messages","cause":"postgres_57P01"`
	waitFor(t, func() bool { return strings.Contains(logs.String(), failed) })
	store.Heal("PurgeMessageContent")
	waitFor(t, func() bool {
		return strings.Contains(logs.String(), `"msg":"retention_purged","kind":"messages","rows":0`)
	})
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v, want nil when its context ends", err)
	}
	if strings.Contains(logs.String(), "ZEBRA") {
		t.Errorf("error text leaked:\n%s", logs)
	}
}

// covers: AC-8 (spec 0010). When the agent's context has ended, a pass logs
// nothing and Run returns nil.
func TestRetentionStopsQuietly(t *testing.T) {
	store := agenttest.NewStore(func() time.Time { return now })
	logs := &syncBuffer{}
	r := agent.NewRetention(store, 30, time.Hour, func() time.Time { return now }, slog.New(slog.NewJSONHandler(logs, nil)))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Run(ctx); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
	if logs.String() != "" {
		t.Errorf("a pass on an ended context logged:\n%s", logs)
	}
}

// hangingStore never answers until its context ends, like a stalled
// connection to Postgres.
type hangingStore struct{}

func (hangingStore) CloseIdleConversations(ctx context.Context, _ time.Time) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func (hangingStore) PurgeMessageContent(ctx context.Context, _ time.Time) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

// covers: AC-8 (spec 0010), the review of 2026-10-19. A hung query fails its
// step with cause deadline and the pass moves on, instead of stopping the loop.
func TestRetentionStepTimesOut(t *testing.T) {
	logs := &syncBuffer{}
	r := agent.NewRetention(hangingStore{}, 30, time.Hour, func() time.Time { return now }, slog.New(slog.NewJSONHandler(logs, nil)))
	r.SetStepTimeout(20 * time.Millisecond)
	r.Pass(t.Context())
	for _, kind := range []string{"idle_conversations", "messages"} {
		want := `"msg":"retention_purge_failed","kind":"` + kind + `","cause":"deadline"`
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %s:\n%s", want, logs)
		}
	}
}
