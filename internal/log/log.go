// Package log provides structured JSON logging via log/slog.
// Log level is controlled by LOG_LEVEL env var (debug/info/warn/error, default info).
package log

import (
	"log/slog"
	"os"
)

func init() {
	level := slog.LevelInfo
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}
