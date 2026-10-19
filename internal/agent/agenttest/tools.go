package agenttest

import (
	"context"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// ErrNoAnswer is a tool server that did not answer (a transport error).
var ErrNoAnswer = errors.New("agenttest: the tool server did not answer")

// ToolCall is one recorded call.
type ToolCall struct {
	Requester mcpclient.Requester
	Tool      string
	Args      map[string]any
}

// Handler answers one tool call.
type Handler func(ctx context.Context, c ToolCall) (mcpclient.Result, error)

// Tools is a fake tool server. Handlers answer by tool name; a tool with no
// handler gets ErrNoAnswer.
type Tools struct {
	mu       sync.Mutex
	Handlers map[string]Handler
	ListErr  error
	calls    []ToolCall
	lists    int
}

// NewTools returns a tool server where the requester's account username is
// locked or not, and every other tool succeeds.
func NewTools(username string, locked bool) *Tools {
	return &Tools{Handlers: map[string]Handler{
		"user_lookup":    Respond(LookupResult(username, locked)),
		"account_unlock": Respond(OK("Account unlocked and confirmed.", map[string]any{"was_locked": locked, "locked": false})),
		"ticket_create":  Respond(OK("Ticket created.", map[string]any{"ticket_id": float64(42), "ticket_number": "10042"})),
		"ticket_update": func(_ context.Context, c ToolCall) (mcpclient.Result, error) {
			state, _ := c.Args["state"].(string)
			if state == "" {
				state = "open"
			}
			return OK("Ticket updated.", map[string]any{"ticket_id": float64(c.Requester.TicketID), "state": state}), nil
		},
		"privileged_access_change": Respond(Refused("blocked", "Privileged changes are never made by the assistant. Escalate to IT.")),
	}}
}

// Respond always returns r.
func Respond(r mcpclient.Result) Handler {
	return func(context.Context, ToolCall) (mcpclient.Result, error) { return r, nil }
}

// NoAnswer is a handler for a tool server that never answers.
func NoAnswer() Handler {
	return func(context.Context, ToolCall) (mcpclient.Result, error) { return mcpclient.Result{}, ErrNoAnswer }
}

// OK is a successful result.
func OK(text string, data map[string]any) mcpclient.Result {
	return mcpclient.Result{Text: text, Data: data}
}

// Refused is a refusal or failure with its code.
func Refused(code, text string) mcpclient.Result {
	return mcpclient.Result{Text: text, Data: map[string]any{"code": code}, IsError: true, Code: code}
}

// LookupResult is user_lookup's answer for a matched requester.
func LookupResult(username string, locked bool) mcpclient.Result {
	var endsAt any
	if locked {
		endsAt = "2026-10-19T09:30:00Z"
	}
	return OK("Looked up the requester's account.", map[string]any{
		"username": username, "display_name": "Jan de Vries", "department": "Operations",
		"enabled": true, "locked": locked, "lock_ends_at": endsAt,
	})
}

// Call runs the tool's handler and records the call.
func (t *Tools) Call(ctx context.Context, r mcpclient.Requester, tool string, args map[string]any) (mcpclient.Result, error) {
	c := ToolCall{Requester: r, Tool: tool, Args: args}
	t.mu.Lock()
	t.calls = append(t.calls, c)
	h := t.Handlers[tool]
	t.mu.Unlock()
	if h == nil {
		return mcpclient.Result{}, ErrNoAnswer
	}
	return h(ctx, c)
}

// ListTools returns the production tool names with simple schemas.
func (t *Tools) ListTools(context.Context) ([]*mcp.Tool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lists++
	if t.ListErr != nil {
		return nil, t.ListErr
	}
	empty := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	text := func(names ...string) map[string]any {
		props := map[string]any{}
		for _, n := range names {
			props[n] = map[string]any{"type": "string"}
		}
		return map[string]any{"type": "object", "properties": props, "required": []any{names[0]}, "additionalProperties": false}
	}
	return []*mcp.Tool{
		{Name: "user_lookup", Description: "Look up the requester's own account.", InputSchema: empty},
		{Name: "account_unlock", Description: "Unlock the requester's own account.", InputSchema: empty},
		{Name: "ticket_create", Description: "Open this conversation's ticket.", InputSchema: text("title", "body")},
		{Name: "ticket_update", Description: "Update this conversation's ticket.", InputSchema: text("note", "state", "pending_until")},
		{Name: "privileged_access_change", Description: "Always refused.", InputSchema: text("summary")},
	}, nil
}

// Calls returns every call so far.
func (t *Tools) Calls() []ToolCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]ToolCall(nil), t.calls...)
}

// Names returns the tool name of every call so far, in order.
func (t *Tools) Names() []string {
	var out []string
	for _, c := range t.Calls() {
		out = append(out, c.Tool)
	}
	return out
}

// Lists is how many tools/list calls were made.
func (t *Tools) Lists() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lists
}

// Set replaces one tool's handler.
func (t *Tools) Set(tool string, h Handler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Handlers[tool] = h
}
