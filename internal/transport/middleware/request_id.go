package middleware

import (
	"context"
	"main/pkg/ctxutils"

	"github.com/labstack/echo/v4"
)

// для лучшего трассирования запросов в логах,
// извлекаем Request ID из заголовков и помещаем его в контекст
func RequestIDToContextMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			reqID := c.Response().Header().Get(echo.HeaderXRequestID)
			if reqID == "" {
				reqID = "generated-id"
			}

			reqCtx := c.Request().Context()

			ctxWithID := context.WithValue(reqCtx, ctxutils.RequestIDKey, reqID)

			c.SetRequest(c.Request().WithContext(ctxWithID))

			return next(c)
		}
	}
}
