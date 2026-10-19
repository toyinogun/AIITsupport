// Package logging builds the JSON slog logger both binaries write to stdout,
// and owns the allow list that keeps personal data out of logs and spans
// (spec 0010): a value prints only under a listed key, as a short plain
// value. Everything else prints as Redacted.
package logging

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"unicode"
)

// Redacted replaces every value the allow list does not let through.
const Redacted = "[redacted]"

// maxValueBytes bounds a string value that may print as is.
const maxValueBytes = 128

// allowed is the one list behind Allowed, shared by logs and spans. A key
// must only ever hold an id that is not a person's (a Slack channel or event
// id is fine; a Slack user id is not), a count, a flag, or a fixed token.
// Adding one is a reviewed one line change.
var allowed = map[string]bool{
	// ids
	"event_id": true, "channel_id": true, "conversation_id": true, "call_id": true, "trace_id": true,
	// fixed tokens
	"service": true, "outcome": true, "cause": true, "reason": true, "kind": true, "code": true,
	"tool": true, "tier": true, "event_type": true, "api": true, "escalation_reason": true,
	"stop_reason": true, "model": true, "addr": true,
	// counts and flags
	"attempt": true, "attempts": true, "backoff_ms": true, "latency_ms": true, "model_calls": true,
	"tool_calls": true, "ticket": true, "removed": true, "read_only": true, "rows": true,
	"input_tokens": true, "output_tokens": true, "cache_read_tokens": true, "cache_write_tokens": true,
}

// Allowed reports whether key is on the allow list.
func Allowed(key string) bool { return allowed[key] }

// AllowedKeys returns a sorted copy of the allow list.
func AllowedKeys() []string {
	keys := make([]string, 0, len(allowed))
	for k := range allowed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Plain reports whether v may print as is: a string, int, uint, float,
// bool, or duration, and a string at most maxValueBytes long with no
// whitespace and no @.
func Plain(v slog.Value) bool {
	switch v.Kind() {
	case slog.KindInt64, slog.KindUint64, slog.KindFloat64, slog.KindBool, slog.KindDuration:
		return true
	case slog.KindString:
		s := v.String()
		return len(s) <= maxValueBytes && !strings.ContainsRune(s, '@') && strings.IndexFunc(s, unicode.IsSpace) < 0
	default:
		return false
	}
}

// New returns a JSON logger at the given level whose handler redacts every
// attribute outside the allow list (AC-1), including ones added with With.
// level is one of debug, info, warn, or error; internal/config has already
// validated it.
func New(w io.Writer, level string, service string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(redactingHandler{inner: inner}).With("service", service)
}

// redactingHandler redacts every attribute before the JSON handler sees it,
// so only the record's own time, level, and message pass as is: an attribute
// keyed time, level, or msg is redacted like any other unlisted key.
type redactingHandler struct {
	inner slog.Handler
	// grouped is set once WithGroup was called: every later attribute is
	// inside a group, so none may print as is.
	grouped bool
}

func (h redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redact(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.redact(a)
	}
	return redactingHandler{inner: h.inner.WithAttrs(out), grouped: h.grouped}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{inner: h.inner.WithGroup(name), grouped: true}
}

// redact keeps a listed key with a plain value outside any group; any other
// attribute, a group included, keeps its key and gets Redacted.
func (h redactingHandler) redact(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	if !h.grouped && Allowed(a.Key) && Plain(a.Value) {
		return a
	}
	return slog.Attr{Key: a.Key, Value: slog.StringValue(Redacted)}
}
