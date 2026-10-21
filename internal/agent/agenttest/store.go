package agenttest

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/agent"
)

// ErrStoreDown is the error a failing store method returns.
var ErrStoreDown = errors.New("agenttest: the store is down")

// Store is an in memory agent.Store that answers like the SQL. Like
// Postgres, it fails a call whose context has ended.
type Store struct {
	mu      sync.Mutex
	Now     func() time.Time
	convs   []*row
	msgs    map[int64][]agent.StoredMessage
	usage   []agent.Usage
	failOn  map[string]bool
	failErr map[string]error
	cards   []*cardRow
}

type cardRow struct {
	agent.ApprovalCard
	lastPostAt time.Time
}

type row struct {
	agent.Conversation
	createdAt, updatedAt, closedAt time.Time
}

// NewStore returns an empty store on the given clock.
func NewStore(now func() time.Time) *Store {
	return &Store{Now: now, msgs: map[int64][]agent.StoredMessage{}, failOn: map[string]bool{}}
}

// FailOn makes the named method (for example "AppendToolResult") fail.
func (s *Store) FailOn(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failOn[method] = true
}

func (s *Store) check(ctx context.Context, method string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.failErr[method]; err != nil {
		return err
	}
	if s.failOn[method] {
		return ErrStoreDown
	}
	return nil
}

// UpsertConversation returns the thread's conversation, creating it open.
func (s *Store) UpsertConversation(ctx context.Context, channelID, threadTS, requesterSlackID string) (agent.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "UpsertConversation"); err != nil {
		return agent.Conversation{}, err
	}
	for _, r := range s.convs {
		if r.ChannelID == channelID && r.ThreadTS == threadTS {
			r.updatedAt = s.Now()
			return r.Conversation, nil
		}
	}
	r := &row{Conversation: agent.Conversation{
		ID: int64(len(s.convs) + 1), ChannelID: channelID, ThreadTS: threadTS,
		RequesterSlackID: requesterSlackID, Status: agent.StatusOpen,
	}, createdAt: s.Now(), updatedAt: s.Now()}
	s.convs = append(s.convs, r)
	return r.Conversation, nil
}

// SetStatus changes an open conversation; agent.ErrNotOpen otherwise.
func (s *Store) SetStatus(ctx context.Context, id int64, ch agent.StatusChange) (agent.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "SetStatus"); err != nil {
		return agent.Conversation{}, err
	}
	return s.setStatus(id, ch)
}

func (s *Store) setStatus(id int64, ch agent.StatusChange) (agent.Conversation, error) {
	r := s.find(id)
	if r == nil || r.Status != agent.StatusOpen {
		return agent.Conversation{}, agent.ErrNotOpen
	}
	r.Status = ch.Status
	if ch.DirectoryAccount != "" {
		r.DirectoryAccount = ch.DirectoryAccount
	}
	if ch.TicketID != 0 {
		r.TicketID = ch.TicketID
	}
	r.updatedAt = s.Now()
	if r.Status != agent.StatusOpen {
		r.closedAt = r.updatedAt
	}
	return r.Conversation, nil
}

// SetTicket stores a ticket id unless one is already stored.
func (s *Store) SetTicket(ctx context.Context, id, ticketID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "SetTicket"); err != nil {
		return err
	}
	if r := s.find(id); r != nil && r.TicketID == 0 {
		r.TicketID = ticketID
		r.updatedAt = s.Now()
	}
	return nil
}

// AppendMessage stores the next message with a gapless seq.
func (s *Store) AppendMessage(ctx context.Context, id int64, role string, content json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "AppendMessage"); err != nil {
		return err
	}
	return s.append(id, role, content)
}

func (s *Store) append(id int64, role string, content json.RawMessage) error {
	r := s.find(id)
	if r == nil {
		return errors.New("agenttest: no such conversation")
	}
	r.LastSeq++
	r.updatedAt = s.Now()
	s.msgs[id] = append(s.msgs[id], agent.StoredMessage{Seq: r.LastSeq, Role: role, Content: append(json.RawMessage(nil), content...)})
	return nil
}

// AppendToolResult is all or nothing, like the transaction.
func (s *Store) AppendToolResult(ctx context.Context, id int64, content json.RawMessage, ch *agent.StatusChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "AppendToolResult"); err != nil {
		return err
	}
	if err := s.append(id, agent.RoleUser, content); err != nil {
		return err
	}
	if ch != nil {
		_, err := s.setStatus(id, *ch)
		return err
	}
	return nil
}

// ListMessages returns a conversation's messages in seq order.
func (s *Store) ListMessages(ctx context.Context, id int64) ([]agent.StoredMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "ListMessages"); err != nil {
		return nil, err
	}
	return append([]agent.StoredMessage(nil), s.msgs[id]...), nil
}

