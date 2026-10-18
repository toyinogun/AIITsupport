// Package mcpclient is the agent's thin client for the tool server (spec
// 0007). It can only send identity from agent code: Call takes a Requester
// the agent builds from Slack, and sets every _meta key from it, so the
// model can only ever fill tool arguments. It holds the tool server's bearer
// token, never a client system credential.
package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The _meta keys the tool server reads identity from (spec 0007, AC-2).
const (
	MetaSlackID   = "noord.example/requester_slack_id"
	MetaEmail     = "noord.example/requester_email"
	MetaChannelID = "noord.example/channel_id"
	MetaThreadTS  = "noord.example/thread_ts"
	MetaTicketID  = "noord.example/ticket_id"
)

// clientName is how the agent introduces itself in MCP initialize.
const clientName = "noord-agent"

// ErrIncompleteRequester means a Requester lacks the Slack id, email,
// channel, or thread; Call sends nothing.
var ErrIncompleteRequester = errors.New("mcpclient: the requester needs a Slack id, email, channel, and thread")

// Requester is who is asking and where, built by agent code from Slack.
type Requester struct {
	SlackID   string
	Email     string
	ChannelID string
	ThreadTS  string
	TicketID  int64 // the thread's ticket, 0 before ticket_create
}

func (r Requester) meta() (mcp.Meta, error) {
	if r.SlackID == "" || r.Email == "" || r.ChannelID == "" || r.ThreadTS == "" {
		return nil, ErrIncompleteRequester
	}
	m := mcp.Meta{MetaSlackID: r.SlackID, MetaEmail: r.Email, MetaChannelID: r.ChannelID, MetaThreadTS: r.ThreadTS}
	if r.TicketID != 0 {
		m[MetaTicketID] = r.TicketID
	}
	return m, nil
}

// Result is a tool's answer. A refusal or failure is a Result with IsError
// true and its Code, not a Go error: the model reads it and decides.
type Result struct {
	Data    map[string]any
	Text    string
	IsError bool
	Code    string
}

// Client calls the tool server. The server is stateless, so each Call
// connects, initializes, calls, and closes.
type Client struct {
	endpoint string
	http     *http.Client
}

// New returns a client for the tool server's MCP endpoint (TOOLSERVER_URL)
// that sends token (TOOLSERVER_TOKEN) on every request.
func New(endpoint, token string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("mcpclient: TOOLSERVER_URL must be an http or https URL with a host")
	}
	if token == "" {
		return nil, errors.New("mcpclient: no TOOLSERVER_TOKEN")
	}
	return &Client{endpoint: endpoint, http: &http.Client{
		Transport: bearer{token: token, next: http.DefaultTransport},
		// A redirect would send the token somewhere we did not choose.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Call runs one tool, bounded by ctx. Identity goes only in _meta, from r.
func (c *Client) Call(ctx context.Context, r Requester, tool string, args map[string]any) (Result, error) {
	meta, err := r.meta()
	if err != nil {
		return Result{}, err
	}
	session, err := c.connect(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = session.Close() }()

	params := &mcp.CallToolParams{Meta: meta, Name: tool}
	if args != nil {
		params.Arguments = args
	}
	res, err := session.CallTool(ctx, params)
	if err != nil {
		return Result{}, fmt.Errorf("call tool %s: %w", tool, err)
	}
	return toResult(res), nil
}

// ListTools returns the tools the server offers.
func (c *Client) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	session, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close() }()
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return res.Tools, nil
}

func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             c.endpoint,
		HTTPClient:           c.http,
		MaxRetries:           -1, // never resend a call that may have written
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to the tool server: %w", err)
	}
	return session, nil
}

func toResult(res *mcp.CallToolResult) Result {
	out := Result{IsError: res.IsError}
	var texts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, t.Text)
		}
	}
	out.Text = strings.Join(texts, "\n")
	if data, ok := res.StructuredContent.(map[string]any); ok {
		out.Data = data
		if code, ok := data["code"].(string); ok {
			out.Code = code
		}
	}
	return out
}

// bearer adds the tool server token to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(req)
}
