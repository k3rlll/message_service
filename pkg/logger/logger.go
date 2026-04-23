package logger

import (
	"context"
	"log/slog"
	"main/pkg/ctxutils"
	"os"
)

type ContextHandler struct {
	slog.Handler
}

func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	reqID := ctxutils.GetRequestID(ctx)

	if reqID != "" {

		r.AddAttrs(slog.String("request_id", reqID))
	}

	return h.Handler.Handle(ctx, r)
}

// SetupLogger инициализирует slog.Logger с учетом окружения
//
//	(production, development, local)
//
// и добавляет ContextHandler для включения Request ID в логи
func SetupLogger(env string) *slog.Logger {
	var baseHandler slog.Handler

	switch env {
	case "production":
		baseHandler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	case "development", "local":
		baseHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	default:
		baseHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	}

	ctxHandler := ContextHandler{Handler: baseHandler}

	return slog.New(ctxHandler)
}