// InsertUsage records an llm_usage row.
func (s *Store) InsertUsage(ctx context.Context, u agent.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "InsertUsage"); err != nil {
		return err
	}
	s.usage = append(s.usage, u)
	return nil
}

// SumUsage adds up each of the conversation's four token counts.
func (s *Store) SumUsage(ctx context.Context, id int64) (eval.Tokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "SumUsage"); err != nil {
		return eval.Tokens{}, err
	}
	var sum eval.Tokens
	for _, u := range s.usage {
		if u.ConversationID == id {
			sum.Input += u.Tokens.Input
			sum.Output += u.Tokens.Output
			sum.CacheRead += u.Tokens.CacheRead
			sum.CacheWrite += u.Tokens.CacheWrite
		}
	}
	return sum, nil
}

// CountRecentConversations counts the requester's conversations created after since.
func (s *Store) CountRecentConversations(ctx context.Context, requesterSlackID string, since time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "CountRecentConversations"); err != nil {
		return 0, err
	}
	var n int64
	for _, r := range s.convs {
		if r.RequesterSlackID == requesterSlackID && r.createdAt.After(since) {
			n++
		}
	}
	return n, nil
}

var toolUseBlock = []byte(`"type":"tool_use"`)

// ListInterrupted matches the ListInterruptedConversations query.
func (s *Store) ListInterrupted(ctx context.Context, staleBefore time.Time) ([]agent.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "ListInterrupted"); err != nil {
		return nil, err
	}
	var out []agent.Conversation
	for _, r := range s.convs {
		msgs := s.msgs[r.ID]
		if r.Status != agent.StatusOpen || !r.updatedAt.Before(staleBefore) || len(msgs) == 0 {
			continue
		}
		last := msgs[len(msgs)-1]
		if last.Role == agent.RoleUser || bytes.Contains(last.Content, toolUseBlock) {
			out = append(out, r.Conversation)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CloseIdleConversations matches the CloseIdleConversationsBefore query.
func (s *Store) CloseIdleConversations(ctx context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "CloseIdleConversations"); err != nil {
		return 0, err
	}
	var n int64
	for _, r := range s.convs {
		if r.Status == agent.StatusOpen && r.updatedAt.Before(cutoff) {
			r.Status, r.closedAt = agent.StatusResolved, r.updatedAt
			n++
		}
	}
	return n, nil
}

// PurgeMessageContent matches the PurgeMessageContentBefore query.
func (s *Store) PurgeMessageContent(ctx context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "PurgeMessageContent"); err != nil {
		return 0, err
	}
	var n int64
	for _, r := range s.convs {
		if r.Status == agent.StatusOpen || !r.closedAt.Before(cutoff) {
			continue
		}
		for i := range s.msgs[r.ID] {
			if m := &s.msgs[r.ID][i]; m.Content != nil {
				m.Content = nil
				n++
			}
		}
	}
	return n, nil
}

// FailWith makes the named method return err instead of ErrStoreDown.
func (s *Store) FailWith(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failErr == nil {
		s.failErr = map[string]error{}
	}
	s.failErr[method] = err
}

// Heal undoes FailOn and FailWith for the named method.
func (s *Store) Heal(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failOn, method)
	delete(s.failErr, method)
}

func (s *Store) find(id int64) *row {
	for _, r := range s.convs {
		if r.ID == id {
			return r
		}
	}
	return nil
}

// Conversation returns a conversation by thread, for assertions.
func (s *Store) Conversation(channelID, threadTS string) (agent.Conversation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.convs {
		if r.ChannelID == channelID && r.ThreadTS == threadTS {
			return r.Conversation, true
		}
	}
	return agent.Conversation{}, false
}

// Conversations is how many conversation rows exist.
func (s *Store) Conversations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.convs)
}

// Messages returns a conversation's stored messages.
func (s *Store) Messages(id int64) []agent.StoredMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.StoredMessage(nil), s.msgs[id]...)
}

// Usage returns every llm_usage row.
func (s *Store) Usage() []agent.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.Usage(nil), s.usage...)
}

// Seed adds a conversation as it would be after earlier turns, updated at
// the given time, with the given messages.
func (s *Store) Seed(c agent.Conversation, updatedAt time.Time, msgs ...agent.StoredMessage) agent.Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.ID = int64(len(s.convs) + 1)
	c.LastSeq = len(msgs)
	if c.Status == "" {
		c.Status = agent.StatusOpen
	}
	s.convs = append(s.convs, &row{Conversation: c, createdAt: updatedAt, updatedAt: updatedAt})
	for i, m := range msgs {
		m.Seq = i + 1
		s.msgs[c.ID] = append(s.msgs[c.ID], m)
	}
	return c
}

// SeedUsage adds an llm_usage row.
func (s *Store) SeedUsage(u agent.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, u)
}

