// Package scanfixture holds the calls the source scan must reject (each
// marked with a want comment) and ones it must accept. It is never built.
package scanfixture

import (
	"context"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func calls(ctx context.Context, logger *slog.Logger, tracer trace.Tracer, w http.ResponseWriter, v, name string, args []any) {
	logger.Info("ok", "kind", v, "rows", 3)
	logger.Info("bad", "user", v) // want "not on the allow list"
	key := "kind"
	logger.Warn("bad", key, v)                                          // want "not a string literal"
	logger.Error("bad", "msg", v)                                       // want "built in key"
	logger.InfoContext(ctx, "bad", "email", v)                          // want "not on the allow list"
	logger.Info("bad", args...)                                         // want "spread"
	logger.With("username", v).Info("x")                                // want "not on the allow list"
	logger.LogAttrs(ctx, slog.LevelInfo, "bad", slog.String("name", v)) // want "not on the allow list"
	_ = attribute.String("email", v)                                    // want "not on the allow list"
	_ = attribute.Int("rows", 1)
	_ = attribute.Key("department") // want "not on the allow list"
	_, _ = tracer.Start(ctx, name)  // want "span name"
	_, span := tracer.Start(ctx, "fixture.span")
	span.SetName(name) // want "span name"
	span.SetName("fixture.renamed")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
