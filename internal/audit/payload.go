package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// omittedKey counts the keys a tool does not allow. Their names are dropped
// too: a key name comes from the model and can hold any text.
const omittedKey = "_omitted"

// encodeKept keeps the allowed top level keys of data, drops the rest, and
// stores how many it dropped under omittedKey. Nested values under an allowed
// key are stored whole. A result over maxBytes becomes
// {"truncated": true, "bytes": n}. A nil map stores SQL NULL.
func encodeKept(data map[string]any, keep []string, maxBytes int) (json.RawMessage, error) {
	if data == nil {
		return nil, nil
	}
	kept := make(map[string]any, len(data))
	dropped := 0
	for k, v := range data {
		if k != omittedKey && slices.Contains(keep, k) {
			kept[k] = v
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		kept[omittedKey] = dropped
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

// Fixed reasons stored when an action fails without giving one.
const (
	reasonActionFailed   = "action failed"
	reasonActionPanicked = "action panicked"
	reasonActionExited   = "action exited without returning"
)

// WithReason wraps err with a fixed reason for the audit log's error column,
// such as "account not found". Run stores only the reason; err itself is
// returned to the caller and never stored, since directory and ticket errors
// can quote names. reason must be fixed text the tool code writes, never data
// from a request, the directory, or a ticket.
func WithReason(reason string, err error) error {
	return &reasonError{reason: reason, err: err}
}

type reasonError struct {
	reason string
	err    error
}

func (e *reasonError) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}

func (e *reasonError) Unwrap() error { return e.err }

// storedReason is the error column for an action error: its WithReason reason
// if it has one, else reasonActionFailed.
func storedReason(err error) string {
	if re, ok := errors.AsType[*reasonError](err); ok {
		return re.reason
	}
	return reasonActionFailed
}

// capRunes cuts s to at most n characters, the unit Postgres char_length counts.
func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
