// Package telemetry sets up OpenTelemetry tracing. With no collector endpoint
// configured it is a no op, so spans can live in the code from day one while
// the collector, Tempo, and Grafana arrive in feature #18.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/toyinogun/AIITsupport/internal/cause"
)

// ShutdownFunc flushes and stops the tracer provider.
type ShutdownFunc func(context.Context) error

// errorEvery is the least time between two telemetry_export_failed lines.
const errorEvery = time.Minute

// Setup always installs an OpenTelemetry error handler that logs
// telemetry_export_failed with only a cause token, at most once a minute.
// With an endpoint it installs a global tracer provider that exports to it
// over OTLP HTTP through NewRedactingExporter (spec 0010, AC-4). An empty
// endpoint leaves the default no op provider in place.
func Setup(ctx context.Context, service, endpoint string, logger *slog.Logger) (ShutdownFunc, error) {
	otel.SetErrorHandler(newErrorHandler(logger, time.Now))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	target, err := tracesURL(endpoint)
	if err != nil {
		return nil, err
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(target))
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(NewRedactingExporter(exporter)),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// errorHandler logs OpenTelemetry's internal errors (a failed export, mostly)
// as a cause token, never their text, which can quote a span.
type errorHandler struct {
	logger *slog.Logger
	now    func() time.Time

	mu   sync.Mutex
	last time.Time
}

func newErrorHandler(logger *slog.Logger, now func() time.Time) *errorHandler {
	return &errorHandler{logger: logger, now: now}
}

func (h *errorHandler) Handle(err error) {
	h.mu.Lock()
	now := h.now()
	if !h.last.IsZero() && now.Sub(h.last) < errorEvery {
		h.mu.Unlock()
		return
	}
	h.last = now
	h.mu.Unlock()
	h.logger.Warn("telemetry_export_failed", "cause", cause.Of(err))
}

// tracesURL turns the configured endpoint into the URL spans are posted to.
// OTEL_EXPORTER_OTLP_ENDPOINT is a base URL (http://otel-collector:4318), so
// the signal path /v1/traces is added, as the OpenTelemetry standard says;
// otlptracehttp's WithEndpointURL would post to "/" and the Collector answers
// 404. An endpoint that already names a path is used as given.
func tracesURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse otlp endpoint: %w", err)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/traces"
	}
	return u.String(), nil
}
