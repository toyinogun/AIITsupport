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

// Field is one string argument. Bounds are counted in runes. The bounds and
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
}

// CheckEnv is what a tool's Check rules may read.
type CheckEnv struct {
	Now        time.Time
	MaxPending time.Duration
}

// invalid says which schema field failed; empty when the failure has no
// field name the model may see (a key the schema does not know).
type invalid struct{ field string }

// parseArgs checks raw against the tool's fields and Check rules. The
// returned field name is safe to show: it comes from the schema, never from
// the request.
func (t *Tool) parseArgs(raw []byte, env CheckEnv) (Args, *invalid) {
	args := Args{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, &invalid{}
	}
	for key := range obj {
		if !slices.ContainsFunc(t.Fields, func(f Field) bool { return f.Name == key }) {
			return nil, &invalid{}
		}
	}
	for _, f := range t.Fields {
		value, present := obj[f.Name]
		if !present {
			if f.Required {
				return nil, &invalid{field: f.Name}
			}
			continue
		}
		var s string
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &s) != nil || !f.accepts(s) {
			return nil, &invalid{field: f.Name}
		}
		args[f.Name] = s
	}
	if t.Check != nil {
		if field, ok := t.Check(args, env); !ok {
			return nil, &invalid{field: field}
		}
	}
	return args, nil
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
		if f.Description != "" {
			p["description"] = f.Description
		}
		if f.MinRunes > 0 {
			p["minLength"] = f.MinRunes
		}
		if f.MaxRunes > 0 {
			p["maxLength"] = f.MaxRunes
		}
		if len(f.Enum) > 0 {
			p["enum"] = slices.Clone(f.Enum)
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
