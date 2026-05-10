// Package buildlog constructs a structured slog.Logger from a level string.
// The level string matches common env-var conventions: "debug", "info",
// "warn", "error". Anything else defaults to Info.
package buildlog

import (
	"log/slog"
	"os"
)

// New returns a JSON-format slog.Logger writing to stdout at the given level.
func New(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
