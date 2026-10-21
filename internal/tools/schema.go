package tools

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Field is one string argument, or, with List, a list of strings (spec
// 0013). Bounds are counted in runes, per item for a list. The bounds and
// enums are code constants, not settings: they are part of the contract the
// model sees, so they change through code review.
type Field struct {
	Name        string
	Description string
	Required    bool
	MinRunes    int
	MaxRunes    int
	Enum        []string
	// NoNewline refuses \n (a title is one line). Every field refuses other
	// control characters except \t, and a value that is only whitespace.
	NoNewline bool
	// FreeText marks text the model writes (a title, a note). It goes only to
	// Zammad; the registry refuses to store it in the audit log.
	FreeText bool
	// List makes the field a JSON array of MinItems to MaxItems strings.
	// Each item is trimmed, then held to the string rules above.
	List               bool
	MinItems, MaxItems int
}

// Lists are a call's list arguments after validation, items trimmed.
type Lists map[string][]string

// CheckEnv is what a tool's Check rules may read.
type CheckEnv struct {
	Now        time.Time
	MaxPending time.Duration
	Initiator  string // the call's _meta initiator, InitiatorCode or empty
}

// invalid says which schema field failed; empty when the failure has no
// field name the model may see (a key the schema does not know).
type invalid struct{ field string }

// parseArgs checks raw against the tool's fields and Check rules. The
// returned field name is safe to show: it comes from the schema, never from
// the request.
func (t *Tool) parseArgs(raw []byte, env CheckEnv) (Args, Lists, *invalid) {
	args, lists := Args{}, Lists{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, nil, &invalid{}
	}
	for key := range obj {
		if !slices.ContainsFunc(t.Fields, func(f Field) bool { return f.Name == key }) {
			return nil, nil, &invalid{}
		}
	}
	for _, f := range t.Fields {
		value, present := obj[f.Name]
		if !present {
			if f.Required {
				return nil, nil, &invalid{field: f.Name}
			}
			continue
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, nil, &invalid{field: f.Name}
		}
		if f.List {
			items, ok := f.acceptsList(value)
			if !ok {
				return nil, nil, &invalid{field: f.Name}
			}
			lists[f.Name] = items
			continue
		}
		var s string
		if json.Unmarshal(value, &s) != nil || !f.accepts(s) {
			return nil, nil, &invalid{field: f.Name}
		}
		args[f.Name] = s
	}
	if t.Check != nil {
		if field, ok := t.Check(args, env); !ok {
			return nil, nil, &invalid{field: field}
		}
	}
	return args, lists, nil
}

// acceptsList decodes a list field: an array of MinItems to MaxItems
// strings, each one trimmed and then accepted by the string rules.
func (f Field) acceptsList(value json.RawMessage) ([]string, bool) {
	var items []string
	if json.Unmarshal(value, &items) != nil || len(items) < f.MinItems || len(items) > f.MaxItems {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if !f.accepts(item) {
			return nil, false
		}
		out = append(out, item)
	}
	return out, true
}

func (f Field) accepts(s string) bool {
	n := utf8.RuneCountInString(s)
	if n < f.MinRunes || (f.MaxRunes > 0 && n > f.MaxRunes) || strings.TrimSpace(s) == "" {
		return false
	}
	if len(f.Enum) > 0 && !slices.Contains(f.Enum, s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '\t':
		case r == '\n':
			if f.NoNewline {
				return false
			}
		case unicode.IsControl(r):
			return false
		}
	}
	return true
}

// InputSchema is the JSON schema tools/list shows: an object with exactly
// these string properties and no others.
func (t *Tool) InputSchema() map[string]any {
	props := map[string]any{}
	var required []string
	for _, f := range t.Fields {
		p := map[string]any{"type": "string"}
		if f.MinRunes > 0 {
			p["minLength"] = f.MinRunes
		}
		if f.MaxRunes > 0 {
			p["maxLength"] = f.MaxRunes
		}
		if len(f.Enum) > 0 {
			p["enum"] = slices.Clone(f.Enum)
		}
		if f.List {
			p = map[string]any{"type": "array", "items": p, "minItems": f.MinItems, "maxItems": f.MaxItems}
		}
		if f.Description != "" {
			p["description"] = f.Description
		}
		props[f.Name] = p
		if f.Required {
			required = append(required, f.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
