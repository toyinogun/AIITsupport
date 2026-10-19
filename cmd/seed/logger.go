package main

import (
	"io"
	"log/slog"
)

// newLogger is the seed tool's own plain JSON logger. internal/logging
// redacts by design (spec 0010) and offers no unredacted constructor;
// cmd/seed is an operator tool whose user lines are how you debug a sync,
// so its logs keep their values.
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", service)
}
