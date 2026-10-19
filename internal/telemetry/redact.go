package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/toyinogun/AIITsupport/internal/logging"
)

// redactingExporter applies the log allow list to every span before the
// inner exporter sees it (spec 0010, AC-4).
type redactingExporter struct {
	inner sdktrace.SpanExporter
}

// NewRedactingExporter wraps inner so each exported span keeps an attribute
// only under a listed key with a short plain value (Redacted otherwise),
// carries no events, has an empty status description, and has links and an
// instrumentation scope with no attributes. The resource passes unchanged.
// The spans it was given are never changed.
func NewRedactingExporter(inner sdktrace.SpanExporter) sdktrace.SpanExporter {
	return redactingExporter{inner: inner}
}

func (e redactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = redactedSpan{ReadOnlySpan: s}
	}
	return e.inner.ExportSpans(ctx, out)
}

func (e redactingExporter) Shutdown(ctx context.Context) error { return e.inner.Shutdown(ctx) }

// redactedSpan overrides every part of a span that can carry free text; the
// embedded span supplies the rest (names, ids, times, the resource).
type redactedSpan struct {
	sdktrace.ReadOnlySpan
}

func (s redactedSpan) Attributes() []attribute.KeyValue {
	return redactAttrs(s.ReadOnlySpan.Attributes())
}

func (s redactedSpan) Events() []sdktrace.Event { return nil }

func (s redactedSpan) DroppedEvents() int { return 0 }

func (s redactedSpan) Status() sdktrace.Status {
	return sdktrace.Status{Code: s.ReadOnlySpan.Status().Code}
}

func (s redactedSpan) Links() []sdktrace.Link {
	links := s.ReadOnlySpan.Links()
	out := make([]sdktrace.Link, len(links))
	for i, l := range links {
		out[i] = sdktrace.Link{SpanContext: l.SpanContext}
	}
	return out
}

func (s redactedSpan) InstrumentationScope() instrumentation.Scope {
	sc := s.ReadOnlySpan.InstrumentationScope()
	return instrumentation.Scope{Name: sc.Name, Version: sc.Version, SchemaURL: sc.SchemaURL}
}

//nolint:staticcheck // the deprecated method is still part of ReadOnlySpan and must not leak scope attributes
func (s redactedSpan) InstrumentationLibrary() instrumentation.Library {
	return s.InstrumentationScope()
}

func redactAttrs(attrs []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, len(attrs))
	for i, kv := range attrs {
		if logging.Allowed(string(kv.Key)) && logging.Plain(slogValue(kv.Value)) {
			out[i] = kv
			continue
		}
		out[i] = attribute.String(string(kv.Key), logging.Redacted)
	}
	return out
}

// slogValue maps a span attribute value onto the slog value the allow list
// rules read; slices map to a value that is never plain.
func slogValue(v attribute.Value) slog.Value {
	switch v.Type() {
	case attribute.BOOL:
		return slog.BoolValue(v.AsBool())
	case attribute.INT64:
		return slog.Int64Value(v.AsInt64())
	case attribute.FLOAT64:
		return slog.Float64Value(v.AsFloat64())
	case attribute.STRING:
		return slog.StringValue(v.AsString())
	default:
		return slog.AnyValue(v)
	}
}
