package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// newTraceID returns a random trace id that is not all zero (spec 0016, AC-1).
func newTraceID() trace.TraceID {
	for {
		var id trace.TraceID
		if _, err := rand.Read(id[:]); err != nil {
			// crypto/rand does not fail on the supported platforms; a zero id
			// is skipped below and the loop reads again.
			continue
		}
		if id.IsValid() {
			return id
		}
	}
}

// parseTraceID reads a stored trace id; ok is false for anything but 32
// lowercase hex characters that are not all zero.
func parseTraceID(s string) (trace.TraceID, bool) {
	if len(s) != 32 || s != strings.ToLower(s) {
		return trace.TraceID{}, false
	}
	var id trace.TraceID
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return trace.TraceID{}, false
	}
	return id, id.IsValid()
}

// conversationContext puts a remote, sampled parent span context on ctx whose
// trace id is the conversation's, so the turn's spans join one trace. The
// parent span id is the last 8 bytes of the trace id, with the lowest bit
// set if they are all zero (spec 0016, AC-1).
func conversationContext(ctx context.Context, id trace.TraceID) context.Context {
	var span trace.SpanID
	copy(span[:], id[8:])
	if !span.IsValid() {
		span[7] = 1
	}
	return trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: id, SpanID: span, TraceFlags: trace.FlagsSampled, Remote: true,
	}))
}

// turnTraceID is the trace id this turn uses: the thread's stored id, else a
// fresh one. A failed lookup also gives a fresh id; the turn then lands in a
// stray trace and the stored id is kept (spec 0016, AC-1).
func (a *Agent) turnTraceID(ctx context.Context, in Inbound) trace.TraceID {
	stored, err := a.deps.Store.ThreadTrace(ctx, in.ChannelID, in.ThreadTS)
	if err == nil {
		if id, ok := parseTraceID(stored); ok {
			return id
		}
	}
	return newTraceID()
}
