package runner_test

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toyinogun/AIITsupport/eval/runner"
)

func tool(name, desc string, schema map[string]any) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: desc, InputSchema: schema}
}

// covers: AC-14
func TestToolsHash(t *testing.T) {
	a := tool("a", "first", map[string]any{"type": "object", "properties": map[string]any{"x": 1}})
	b := tool("b", "second", map[string]any{"type": "object"})
	base, err := runner.ToolsHash([]*mcp.Tool{a, b})
	if err != nil {
		t.Fatal(err)
	}
	swapped, _ := runner.ToolsHash([]*mcp.Tool{b, a})
	if base != swapped {
		t.Error("the hash depends on the order the server listed the tools")
	}
	for name, changed := range map[string][]*mcp.Tool{
		"description": {tool("a", "first.", a.InputSchema.(map[string]any)), b},
		"schema":      {tool("a", "first", map[string]any{"type": "object"}), b},
		"name":        {tool("c", "first", a.InputSchema.(map[string]any)), b},
	} {
		got, _ := runner.ToolsHash(changed)
		if got == base {
			t.Errorf("changing the %s did not change the hash", name)
		}
	}
}

// covers: AC-14
func TestPromptHashChangesWithText(t *testing.T) {
	if runner.HashText("a") == runner.HashText("b") || runner.PromptHash() == "" {
		t.Fatal("hash does not follow the text")
	}
}
