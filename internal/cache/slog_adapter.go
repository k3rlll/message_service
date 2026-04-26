package cache

import (
	"context"
	"log/slog"
)

// обертка над логгером
type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter функция для создания адаптера
func NewSlogAdapter(l *slog.Logger) *SlogAdapter {
	return &SlogAdapter{logger: l}
}

// Реализуем метод Warn для интерфейса otter.Logger
func (a *SlogAdapter) Warn(ctx context.Context, msg string, err error) {
	// Вызываем WarnContext у slog чтобы не потерять контекст
	// и прокидываем ошибку как атрибут
	a.logger.WarnContext(ctx, msg, slog.Any("error", err))
}

// Реализуем метод Error для интерфейса otter.Logger
func (a *SlogAdapter) Error(ctx context.Context, msg string, err error) {
	a.logger.ErrorContext(ctx, msg, slog.Any("error", err))
}