// GetConversation returns a conversation by id.
func (s *Store) GetConversation(ctx context.Context, id int64) (agent.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "GetConversation"); err != nil {
		return agent.Conversation{}, err
	}
	r := s.find(id)
	if r == nil {
		return agent.Conversation{}, errors.New("agenttest: no conversation")
	}
	return r.Conversation, nil
}

// AppendToolResultWithCard is all or nothing, like the transaction; the
// card insert does nothing for an approval that already has one.
func (s *Store) AppendToolResultWithCard(ctx context.Context, id int64, content json.RawMessage, card agent.ApprovalCard) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "AppendToolResultWithCard"); err != nil {
		return false, err
	}
	if err := s.append(id, agent.RoleUser, content); err != nil {
		return false, err
	}
	if s.card(card.ApprovalID) != nil {
		return false, nil
	}
	card.ConversationID = id
	s.cards = append(s.cards, &cardRow{ApprovalCard: card})
	return true, nil
}

// RecordCardPostAttempt claims an attempt like the SQL does.
func (s *Store) RecordCardPostAttempt(ctx context.Context, id uuid.UUID, maxAttempts int, spacing time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "RecordCardPostAttempt"); err != nil {
		return false, err
	}
	c := s.card(id)
	if c == nil || c.CardTS != "" || c.Settled || c.PostAttempts >= maxAttempts ||
		(!c.lastPostAt.IsZero() && !c.lastPostAt.Before(s.Now().Add(-spacing))) {
		return false, nil
	}
	c.PostAttempts++
	c.lastPostAt = s.Now()
	return true, nil
}

// SetCardPosted stores the card's ts once and fills an empty channel and
// owner Slack id, like the SQL.
func (s *Store) SetCardPosted(ctx context.Context, id uuid.UUID, ts, channel, approverSlackID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "SetCardPosted"); err != nil {
		return err
	}
	if c := s.card(id); c != nil && c.CardTS == "" {
		c.CardTS = ts
		c.ChannelID = cmp.Or(c.ChannelID, channel)
		c.ApproverSlackID = cmp.Or(c.ApproverSlackID, approverSlackID)
	}
	return nil
}

// GetApprovalCard returns the card, or agent.ErrNoCard.
func (s *Store) GetApprovalCard(ctx context.Context, id uuid.UUID) (agent.ApprovalCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "GetApprovalCard"); err != nil {
		return agent.ApprovalCard{}, err
	}
	if c := s.card(id); c != nil {
		return c.ApprovalCard, nil
	}
	return agent.ApprovalCard{}, agent.ErrNoCard
}

// SettleCard settles once and moves an open conversation, like the transaction.
func (s *Store) SettleCard(ctx context.Context, id uuid.UUID, outcome, status string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "SettleCard"); err != nil {
		return false, err
	}
	c := s.card(id)
	if c == nil || c.Settled {
		return false, nil
	}
	c.Settled, c.Outcome = true, outcome
	if _, err := s.setStatus(c.ConversationID, agent.StatusChange{Status: status}); err != nil && !errors.Is(err, agent.ErrNotOpen) {
		return false, err
	}
	return true, nil
}

// AdoptApprovalCard inserts a card row for the conversation that owns the
// thread, like the SQL; nothing for an unknown thread or a known approval.
func (s *Store) AdoptApprovalCard(ctx context.Context, card agent.ApprovalCard, threadChannel, threadTS string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "AdoptApprovalCard"); err != nil {
		return false, err
	}
	if s.card(card.ApprovalID) != nil {
		return false, nil
	}
	for _, r := range s.convs {
		if r.ChannelID == threadChannel && r.ThreadTS == threadTS {
			card.ConversationID = r.ID
			s.cards = append(s.cards, &cardRow{ApprovalCard: card})
			return true, nil
		}
	}
	return false, nil
}

// HaveApprovalCards reports which ids have a card row, like the SQL.
func (s *Store) HaveApprovalCards(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "HaveApprovalCards"); err != nil {
		return nil, err
	}
	have := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if s.card(id) != nil {
			have[id] = true
		}
	}
	return have, nil
}

// ListUnsettledApprovalCards lists unsettled cards in insertion order.
func (s *Store) ListUnsettledApprovalCards(ctx context.Context, limit int) ([]agent.ApprovalCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(ctx, "ListUnsettledApprovalCards"); err != nil {
		return nil, err
	}
	var out []agent.ApprovalCard
	for _, c := range s.cards {
		if !c.Settled && len(out) < limit {
			out = append(out, c.ApprovalCard)
		}
	}
	return out, nil
}

// Cards returns every card row.
func (s *Store) Cards() []agent.ApprovalCard {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agent.ApprovalCard, 0, len(s.cards))
	for _, c := range s.cards {
		out = append(out, c.ApprovalCard)
	}
	return out
}

func (s *Store) card(id uuid.UUID) *cardRow {
	for _, c := range s.cards {
		if c.ApprovalID == id {
			return c
		}
	}
	return nil
}
