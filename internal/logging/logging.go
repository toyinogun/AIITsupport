// Package logging builds the JSON slog logger both binaries write to stdout.
// Redaction rules arrive with feature #12.
package logging

import (
	"io"
	"log/slog"
)

// New returns a JSON logger at the given level. level is one of debug, info,
// warn, or error; internal/config has already validated it.
func New(w io.Writer, level string, service string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})).With("service", service)
}
