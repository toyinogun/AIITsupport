package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/logging"
)

// covers: AC-4 (spec 0010). An exported span keeps only listed attributes
// with plain values, loses its events and status text, and its links and
// scope carry no attributes; the span it was given is unchanged.
func TestRedactingExporter(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	link := sdktrace.Link{
		SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}}),
		Attributes:  []attribute.KeyValue{attribute.String("email", "jan@noord.example")},
	}
	in := tracetest.SpanStub{
		Name: "tool.call",
		Attributes: []attribute.KeyValue{
			attribute.String("tool", "account_unlock"),
			attribute.String("email", "jan@noord.example"),
			attribute.String("outcome", "Jan de Vries"),
			attribute.Int("model_calls", 2),
			attribute.StringSlice("kind", []string{"a"}),
		},
		Events: []sdktrace.Event{{Name: "exception", Attributes: []attribute.KeyValue{attribute.String("exception.message", "ZEBRA")}}},
		Status: sdktrace.Status{Code: codes.Error, Description: "ZEBRA failed"},
		Links:  []sdktrace.Link{link},
		InstrumentationScope: instrumentation.Scope{
			Name: "noord", Attributes: attribute.NewSet(attribute.String("user", "ZEBRA")),
		},
	}.Snapshot()

	if err := NewRedactingExporter(mem).ExportSpans(t.Context(), []sdktrace.ReadOnlySpan{in}); err != nil {
		t.Fatal(err)
	}
	got := mem.GetSpans()
	if len(got) != 1 {
		t.Fatalf("exported %d spans, want 1", len(got))
	}
	s := got[0]
	want := map[attribute.Key]string{
		"tool": "account_unlock", "email": logging.Redacted, "outcome": logging.Redacted,
		"model_calls": "2", "kind": logging.Redacted,
	}
	for _, kv := range s.Attributes {
		if want[kv.Key] != kv.Value.String() {
			t.Errorf("%s = %q, want %q", kv.Key, kv.Value.String(), want[kv.Key])
		}
	}
	if len(s.Attributes) != len(want) {
		t.Errorf("attributes = %v", s.Attributes)
	}
	if len(s.Events) != 0 || s.Status.Description != "" || s.Status.Code != codes.Error {
		t.Errorf("events %v, status %+v; want none, an empty description, code Error", s.Events, s.Status)
	}
	if len(s.Links) != 1 || len(s.Links[0].Attributes) != 0 || !s.Links[0].SpanContext.Equal(link.SpanContext) {
		t.Errorf("links = %+v, want one bare link", s.Links)
	}
	if s.InstrumentationScope.Attributes.Len() != 0 || s.InstrumentationScope.Name != "noord" {
		t.Errorf("scope = %+v, want noord with no attributes", s.InstrumentationScope)
	}

	// The original span is unchanged.
	if in.Attributes()[1].Value.AsString() != "jan@noord.example" || in.Status().Description != "ZEBRA failed" ||
		len(in.Events()) != 1 || len(in.Links()[0].Attributes) != 1 {
		t.Error("the exporter changed the span it was given")
	}
}

// covers: AC-4 (spec 0010). A burst of exporter errors logs one
// telemetry_export_failed line with only a cause; a minute later, another.
func TestErrorHandlerIsThrottledAndTextFree(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	h := newErrorHandler(slog.New(slog.NewJSONHandler(&buf, nil)), func() time.Time { return now })
	for range 5 {
		h.Handle(errors.New("ZEBRA export to jan@noord.example failed"))
	}
	if n := strings.Count(buf.String(), "telemetry_export_failed"); n != 1 {
		t.Errorf("%d lines in a burst, want 1:\n%s", n, buf.String())
	}
	now = now.Add(errorEvery)
	h.Handle(errors.New("ZEBRA"))
	out := buf.String()
	if n := strings.Count(out, `"msg":"telemetry_export_failed","cause":"other"}`); n != 2 {
		t.Errorf("%d lines a minute apart, want 2:\n%s", n, out)
	}
	if strings.Contains(out, "ZEBRA") {
		t.Errorf("error text leaked:\n%s", out)
	}
}

// captureExporter keeps the spans exactly as the redacting exporter hands
// them on, so a test can call any ReadOnlySpan method on them.
type captureExporter struct{ spans []sdktrace.ReadOnlySpan }

func (c *captureExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.spans = append(c.spans, spans...)
	return nil
}

func (c *captureExporter) Shutdown(context.Context) error { return nil }

// covers: AC-4 (spec 0010). The deprecated InstrumentationLibrary accessor,
// which older exporters still read, carries no scope attributes either, and
// the dropped event count does not hint that events existed.
func TestRedactingExporterDeprecatedScopeHasNoAttributes(t *testing.T) {
	inner := &captureExporter{}
	in := tracetest.SpanStub{
		Name:                 "tool.call",
		Events:               []sdktrace.Event{{Name: "exception"}},
		DroppedEvents:        3,
		InstrumentationScope: instrumentation.Scope{Name: "noord", Version: "1", Attributes: attribute.NewSet(attribute.String("user", "ZEBRA"))},
	}.Snapshot()

	if err := NewRedactingExporter(inner).ExportSpans(t.Context(), []sdktrace.ReadOnlySpan{in}); err != nil {
		t.Fatal(err)
	}

	lib := inner.spans[0].InstrumentationLibrary() //nolint:staticcheck // the deprecated accessor is the one under test
	if lib.Attributes.Len() != 0 || lib.Name != "noord" || lib.Version != "1" {
		t.Errorf("InstrumentationLibrary = %+v, want noord 1 with no attributes", lib)
	}
	if n := inner.spans[0].DroppedEvents(); n != 0 {
		t.Errorf("DroppedEvents = %d, want 0", n)
	}
}

// covers: AC-4 (spec 0010). A span attribute passes under the same value
// rules as a log attribute: a listed key with a number, a bool, or a string
// of at most 128 bytes with no whitespace and no @; anything else prints
// [redacted] under its own key.
func TestRedactingExporterAppliesTheValueRules(t *testing.T) {
	cases := []struct {
		name string
		kv   attribute.KeyValue
		keep bool
	}{
		{"short token", attribute.String("outcome", "ok"), true},
		{"128 bytes", attribute.String("outcome", strings.Repeat("a", 128)), true},
		{"129 bytes", attribute.String("outcome", strings.Repeat("a", 129)), false},
		{"an @", attribute.String("outcome", "a@b"), false},
		{"a tab", attribute.String("outcome", "a\tb"), false},
		{"empty string", attribute.String("outcome", ""), true},
		{"bool", attribute.Bool("outcome", true), true},
		{"int", attribute.Int64("model_calls", 3), true},
		{"float", attribute.Float64("model_calls", 1.5), true},
		{"int slice", attribute.Int64Slice("model_calls", []int64{1}), false},
		{"unlisted key with an int", attribute.Int("given_name", 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mem := tracetest.NewInMemoryExporter()
			in := tracetest.SpanStub{Name: "tool.call", Attributes: []attribute.KeyValue{tc.kv}}.Snapshot()

			if err := NewRedactingExporter(mem).ExportSpans(t.Context(), []sdktrace.ReadOnlySpan{in}); err != nil {
				t.Fatal(err)
			}

			got := mem.GetSpans()[0].Attributes[0]
			want := attribute.String(string(tc.kv.Key), logging.Redacted)
			if tc.keep {
				want = tc.kv
			}
			if got != want {
				t.Errorf("attribute = %v, want %v", got, want)
			}
		})
	}
}
