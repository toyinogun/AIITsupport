package telemetry

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// restoreGlobalProvider puts back whatever tracer provider was installed before the test.
func restoreGlobalProvider(t *testing.T) {
	t.Helper()
	prev, prevHandler := otel.GetTracerProvider(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		otel.SetErrorHandler(prevHandler)
	})
}

func TestSetupWithoutEndpointIsNoOp(t *testing.T) {
	restoreGlobalProvider(t)

	shutdown, err := Setup(t.Context(), "agent", "", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider); isSDK {
		t.Error("an empty endpoint should leave the no op provider in place")
	}
	if err := shutdown(t.Context()); err != nil {
		t.Errorf("no op shutdown returned %v", err)
	}
}

func TestSetupWithEndpointInstallsExportingProvider(t *testing.T) {
	restoreGlobalProvider(t)

	// Nothing listens here. The exporter connects lazily, so Setup still succeeds.
	shutdown, err := Setup(t.Context(), "agent", "http://127.0.0.1:1/v1/traces", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if _, isSDK := otel.GetTracerProvider().(*sdktrace.TracerProvider); !isSDK {
		t.Errorf("global provider is %T, want *sdktrace.TracerProvider", otel.GetTracerProvider())
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Errorf("shutdown with no spans returned %v", err)
	}
}
