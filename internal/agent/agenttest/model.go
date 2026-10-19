package agenttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ErrScriptDone is what Model returns once its script runs out.
var ErrScriptDone = errors.New("agenttest: the scripted model has no more responses")

// Step answers one model call.
type Step func(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error)

// Model is a scripted Messages API. Each call takes the next step and
// records the request.
type Model struct {
	mu    sync.Mutex
	steps []Step
	calls []anthropic.MessageNewParams
}

// NewModel returns a model that plays steps in order.
func NewModel(steps ...Step) *Model { return &Model{steps: steps} }

// New plays the next step.
func (m *Model) New(ctx context.Context, params anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	m.mu.Lock()
	m.calls = append(m.calls, params)
	if len(m.steps) == 0 {
		m.mu.Unlock()
		return nil, ErrScriptDone
	}
	step := m.steps[0]
	m.steps = m.steps[1:]
	m.mu.Unlock()
	return step(ctx, params)
}

// Calls returns every request so far.
func (m *Model) Calls() []anthropic.MessageNewParams {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]anthropic.MessageNewParams(nil), m.calls...)
}

// Reply is a step that returns msg.
func Reply(msg *anthropic.Message) Step {
	return func(context.Context, anthropic.MessageNewParams) (*anthropic.Message, error) { return msg, nil }
}

// Fail is a step that returns err, like a call that failed after retries.
func Fail(err error) Step {
	return func(context.Context, anthropic.MessageNewParams) (*anthropic.Message, error) { return nil, err }
}

// Hang is a step that waits for ctx to end, like a call that runs past the deadline.
func Hang() Step {
	return func(ctx context.Context, _ anthropic.MessageNewParams) (*anthropic.Message, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

// Text is a text content block.
func Text(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

// Thinking is a thinking block with a signature, as the API returns one.
func Thinking() map[string]any {
	return map[string]any{"type": "thinking", "thinking": "", "signature": "sig-abc"}
}

// ToolUse is a tool_use content block.
func ToolUse(id, name string, input map[string]any) map[string]any {
	if input == nil {
		input = map[string]any{}
	}
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

// Usage are the token counts every scripted response reports.
var Usage = map[string]any{"input_tokens": 1000, "output_tokens": 100, "cache_read_input_tokens": 2000, "cache_creation_input_tokens": 500}

// Message builds a response the way the SDK decodes one from the API.
func Message(stopReason string, blocks ...map[string]any) *anthropic.Message {
	raw, err := json.Marshal(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-test",
		"content": blocks, "stop_reason": stopReason, "usage": Usage,
	})
	if err != nil {
		panic(fmt.Sprintf("agenttest: encode message: %v", err))
	}
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic(fmt.Sprintf("agenttest: decode message: %v", err))
	}
	return &msg
}

// CallTool is a step that asks for one tool.
func CallTool(id, name string, input map[string]any) Step {
	return Reply(Message("tool_use", Thinking(), ToolUse(id, name, input)))
}

// Answer is a step that ends the turn with text.
func Answer(text string) Step {
	return Reply(Message("end_turn", Text(text)))
}
