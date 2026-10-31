package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/toyinogun/AIITsupport/internal/db/queries"
)

// The approval_cards side of pgStore (spec 0012). Every move is a guarded
// UPDATE: zero rows back means another worker or tick got there first.

func (s *pgStore) GetConversation(ctx context.Context, id int64) (Conversation, error) {
	c, err := s.q.GetConversation(ctx, id)
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	return fromRow(c), nil
}

// AppendToolResultWithCard writes a pending tool result and its card row in
// one transaction (AC-6), so no stored result names an approval the agent
// does not track.
func (s *pgStore) AppendToolResultWithCard(ctx context.Context, id int64, content json.RawMessage, card ApprovalCard) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tool result: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // a no op after Commit
	q := s.q.WithTx(tx)
	if err := appendMessage(ctx, q, id, RoleUser, content); err != nil {
		return false, err
	}
	n, err := q.InsertApprovalCard(ctx, queries.InsertApprovalCardParams{
		ApprovalID: card.ApprovalID, ConversationID: id, ToolName: card.ToolName,
		SlackChannelID: optText(card.ChannelID), ExpiresAt: card.ExpiresAt,
	})
	if err != nil {
		return false, fmt.Errorf("insert approval card: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit tool result: %w", err)
	}
	return n == 1, nil
}

func (s *pgStore) RecordCardPostAttempt(ctx context.Context, approvalID uuid.UUID, maxAttempts int, spacing time.Duration) (bool, error) {
	n, err := s.q.RecordCardPostAttempt(ctx, queries.RecordCardPostAttemptParams{
		ApprovalID: approvalID, MaxAttempts: clampInt32(int64(min(maxAttempts, maxCardPosts))), SpacingSeconds: spacing.Seconds(),
	})
	if err != nil {
		return false, fmt.Errorf("record card post attempt: %w", err)
	}
	return n == 1, nil
}

func (s *pgStore) SetCardPosted(ctx context.Context, approvalID uuid.UUID, ts, channel, approverSlackID string) error {
	_, err := s.q.SetApprovalCardPosted(ctx, queries.SetApprovalCardPostedParams{
		ApprovalID: approvalID, CardTs: ts, SlackChannelID: channel, ApproverSlackID: optText(approverSlackID),
	})
	if err != nil {
		return fmt.Errorf("set card posted: %w", err)
	}
	return nil
}

// optText is a nullable text: empty is NULL.
func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func (s *pgStore) GetApprovalCard(ctx context.Context, approvalID uuid.UUID) (ApprovalCard, error) {
	c, err := s.q.GetApprovalCard(ctx, approvalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalCard{}, ErrNoCard
	}
	if err != nil {
		return ApprovalCard{}, fmt.Errorf("get approval card: %w", err)
	}
	return cardFromRow(c), nil
}

// SettleCard settles the card and, when still open, the conversation, in
// one transaction (AC-15).
func (s *pgStore) SettleCard(ctx context.Context, approvalID uuid.UUID, outcome string, change StatusChange) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin settle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	card, err := q.GetApprovalCard(ctx, approvalID)
	if err != nil {
		return false, fmt.Errorf("get approval card: %w", err)
	}
	n, err := q.SettleApprovalCard(ctx, queries.SettleApprovalCardParams{ApprovalID: approvalID, Outcome: outcome})
	if err != nil {
		return false, fmt.Errorf("settle approval card: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	if _, err := setStatus(ctx, q, card.ConversationID, change); err != nil && !errors.Is(err, ErrNotOpen) {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit settle: %w", err)
	}
	return true, nil
}

// AdoptApprovalCard inserts a card row for an approval the agent never
// stored a result for, in the conversation that owns its thread (AC-14).
func (s *pgStore) AdoptApprovalCard(ctx context.Context, card ApprovalCard, threadChannel, threadTS string) (bool, error) {
	n, err := s.q.AdoptApprovalCard(ctx, queries.AdoptApprovalCardParams{
		ApprovalID: card.ApprovalID, ToolName: card.ToolName, CardChannelID: optText(card.ChannelID), ExpiresAt: card.ExpiresAt,
		ThreadChannelID: threadChannel, ThreadTs: threadTS,
	})
	if err != nil {
		return false, fmt.Errorf("adopt approval card: %w", err)
	}
	return n == 1, nil
}

func (s *pgStore) HaveApprovalCards(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	have := make(map[uuid.UUID]bool, len(ids))
	if len(ids) == 0 {
		return have, nil
	}
	rows, err := s.q.ApprovalCardIDsAmong(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read approval card ids: %w", err)
	}
	for _, id := range rows {
		have[id] = true
	}
	return have, nil
}

func (s *pgStore) ListUnsettledApprovalCards(ctx context.Context, limit int) ([]ApprovalCard, error) {
	rows, err := s.q.ListUnsettledApprovalCards(ctx, clampInt32(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("list unsettled approval cards: %w", err)
	}
	out := make([]ApprovalCard, 0, len(rows))
	for _, r := range rows {
		out = append(out, cardFromRow(r))
	}
	return out, nil
}

func cardFromRow(c queries.ApprovalCard) ApprovalCard {
	return ApprovalCard{
		ApprovalID: c.ApprovalID, ConversationID: c.ConversationID, ToolName: c.ToolName,
		ChannelID: c.SlackChannelID.String, ApproverSlackID: c.ApproverSlackID.String,
		CardTS: c.CardTs.String, PostAttempts: int(c.PostAttempts),
		ExpiresAt: c.ExpiresAt, Settled: c.SettledAt.Valid, Outcome: c.Outcome.String,
	}
}
