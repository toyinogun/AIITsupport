package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// exportedPath sets telemetry up against a server that records the path of the
// first OTLP request, ends one span, flushes, and returns that path.
func exportedPath(t *testing.T, endpointPath string) string {
	t.Helper()
	restoreGlobalProvider(t)

	paths := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case paths <- r.URL.Path:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	shutdown, err := Setup(t.Context(), "agent", srv.URL+endpointPath, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, span := otel.Tracer("test").Start(t.Context(), "agent.turn")
	span.End()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case p := <-paths:
		return p
	default:
		t.Fatal("the server received no export request")
		return ""
	}
}

// covers: spec 0016 AC-5. OTEL_EXPORTER_OTLP_ENDPOINT is a base URL, as the
// chart, Compose, and the runbook set it (http://otel-collector:4318): the
// signal path /v1/traces is added, as the OpenTelemetry standard says. Sent to
// "/", the Collector answered 404 and no trace ever arrived (journal 0087).
func TestSetupBaseEndpointExportsToTracesPath(t *testing.T) {
	for _, tc := range []struct{ name, endpointPath string }{
		{"no path", ""},
		{"trailing slash", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exportedPath(t, tc.endpointPath); got != "/v1/traces" {
				t.Errorf("export path = %q, want /v1/traces", got)
			}
		})
	}
}

// An endpoint that already names its path is used as given.
func TestSetupKeepsAnExplicitPath(t *testing.T) {
	if got := exportedPath(t, "/custom/v1/traces"); got != "/custom/v1/traces" {
		t.Errorf("export path = %q, want /custom/v1/traces", got)
	}
}
