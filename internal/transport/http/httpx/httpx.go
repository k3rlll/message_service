// Package httpx - общие помощники HTTP-хендлеров: разбор запроса и маппинг ошибок.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"main/internal/transport/apierror"
	errHandler "main/pkg/error_handler"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

// Bind разбирает запрос в DTO и валидирует его.
// Возвращает готовую HTTP ошибку: 400 при некорректном формате, 422 при провале валидации.
func Bind(c echo.Context, req any) error {
	if err := c.Bind(req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if err := c.Validate(req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			details := make(map[string]string, len(validationErrs))
			for _, f := range validationErrs {
				details[f.Field()] = fmt.Sprintf("failed on the '%s' rule", f.Tag())
			}
			return echo.NewHTTPError(http.StatusUnprocessableEntity, details)
		}
		return echo.NewHTTPError(http.StatusBadRequest, "validation error")
	}

	return nil
}

// Error переводит ошибку usecase в HTTP-ошибку.
// Неожиданные ошибки логируются здесь, на границе системы; клиенту их детали не уходят.
func Error(ctx context.Context, logger *slog.Logger, op string, err error) error {
	info, known := apierror.Describe(err)
	if !known {
		logger.ErrorContext(ctx, "request failed", "op", op, "error", err)
	}

	return &errHandler.AppError{
		HTTPCode: info.Status,
		Code:     info.Code,
		Message:  info.Message,
	}
}
