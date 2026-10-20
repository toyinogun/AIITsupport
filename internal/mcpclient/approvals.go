package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// The approval routes beside /mcp on the tool server (spec 0012, AC-10 to
// AC-13). They are plain JSON over the same bearer token, never MCP tools,
// so the model has no way to reach them.
const (
	ApprovalsPath = "/approvals"
	ExpirePath    = "/approvals/expire"
)

// Refusal tokens in a DecisionResponse's Reason (spec 0012, API surface).
const (
	ReasonNotAllowed = "not_allowed"
	ReasonSelf       = "self"
	ReasonExpired    = "expired"
	ReasonDecided    = "decided"
)

// Approval statuses as the tool server reports them (spec 0003).
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDenied   = "denied"
	StatusExpired  = "expired"
	StatusExecuted = "executed"
	StatusFailed   = "failed"
)

// ApprovalView is the approval as the tool server shows it (AC-13).
type ApprovalView struct {
	ID               uuid.UUID          `json:"id"`
	ToolName         string             `json:"tool_name"`
	Status           string             `json:"status"`
	DecidedBy        string             `json:"decided_by,omitempty"`
	DecidedAt        *time.Time         `json:"decided_at,omitempty"`
	ExpiresAt        time.Time          `json:"expires_at"`
	CreatedAt        time.Time          `json:"created_at"`
	RequesterSlackID string             `json:"requester_slack_id"`
	RequesterAccount string             `json:"requester_account"`
	SlackChannelID   string             `json:"slack_channel_id"`
	SlackThreadTS    string             `json:"slack_thread_ts"`
	TicketID         int64              `json:"ticket_id,omitempty"`
	TicketURL        string             `json:"ticket_url,omitempty"`
	Execution        *ApprovalExecution `json:"execution,omitempty"`
}

// ApprovalExecution is how an executed or failed approval ended: the outcome,
// its stored reason, and the StoreResult counts when there are any.
type ApprovalExecution struct {
	Outcome string         `json:"outcome"`
	Reason  string         `json:"reason,omitempty"`
	Result  map[string]any `json:"result,omitempty"`
}

// DecisionRequest is the body of POST /approvals/{id}/decision: exactly these
// two keys. Clicker is the Slack id from the interaction payload.
type DecisionRequest struct {
	Clicker string `json:"clicker"`
	Approve bool   `json:"approve"`
}

// DecisionResponse says whether the click moved the approval and, when it
// did not, why, with the approval as it now stands.
type DecisionResponse struct {
	Moved    bool         `json:"moved"`
	Reason   string       `json:"reason,omitempty"`
	Approval ApprovalView `json:"approval"`
}

// ExpireResponse counts what POST /approvals/expire ended.
type ExpireResponse struct {
	Expired   int `json:"expired"`
	Abandoned int `json:"abandoned"`
}

// ErrApprovalNotFound is a 404 from an approval route: no approval has that id.
var ErrApprovalNotFound = errors.New("mcpclient: approval not found")

// StatusError is a non 2xx answer from an approval route other than 404.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("mcpclient: approval route answered %d", e.Status)
}

// maxApprovalResponseBytes caps what the agent reads from an approval route.
const maxApprovalResponseBytes = 1 << 20

// ApprovalClient calls the tool server's approval routes. Its URLs are
// TOOLSERVER_URL's scheme and host plus the fixed /approvals paths, and every
// request carries the bearer token and the W3C trace context, like Call.
type ApprovalClient struct {
	base string // scheme://host
	http *http.Client
}

// NewApprovalClient builds the client from TOOLSERVER_URL (the MCP endpoint,
// whose path is dropped) and TOOLSERVER_TOKEN.
func NewApprovalClient(endpoint, token string) (*ApprovalClient, error) {
	c, err := New(endpoint, token)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(endpoint) // New parsed it
	return &ApprovalClient{base: u.Scheme + "://" + u.Host, http: c.http}, nil
}

// Decide sends one click. A refused click is a DecisionResponse with Moved
// false, not an error.
func (c *ApprovalClient) Decide(ctx context.Context, id uuid.UUID, clicker string, approve bool) (DecisionResponse, error) {
	var out DecisionResponse
	err := c.do(ctx, http.MethodPost, ApprovalsPath+"/"+id.String()+"/decision", DecisionRequest{Clicker: clicker, Approve: approve}, &out)
	return out, err
}

// Get reads one approval.
func (c *ApprovalClient) Get(ctx context.Context, id uuid.UUID) (ApprovalView, error) {
	var out ApprovalView
	err := c.do(ctx, http.MethodGet, ApprovalsPath+"/"+id.String(), nil, &out)
	return out, err
}

// Since lists approvals created at or after since, oldest first, up to 100.
func (c *ApprovalClient) Since(ctx context.Context, since time.Time) ([]ApprovalView, error) {
	var out []ApprovalView
	err := c.do(ctx, http.MethodGet, ApprovalsPath+"?since="+url.QueryEscape(since.UTC().Format(time.RFC3339)), nil, &out)
	return out, err
}

// Expire ends overdue pending approvals and abandons stuck approved ones.
func (c *ApprovalClient) Expire(ctx context.Context) (ExpireResponse, error) {
	var out ExpireResponse
	err := c.do(ctx, http.MethodPost, ExpirePath, nil, &out)
	return out, err
}

func (c *ApprovalClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode approval request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return fmt.Errorf("build approval request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("approval route: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrApprovalNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return &StatusError{Status: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxApprovalResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode approval response: %w", err)
	}
	return nil
}
