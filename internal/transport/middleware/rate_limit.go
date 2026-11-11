package middleware

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"main/internal/ratelimit"
	errHandler "main/pkg/error_handler"

	"github.com/labstack/echo/v4"
)

type RateLimiter interface {
	Allow(ctx context.Context, key string, rule ratelimit.Rule) (ratelimit.Result, error)
}

// RateLimitMiddleware ограничивает частоту запросов пользователя.
// Должен стоять после AuthMiddleware: лимит считается по ID пользователя, а не по IP,
// чтобы пользователи за одним NAT не мешали друг другу.
func RateLimitMiddleware(limiter RateLimiter, rule ratelimit.Rule, logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := c.Request().Context()

			result, err := limiter.Allow(ctx, "http:"+UserID(c), rule)
			if err != nil {
				// Redis недоступен: пропускаем запрос. Лимит - защита от злоупотреблений,
				// и ее временное отсутствие лучше, чем отказ всем пользователям.
				logger.ErrorContext(ctx, "rate limiter failed, request allowed", "error", err)
				return next(c)
			}

			if result.Limit > 0 {
				header := c.Response().Header()
				header.Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
				header.Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
			}

			if !result.Allowed {
				retryAfter := int(math.Ceil(result.RetryAfter.Seconds()))
				c.Response().Header().Set("Retry-After", strconv.Itoa(max(retryAfter, 1)))

				return &errHandler.AppError{
					HTTPCode: http.StatusTooManyRequests,
					Code:     "RATE_LIMITED",
					Message:  "too many requests, slow down",
				}
			}

			return next(c)
		}
	}
}
