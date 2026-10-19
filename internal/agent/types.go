package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toyinogun/AIITsupport/eval"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// EventAppMention is Inbound.EventType for an @mention in a channel; the
// listener sends "message.im" for a DM.
const EventAppMention = "app_mention"

// Inbound is one Slack message the listener accepted (filtered and deduped).
type Inbound struct {
	EventID   string
	EventType string // app_mention or message.im
	ChannelID string
	ThreadTS  string // the thread, or the message's own ts when it starts one
	TS        string // the message's own ts
	UserID    string // the sender
	Text      string
}

// SlackUser is what users.info says about the sender: the gate fields and
// the profile email (AC-3).
type SlackUser struct {
	TeamID            string
	Deleted           bool
	IsBot             bool
	IsAppUser         bool
	IsRestricted      bool
	IsUltraRestricted bool
	IsStranger        bool
	IsEmailConfirmed  bool
	Email             string
}

// SlackUsers is users.info with the bot token.
type SlackUsers interface {
	UserInfo(ctx context.Context, userID string) (SlackUser, error)
}

// Poster is chat.postMessage in a thread with the bot token.
type Poster interface {
	Reply(ctx context.Context, channel, threadTS, text string) error
}

// Tools is the tool server, through internal/mcpclient (spec 0007).
// *mcpclient.Client satisfies it.
type Tools interface {
	Call(ctx context.Context, r mcpclient.Requester, tool string, args map[string]any) (mcpclient.Result, error)
	ListTools(ctx context.Context) ([]*mcp.Tool, error)
}

// Model is the Messages API. The SDK client's &client.Messages satisfies it.
type Model interface {
	New(ctx context.Context, params anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error)
}

// Conversation statuses (spec 0003).
const (
	StatusOpen      = "open"
	StatusResolved  = "resolved"
	StatusEscalated = "escalated"
)

// Message roles as stored.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Conversation is one Slack thread's conversation row.
type Conversation struct {
	ID               int64
	ChannelID        string
	ThreadTS         string
	RequesterSlackID string
	DirectoryAccount string // empty until the first match
	TicketID         int64  // 0 until ticket_create
	Status           string
	LastSeq          int // 0 when nothing is stored yet
}

// StoredMessage is one stored message. Content is a JSON array of content
// blocks, nil when purged (#12).
type StoredMessage struct {
	Seq     int
	Role    string
	Content json.RawMessage
}

// StatusChange is a SetConversationStatus call. Empty account and zero
// ticket keep the stored values.
type StatusChange struct {
	Status           string
	DirectoryAccount string
	TicketID         int64
}

// Usage is one model call's llm_usage row.
type Usage struct {
	ConversationID int64
	Model          string
	Tokens         eval.Tokens
	CostMicroUSD   int64
	PricingDate    time.Time
}

// ErrNotOpen means a status change found the conversation no longer open.
var ErrNotOpen = errors.New("agent: the conversation is not open")

// Store is the agent's Postgres. NewStore returns the real one.
type Store interface {
	UpsertConversation(ctx context.Context, channelID, threadTS, requesterSlackID string) (Conversation, error)
	// SetStatus changes an open conversation; ErrNotOpen otherwise.
	SetStatus(ctx context.Context, id int64, ch StatusChange) (Conversation, error)
	// SetTicket stores a ticket id on a conversation in any status, unless
	// one is already stored (the restart sweep, AC-14).
	SetTicket(ctx context.Context, id, ticketID int64) error
	AppendMessage(ctx context.Context, id int64, role string, content json.RawMessage) error
	// AppendToolResult stores a tool result message and, when ch is not nil,
	// the status change it causes, in one transaction (AC-6). If the
	// conversation is no longer open it keeps the message and returns ErrNotOpen.
	AppendToolResult(ctx context.Context, id int64, content json.RawMessage, ch *StatusChange) error
	ListMessages(ctx context.Context, id int64) ([]StoredMessage, error)
	InsertUsage(ctx context.Context, u Usage) error
	SumTokens(ctx context.Context, id int64) (int64, error)
	CountRecentConversations(ctx context.Context, requesterSlackID string, since time.Time) (int64, error)
	ListInterrupted(ctx context.Context, staleBefore time.Time) ([]Conversation, error)
	RetentionStore
}

// RetentionStore is what the retention pass writes (spec 0010, AC-8).
type RetentionStore interface {
	// CloseIdleConversations makes open conversations with no activity
	// since cutoff resolved, closed at their last activity.
	CloseIdleConversations(ctx context.Context, cutoff time.Time) (int64, error)
	// PurgeMessageContent clears the stored text of conversations that
	// ended before cutoff. Open conversations are never touched.
	PurgeMessageContent(ctx context.Context, cutoff time.Time) (int64, error)
}
