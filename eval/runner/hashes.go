package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/toyinogun/AIITsupport/internal/agent/prompt"
)

// HashText is the hex sha256 of s.
func HashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// PromptHash hashes the embedded system prompt (AC-14).
func PromptHash() string { return HashText(prompt.System) }

// ToolsHash hashes the tool server's tools/list result: names, descriptions,
// and input schemas, in name order (AC-14).
func ToolsHash(tools []*mcp.Tool) (string, error) {
	type entry struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema any    `json:"input_schema"`
	}
	entries := make([]entry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, entry{t.Name, t.Description, t.InputSchema})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode the tool list: %w", err)
	}
	return HashText(string(raw)), nil
}
