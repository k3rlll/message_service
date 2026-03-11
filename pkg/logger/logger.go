package logger

import (
	"log/slog"
	"os"
)

// SetupLogger initializes and returns a structured logger based on the provided environment.
func SetupLogger(env string) *slog.Logger {
	var log *slog.Logger
	switch env {
	case "production":
		log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	case "development", "local":
		log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	default:
		log = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return log
}
