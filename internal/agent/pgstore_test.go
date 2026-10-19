package agent_test

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/agent"
	"github.com/toyinogun/AIITsupport/internal/db/dbtest"
)

func mustBlocks(t *testing.T, b ...anthropic.ContentBlockParamUnion) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// covers: AC-6. A tool result and the status change it causes commit
// together, and roll back together when the change cannot apply. Runs only
// when TEST_DATABASE_URL is set.
func TestPgToolResultTransaction(t *testing.T) {
	tdb := dbtest.New(t)
	s := agent.NewStore(tdb.Agent)
	ctx := t.Context()
	c, err := s.UpsertConversation(ctx, "D0JAN", "1.0", "U0JAN")
	if err != nil || c.Status != agent.StatusOpen || c.LastSeq != 0 {
		t.Fatalf("upsert = %+v, %v", c, err)
	}
	if again, _ := s.UpsertConversation(ctx, "D0JAN", "1.0", "U0OTHER"); again.ID != c.ID || again.RequesterSlackID != "U0JAN" {
		t.Errorf("second upsert = %+v, want the same row and requester", again)
	}
	if c, err = s.SetStatus(ctx, c.ID, agent.StatusChange{Status: agent.StatusOpen, DirectoryAccount: "jan.devries"}); err != nil || c.DirectoryAccount != "jan.devries" {
		t.Fatalf("set account = %+v, %v", c, err)
	}

	result := mustBlocks(t, anthropic.NewToolResultBlock("t1", "Ticket created.", false))
	if err := s.AppendToolResult(ctx, c.ID, result, &agent.StatusChange{Status: agent.StatusOpen, TicketID: 42}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendToolResult(ctx, c.ID, result, &agent.StatusChange{Status: agent.StatusResolved}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.UpsertConversation(ctx, "D0JAN", "1.0", "U0JAN")
	if c.TicketID != 42 || c.Status != agent.StatusResolved || c.DirectoryAccount != "jan.devries" || c.LastSeq != 2 {
		t.Errorf("after two results = %+v", c)
	}
	// Resolved is terminal: the change fails, but the result of a call that
	// already ran is still kept (AC-11), and the status is unchanged.
	if err := s.AppendToolResult(ctx, c.ID, result, &agent.StatusChange{Status: agent.StatusEscalated}); !errors.Is(err, agent.ErrNotOpen) {
		t.Errorf("change on a resolved conversation = %v, want ErrNotOpen", err)
	}
	msgs, err := s.ListMessages(ctx, c.ID)
	if err != nil || len(msgs) != 3 || msgs[2].Seq != 3 {
		t.Errorf("messages = %+v, %v; want the third result kept", msgs, err)
	}
	if c, _ = s.UpsertConversation(ctx, "D0JAN", "1.0", "U0JAN"); c.Status != agent.StatusResolved {
		t.Errorf("status = %s, want resolved", c.Status)
	}
	if _, err := s.SetStatus(ctx, c.ID, agent.StatusChange{Status: agent.StatusEscalated}); !errors.Is(err, agent.ErrNotOpen) {
		t.Errorf("SetStatus on resolved = %v, want ErrNotOpen", err)
	}
}

// covers: AC-15, AC-19. Usage rows sum into the token budget, and the
// hourly count sees only the requester's recent conversations.
func TestPgUsageAndHourlyCount(t *testing.T) {
	tdb := dbtest.New(t)
	s := agent.NewStore(tdb.Agent)
	ctx := t.Context()
	c, _ := s.UpsertConversation(ctx, "D0JAN", "1.0", "U0JAN")
	_, _ = s.UpsertConversation(ctx, "D0JAN", "2.0", "U0JAN")
	_, _ = s.UpsertConversation(ctx, "D0KEV", "3.0", "U0KEVIN")
	price, _ := eval.Price("claude-sonnet-5-5")
	tokens := eval.Tokens{Input: 1000, Output: 100, CacheRead: 2000, CacheWrite: 500}
	for range 2 {
		if err := s.InsertUsage(ctx, agent.Usage{ConversationID: c.ID, Model: "claude-sonnet-5-5", Tokens: tokens, CostMicroUSD: price.CostMicroUSD(tokens), PricingDate: price.PricingDate}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.SumTokens(ctx, c.ID); err != nil || n != 7200 {
		t.Errorf("SumTokens = %d, %v; want 7200", n, err)
	}
	if n, err := s.CountRecentConversations(ctx, "U0JAN", time.Now().Add(-time.Hour)); err != nil || n != 2 {
		t.Errorf("CountRecent = %d, %v; want 2", n, err)
	}
	if n, _ := s.CountRecentConversations(ctx, "U0JAN", time.Now().Add(time.Minute)); n != 0 {
		t.Errorf("CountRecent in the future = %d, want 0", n)
	}
}

// covers: AC-14. ListInterruptedConversations matches open, stale
// conversations whose last message is the user's or holds a tool_use block,
// written by the SDK's own param types.
func TestPgListInterrupted(t *testing.T) {
	tdb := dbtest.New(t)
	s := agent.NewStore(tdb.Agent)
	ctx := t.Context()
	userMsg := mustBlocks(t, anthropic.NewTextBlock("I'm locked out"))
	toolUse := mustBlocks(t, anthropic.NewToolUseBlock("t1", map[string]any{}, "account_unlock"))
	final := mustBlocks(t, anthropic.NewTextBlock("Done."))

	mk := func(thread string, msgs ...struct {
		role    string
		content json.RawMessage
	},
	) agent.Conversation {
		c, err := s.UpsertConversation(ctx, "D0JAN", thread, "U0JAN")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if err := s.AppendMessage(ctx, c.ID, m.role, m.content); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	type msg = struct {
		role    string
		content json.RawMessage
	}
	endsUser := mk("1.0", msg{"user", userMsg})
	endsTool := mk("2.0", msg{"user", userMsg}, msg{"assistant", toolUse})
	mk("3.0", msg{"user", userMsg}, msg{"assistant", final})
	mk("4.0")
	escalated := mk("5.0", msg{"user", userMsg})
	if _, err := s.SetStatus(ctx, escalated.ID, agent.StatusChange{Status: agent.StatusEscalated}); err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.Owner.Exec(ctx, "UPDATE conversations SET updated_at = now() - interval '10 minutes'"); err != nil {
		t.Fatal(err)
	}
	recent := mk("6.0", msg{"user", userMsg})

	got, err := s.ListInterrupted(ctx, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != endsUser.ID || got[1].ID != endsTool.ID {
		t.Errorf("interrupted = %+v, want threads 1.0 and 2.0 only (not %d)", got, recent.ID)
	}
}

// covers: AC-14 and the review (2026-10-19). The sweep claims a conversation
// (escalated) before it makes a ticket, so SetTicket fills in the id on a
// closed conversation, but never replaces one already stored.
func TestPgSetTicket(t *testing.T) {
	tdb := dbtest.New(t)
	s := agent.NewStore(tdb.Agent)
	ctx := t.Context()
	c, err := s.UpsertConversation(ctx, "D0JAN", "1.0", "U0JAN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(ctx, c.ID, agent.StatusChange{Status: agent.StatusEscalated}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTicket(ctx, c.ID, 26); err != nil {
		t.Fatalf("set ticket on an escalated conversation: %v", err)
	}
	if err := s.SetTicket(ctx, c.ID, 27); err != nil {
		t.Fatalf("second set ticket: %v", err)
	}
	c, _ = s.UpsertConversation(ctx, "D0JAN", "1.0", "U0JAN")
	if c.TicketID != 26 || c.Status != agent.StatusEscalated {
		t.Errorf("after set ticket = %+v, want ticket 26 and still escalated", c)
	}
}

// covers: AC-8 (spec 0010). With a 30 day window: a conversation closed 31
// days ago is purged, one closed 29 days ago is kept, an open one idle for 31
// days is closed (closed_at = updated_at) and purged in the same pass, and an
// open one active yesterday is untouched. A second pass changes nothing, and
// noord_agent still may not DELETE. Runs only when TEST_DATABASE_URL is set.
func TestPgRetention(t *testing.T) {
	tdb := dbtest.New(t)
	s := agent.NewStore(tdb.Agent)
	ctx := t.Context()
	text := mustBlocks(t, anthropic.NewTextBlock("hello"))
	conv := func(thread string, n int) int64 {
		c, err := s.UpsertConversation(ctx, "D0JAN", thread, "U0JAN")
		if err != nil {
			t.Fatal(err)
		}
		for range n {
			if err := s.AppendMessage(ctx, c.ID, agent.RoleUser, text); err != nil {
				t.Fatal(err)
			}
		}
		return c.ID
	}
	closed31, closed29, idle31, active := conv("1.0", 2), conv("2.0", 1), conv("3.0", 3), conv("4.0", 1)
	setup := []struct {
		sql string
		id  int64
	}{
		{"UPDATE conversations SET status = 'resolved', updated_at = now() - interval '31 days', closed_at = now() - interval '31 days' WHERE id = $1", closed31},
		{"UPDATE conversations SET status = 'escalated', updated_at = now() - interval '29 days', closed_at = now() - interval '29 days' WHERE id = $1", closed29},
		{"UPDATE conversations SET updated_at = now() - interval '31 days' WHERE id = $1", idle31},
		{"UPDATE conversations SET updated_at = now() - interval '1 day' WHERE id = $1", active},
	}
	for _, q := range setup {
		if _, err := tdb.Owner.Exec(ctx, q.sql, q.id); err != nil {
			t.Fatal(err)
		}
	}

	logs := &syncBuffer{}
	r := agent.NewRetention(s, 30, time.Hour, time.Now, slog.New(slog.NewJSONHandler(logs, nil)))
	r.Pass(ctx)
	for _, want := range []string{
		`"msg":"retention_purged","kind":"idle_conversations","rows":1`,
		`"msg":"retention_purged","kind":"messages","rows":5`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("first pass logs lack %s:\n%s", want, logs)
		}
	}

	type state struct {
		status         string
		closedIsUpdate bool
		withText       int
		purged         int
	}
	read := func(id int64) state {
		var st state
		err := tdb.Owner.QueryRow(ctx, `
			SELECT c.status, coalesce(c.closed_at = c.updated_at, false),
			       count(m.*) FILTER (WHERE m.content IS NOT NULL AND m.content_purged_at IS NULL),
			       count(m.*) FILTER (WHERE m.content IS NULL AND m.content_purged_at IS NOT NULL)
			FROM conversations c LEFT JOIN conversation_messages m ON m.conversation_id = c.id
			WHERE c.id = $1 GROUP BY c.id`, id).Scan(&st.status, &st.closedIsUpdate, &st.withText, &st.purged)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	want := map[int64]state{
		closed31: {"resolved", true, 0, 2},
		closed29: {"escalated", true, 1, 0},
		idle31:   {"resolved", true, 0, 3},
		active:   {"open", false, 1, 0},
	}
	for id, w := range want {
		if got := read(id); got != w {
			t.Errorf("conversation %d = %+v, want %+v", id, got, w)
		}
	}

	logs2 := &syncBuffer{}
	agent.NewRetention(s, 30, time.Hour, time.Now, slog.New(slog.NewJSONHandler(logs2, nil))).Pass(ctx)
	if n := strings.Count(logs2.String(), `"rows":0`); n != 2 {
		t.Errorf("second pass changed rows:\n%s", logs2)
	}

	for _, table := range []string{"conversation_messages", "conversations"} {
		_, err := tdb.Agent.Exec(ctx, "DELETE FROM "+table+" WHERE false")
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "42501" {
			t.Errorf("noord_agent DELETE on %s = %v, want 42501", table, err)
		}
	}
}
