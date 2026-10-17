package audit

import (
	"encoding/json"
	"fmt"
	"slices"
)

// omitted replaces the value of every key a tool does not allow, so the row
// still shows the key existed without holding its value.
const omitted = "[omitted]"

// encodeKept keeps the allowed top level keys of data and replaces the rest
// with omitted. Nested values under an allowed key are stored whole. A
// result over maxBytes becomes {"truncated": true, "bytes": n}. A nil map
// stores SQL NULL.
func encodeKept(data map[string]any, keep []string, maxBytes int) (json.RawMessage, error) {
	if data == nil {
		return nil, nil
	}
	kept := make(map[string]any, len(data))
	for k, v := range data {
		if slices.Contains(keep, k) {
			kept[k] = v
		} else {
			kept[k] = omitted
		}
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("encode audit payload: %w", err)
	}
	if len(b) > maxBytes {
		return marker(map[string]any{"truncated": true, "bytes": len(b)}), nil
	}
	return b, nil
}

// unencodableMarker stands in for a result that json.Marshal rejects.
func unencodableMarker() json.RawMessage {
	return marker(map[string]any{"unencodable": true})
}

func marker(m map[string]any) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil { // a map of plain values always encodes
		panic(fmt.Sprintf("encode audit marker: %v", err))
	}
	return b
}

// capRunes cuts s to at most n characters, the unit Postgres char_length counts.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
