package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/google/uuid"
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
	ID                string
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

// SlackUsers is users.info and users.lookupByEmail with the bot token.
type SlackUsers interface {
	UserInfo(ctx context.Context, userID string) (SlackUser, error)
	// LookupByEmail finds a group owner's Slack account (spec 0013, AC-10).
	// No account with that email is ErrSlackUserNotFound; any other error
	// is worth trying again.
	LookupByEmail(ctx context.Context, email string) (SlackUser, error)
}

// ErrSlackUserNotFound means users.lookupByEmail found nobody
// (users_not_found).
var ErrSlackUserNotFound = errors.New("agent: no Slack user has that email")

// Poster is chat.postMessage in a thread with the bot token.
type Poster interface {
	Reply(ctx context.Context, channel, threadTS, text string) error
	// ReplyNoUnfurl posts with link and media unfurling off (spec 0011, AC-5).
	ReplyNoUnfurl(ctx context.Context, channel, threadTS, text string) error
	// PostCard posts an approval card as a new message and returns the
	// channel it landed in and its ts (spec 0012, AC-7). A Slack user id as
	// channel posts to that person's DM, whose channel id comes back (spec
	// 0013, AC-10).
	PostCard(ctx context.Context, channel string, c Card) (postedChannel, ts string, err error)
	// UpdateCard replaces a posted card (chat.update, spec 0012 AC-16).
	UpdateCard(ctx context.Context, channel, ts string, c Card) error
	// Ephemeral shows text to one user in a channel (chat.postEphemeral).
	Ephemeral(ctx context.Context, channel, userID, text string) error
}

// Card is an approval card (spec 0012, AC-7): fixed lines built only by
// code, with no model text, and the Approve and Deny buttons while it can
// still be decided. The Slack adapter renders it as Block Kit.
type Card struct {
	Fallback string   // the message's text, for notifications
	Lines    []string // mrkdwn lines, every interpolated value already escaped
	// ApprovalID is the buttons' value; empty renders no buttons.
	ApprovalID string
	// Confirm is the Approve button's confirm dialog text, plain text.
	Confirm string
}

// Card action ids (spec 0012, AC-7 and AC-8).
const (
	ActionApprove = "approval_approve"
	ActionDeny    = "approval_deny"
)

// Click is one Approve or Deny click from the IT channel (spec 0012, AC-8).
// UserID comes from the interaction payload, never from the model.
type Click struct {
	EventID    string // the envelope id, for logs
	ApprovalID uuid.UUID
	UserID     string
	Approve    bool
	ChannelID  string
}

// Approvals are the tool server's approval routes (spec 0012, AC-9 to
// AC-14; spec 0013 adds the clicker's email and Cancel).
// *mcpclient.ApprovalClient satisfies it.
type Approvals interface {
	Decide(ctx context.Context, id uuid.UUID, clicker, email string, approve bool) (mcpclient.DecisionResponse, error)
	// Cancel ends a pending group owner approval whose owner cannot be
	// reached (spec 0013, AC-11).
	Cancel(ctx context.Context, id uuid.UUID) (mcpclient.ApprovalView, error)
	Get(ctx context.Context, id uuid.UUID) (mcpclient.ApprovalView, error)
	// Since lists the approvals created at or after since (the tick's adopt).
	Since(ctx context.Context, since time.Time) ([]mcpclient.ApprovalView, error)
	// Expire ends overdue pending approvals and abandons stuck approved ones.
	Expire(ctx context.Context) (mcpclient.ExpireResponse, error)
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
	GetConversation(ctx context.Context, id int64) (Conversation, error)
	RetentionStore
	ApprovalStore
}

// ApprovalCard is one approval_cards row (spec 0012): the card agent code
// owes IT, and the outcome it owes the user.
type ApprovalCard struct {
	ApprovalID     uuid.UUID
	ConversationID int64
	ToolName       string
	// ChannelID is where the card goes: SLACK_IT_CHANNEL for an IT card;
	// for a group owner card, empty until the first DM post sets the DM
	// channel (spec 0013, AC-9).
	ChannelID string
	// ApproverSlackID is the group owner's Slack id, found by email and set
	// with the first post; only that person's click counts (spec 0013, AC-13).
	ApproverSlackID string
	CardTS          string // empty until a post succeeds
	PostAttempts    int
	ExpiresAt       time.Time
	Settled         bool
	Outcome         string // executed, failed, denied, or expired once settled
}

// ErrNoCard means no approval card has that approval id.
var ErrNoCard = errors.New("agent: no approval card")

// ApprovalStore is the approval_cards table (spec 0012, AC-6 and AC-15).
type ApprovalStore interface {
	// AppendToolResultWithCard stores a pending tool result and inserts its
	// card row in one transaction. created reports whether this call made
	// the row, and so owes the first post.
	AppendToolResultWithCard(ctx context.Context, id int64, content json.RawMessage, card ApprovalCard) (created bool, err error)
	// RecordCardPostAttempt claims one post attempt before it is sent: only
	// while no post succeeded, under maxAttempts, and spacing after the
	// last attempt. ok is false when the claim did not happen.
	RecordCardPostAttempt(ctx context.Context, approvalID uuid.UUID, maxAttempts int, spacing time.Duration) (ok bool, err error)
	// SetCardPosted stores the first successful post: its ts and, when
	// still empty, the channel and the owner's Slack id (spec 0013, AC-10).
	SetCardPosted(ctx context.Context, approvalID uuid.UUID, ts, channel, approverSlackID string) error
	// GetApprovalCard returns ErrNoCard for an unknown id.
	GetApprovalCard(ctx context.Context, approvalID uuid.UUID) (ApprovalCard, error)
	// SettleCard records the outcome once and, when the conversation is
	// still open, moves it to status, in one transaction. settled is false
	// when another settle won.
	SettleCard(ctx context.Context, approvalID uuid.UUID, outcome, status string) (settled bool, err error)
	// AdoptApprovalCard inserts the card row of an approval whose pending
	// result never reached the agent, when its thread (threadChannel,
	// threadTS) is a known conversation (AC-14). created is false when the
	// row exists already or the thread is unknown.
	AdoptApprovalCard(ctx context.Context, card ApprovalCard, threadChannel, threadTS string) (created bool, err error)
	// HaveApprovalCards reports which of ids already have a card row, so the
	// adopt step inserts only for the rest (AC-14).
	HaveApprovalCards(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error)
	// ListUnsettledApprovalCards lists up to limit cards whose outcome is
	// still owed, oldest first (AC-14).
	ListUnsettledApprovalCards(ctx context.Context, limit int) ([]ApprovalCard, error)
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
