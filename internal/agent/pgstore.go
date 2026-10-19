package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// pgStore is the Store over the agent's Postgres role (noord_agent).
type pgStore struct {
	pool *pgxpool.Pool
	q    *queries.Queries
}

// NewStore returns the Store backed by pool.
func NewStore(pool *pgxpool.Pool) Store {
	return &pgStore{pool: pool, q: queries.New(pool)}
}

func (s *pgStore) UpsertConversation(ctx context.Context, channelID, threadTS, requesterSlackID string) (Conversation, error) {
	c, err := s.q.UpsertConversation(ctx, queries.UpsertConversationParams{
		SlackChannelID: channelID, SlackThreadTs: threadTS, RequesterSlackID: requesterSlackID,
	})
	if err != nil {
		return Conversation{}, fmt.Errorf("upsert conversation: %w", err)
	}
	return fromRow(c), nil
}

func (s *pgStore) SetStatus(ctx context.Context, id int64, ch StatusChange) (Conversation, error) {
	return setStatus(ctx, s.q, id, ch)
}

func setStatus(ctx context.Context, q *queries.Queries, id int64, ch StatusChange) (Conversation, error) {
	params := queries.SetConversationStatusParams{Status: ch.Status, ID: id}
	if ch.DirectoryAccount != "" {
		params.DirectoryAccount = pgtype.Text{String: ch.DirectoryAccount, Valid: true}
	}
	if ch.TicketID != 0 {
		params.ZammadTicketID = pgtype.Int8{Int64: ch.TicketID, Valid: true}
	}
	c, err := q.SetConversationStatus(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotOpen
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("set conversation status: %w", err)
	}
	return fromRow(c), nil
}

func (s *pgStore) AppendMessage(ctx context.Context, id int64, role string, content json.RawMessage) error {
	return appendMessage(ctx, s.q, id, role, content)
}

func appendMessage(ctx context.Context, q *queries.Queries, id int64, role string, content json.RawMessage) error {
	if _, err := q.AppendMessage(ctx, queries.AppendMessageParams{ConversationID: id, Role: role, Content: content}); err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	return nil
}

// AppendToolResult writes the tool result and the status change it causes in
// one transaction, so a ticket id or resolved status is never stored apart
// from its result (AC-6). When the conversation is no longer open, the
// result is still kept (AC-11) and ErrNotOpen is returned.
func (s *pgStore) AppendToolResult(ctx context.Context, id int64, content json.RawMessage, ch *StatusChange) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tool result: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no op after Commit
	q := s.q.WithTx(tx)
	if err := appendMessage(ctx, q, id, RoleUser, content); err != nil {
		return err
	}
	var notOpen error
	if ch != nil {
		if _, err := setStatus(ctx, q, id, *ch); errors.Is(err, ErrNotOpen) {
			notOpen = err
		} else if err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tool result: %w", err)
	}
	return notOpen
}

func (s *pgStore) ListMessages(ctx context.Context, id int64) ([]StoredMessage, error) {
	rows, err := s.q.ListMessages(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	out := make([]StoredMessage, len(rows))
	for i, r := range rows {
		out[i] = StoredMessage{Seq: int(r.Seq), Role: r.Role, Content: r.Content}
	}
	return out, nil
}

func (s *pgStore) InsertUsage(ctx context.Context, u Usage) error {
	err := s.q.InsertLLMUsage(ctx, queries.InsertLLMUsageParams{
		ConversationID:   u.ConversationID,
		Model:            u.Model,
		InputTokens:      clampInt32(u.Tokens.Input),
		OutputTokens:     clampInt32(u.Tokens.Output),
		CacheReadTokens:  clampInt32(u.Tokens.CacheRead),
		CacheWriteTokens: clampInt32(u.Tokens.CacheWrite),
		CostMicrousd:     u.CostMicroUSD,
		PricingDate:      u.PricingDate,
	})
	if err != nil {
		return fmt.Errorf("insert llm usage: %w", err)
	}
	return nil
}

func (s *pgStore) SumTokens(ctx context.Context, id int64) (int64, error) {
	n, err := s.q.SumConversationTokens(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("sum conversation tokens: %w", err)
	}
	return n, nil
}

func (s *pgStore) CountRecentConversations(ctx context.Context, requesterSlackID string, since time.Time) (int64, error) {
	n, err := s.q.CountRecentConversations(ctx, queries.CountRecentConversationsParams{RequesterSlackID: requesterSlackID, Since: since})
	if err != nil {
		return 0, fmt.Errorf("count recent conversations: %w", err)
	}
	return n, nil
}

func (s *pgStore) ListInterrupted(ctx context.Context, staleBefore time.Time) ([]Conversation, error) {
	rows, err := s.q.ListInterruptedConversations(ctx, staleBefore)
	if err != nil {
		return nil, fmt.Errorf("list interrupted conversations: %w", err)
	}
	out := make([]Conversation, len(rows))
	for i, r := range rows {
		out[i] = fromRow(r)
	}
	return out, nil
}

func fromRow(c queries.Conversation) Conversation {
	return Conversation{
		ID:               c.ID,
		ChannelID:        c.SlackChannelID,
		ThreadTS:         c.SlackThreadTs,
		RequesterSlackID: c.RequesterSlackID,
		DirectoryAccount: c.DirectoryAccount.String,
		TicketID:         c.ZammadTicketID.Int64,
		Status:           c.Status,
		LastSeq:          int(c.LastSeq),
	}
}

// clampInt32 keeps a token count inside the int column; real counts are far below.
func clampInt32(n int64) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}
