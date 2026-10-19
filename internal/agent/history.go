package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
)

// errHistory means the stored history cannot be sent to the model (AC-5).
var errHistory = errors.New("agent: the stored history is not a valid tool use sequence")

// userText is the user's message as stored: one text block.
func userText(text string) []anthropic.ContentBlockParamUnion {
	return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(text)}
}

// encodeBlocks stores content as a JSON array of content blocks, written
// from the SDK's param types, so a stored history goes back as is.
func encodeBlocks(blocks []anthropic.ContentBlockParamUnion) (json.RawMessage, error) {
	raw, err := json.Marshal(blocks)
	if err != nil {
		return nil, fmt.Errorf("encode content blocks: %w", err)
	}
	return raw, nil
}

// buildHistory turns stored messages into request messages and checks that
// every tool_use is answered by a tool_result in the next message.
func buildHistory(stored []StoredMessage) ([]anthropic.MessageParam, error) {
	msgs := make([]anthropic.MessageParam, 0, len(stored))
	for _, m := range stored {
		if m.Content == nil {
			return nil, errHistory
		}
		var blocks []anthropic.ContentBlockParamUnion
		if err := json.Unmarshal(m.Content, &blocks); err != nil || len(blocks) == 0 {
			return nil, errHistory
		}
		switch m.Role {
		case RoleUser:
			msgs = append(msgs, anthropic.MessageParam{Role: anthropic.MessageParamRoleUser, Content: blocks})
		case RoleAssistant:
			msgs = append(msgs, anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant, Content: blocks})
		default:
			return nil, errHistory
		}
	}
	if len(msgs) == 0 || msgs[0].Role != anthropic.MessageParamRoleUser {
		return nil, errHistory
	}
	if err := checkToolPairs(msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

// checkToolPairs requires a tool_result for each tool_use in the next message.
func checkToolPairs(msgs []anthropic.MessageParam) error {
	for i, m := range msgs {
		var ids []string
		for _, b := range m.Content {
			if b.OfToolUse != nil {
				ids = append(ids, b.OfToolUse.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if m.Role != anthropic.MessageParamRoleAssistant || i+1 >= len(msgs) || msgs[i+1].Role != anthropic.MessageParamRoleUser {
			return errHistory
		}
		answered := map[string]bool{}
		for _, b := range msgs[i+1].Content {
			if b.OfToolResult != nil {
				answered[b.OfToolResult.ToolUseID] = true
			}
		}
		for _, id := range ids {
			if !answered[id] {
				return errHistory
			}
		}
	}
	return nil
}

// toolResultText is Result.Text, a newline, and the compact JSON of
// Result.Data when it is not empty (AC-5).
func toolResultText(r mcpclient.Result) string {
	if len(r.Data) == 0 {
		return r.Text
	}
	data, err := json.Marshal(r.Data)
	if err != nil {
		return r.Text
	}
	return r.Text + "\n" + string(data)
}

// toolUse is one tool_use block from a response.
type toolUse struct {
	id    string
	name  string
	input map[string]any
}

// toolUses lists a response's tool_use blocks in order. The model's input is
// passed through only as the tool's arguments (AC-18).
func toolUses(resp *anthropic.Message) []toolUse {
	var out []toolUse
	for _, b := range resp.Content {
		if b.Type != "tool_use" {
			continue
		}
		var input map[string]any
		if len(b.Input) > 0 {
			_ = json.Unmarshal(b.Input, &input) // a non object input runs with no arguments; the tool server checks the schema
		}
		out = append(out, toolUse{id: b.ID, name: b.Name, input: input})
	}
	return out
}

// responseText joins a response's text blocks.
func responseText(resp *anthropic.Message) string {
	var text string
	for _, b := range resp.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return text
}

// assistantContent is the response as param blocks, ready to store.
func assistantContent(resp *anthropic.Message) []anthropic.ContentBlockParamUnion {
	return resp.ToParam().Content
}
