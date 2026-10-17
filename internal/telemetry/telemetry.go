// Package telemetry sets up OpenTelemetry tracing. With no collector endpoint
// configured it is a no op, so spans can live in the code from day one while
// the collector, Tempo, and Grafana arrive in feature #18.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ShutdownFunc flushes and stops the tracer provider.
type ShutdownFunc func(context.Context) error

// Setup installs a global tracer provider that exports to endpoint over
// OTLP HTTP. An empty endpoint leaves the default no op provider in place.
func Setup(ctx context.Context, service, endpoint string) (ShutdownFunc, error) {
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", service))),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
