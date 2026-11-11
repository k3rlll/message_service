package middleware

import (
	"context"

	"main/pkg/ctxutils"

	"github.com/labstack/echo/v4"
)

// RequestIDToContextMiddleware кладет Request ID в контекст запроса,
// чтобы логгер добавлял его ко всем записям.
// Должен стоять после echo middleware.RequestID(), который генерирует ID
// и пишет его в заголовок ответа.
func RequestIDToContextMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			reqID := c.Response().Header().Get(echo.HeaderXRequestID)
			if reqID != "" {
				ctx := context.WithValue(c.Request().Context(), ctxutils.RequestIDKey, reqID)
				c.SetRequest(c.Request().WithContext(ctx))
			}

			return next(c)
		}
	}
}
