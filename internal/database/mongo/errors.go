package mongo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	domainComm "main/internal/domain"

	"go.mongodb.org/mongo-driver/mongo"
)

// MapError превращает ошибку драйвера в доменную и логирует ее.
// Нарушение уникального индекса не логируется: это штатная ситуация,
// которую обрабатывает вызывающий код.
func MapError(ctx context.Context, logger *slog.Logger, op string, err error, attrs ...any) error {
	if mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("%s: %w", op, domainComm.ErrDuplicate)
	}

	attrs = append([]any{"op", op, "error", err}, attrs...)

	if errors.Is(err, context.DeadlineExceeded) || mongo.IsTimeout(err) {
		logger.ErrorContext(ctx, "database operation timed out", attrs...)
		return fmt.Errorf("%s: %w", op, domainComm.ErrDatabaseTimeout)
	}

	logger.ErrorContext(ctx, "database operation failed", attrs...)
	return fmt.Errorf("%s: %w", op, domainComm.ErrInternal)
}
