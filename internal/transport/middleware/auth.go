package middleware

import (
	"errors"
	"net/http"
	"strings"

	domain "main/internal/domain/token_entity"
	errHandler "main/pkg/error_handler"

	"github.com/labstack/echo/v4"
)

type JWTManagerUsecase interface {
	NewAccessToken(userID string) (string, error)
	VerifyAccessToken(tokenString string) (string, error)
}

func AuthMiddleware(jwtManager JWTManagerUsecase) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {

			header := c.Request().Header.Get("authorization")
			if header == "" ||
				!strings.HasPrefix(header, "Bearer ") {
				return &errHandler.AppError{
					HTTPCode: http.StatusUnauthorized,
					Code:     "INVALID_REQUEST",
					Message:  "invalid request",
				}
			}

			accessToken := strings.TrimPrefix(header, "Bearer ")

			userID, err := jwtManager.VerifyAccessToken(accessToken)
			if err != nil {
				if errors.Is(err, domain.ErrInvalidToken) ||
					errors.Is(err, domain.ErrTokenExpired) {
					return &errHandler.AppError{
						HTTPCode: http.StatusUnauthorized,
						Code:     "INVALID_REQUEST",
						Message:  "invalid request",
					}
				}

				return &errHandler.AppError{
					HTTPCode: http.StatusInternalServerError,
					Code:     "INTERNAL_ERROR",
					Message:  "internal server error",
				}
			}

			c.Set("userID", userID)
			// c.Set("role", role)

			return next(c)
		}
	}
}

// func RateLimitMiddleware(client *redis.Client, cfg *config.RateLimiterConfig) echo.MiddlewareFunc {
// 	return func(next echo.HandlerFunc) echo.HandlerFunc {
// 		return func(c echo.Context) error {

// 			// Get the client's IP address
// 			ip := c.RealIP()
// 			key := "rate_limit:" + ip
// 			ctx := context.Background()

// 			// Increment the request count for the IP address
// 			count, err := client.Incr(ctx, key).Result()
// 			if err != nil {
// 				return echo.NewHTTPError(500, "Internal Server Error")
// 			}

// 			// Set the expiration for the key if it's the first request
// 			if count == 1 {
// 				err := client.Expire(ctx, key, cfg.Window).Err()
// 				if err != nil {
// 					return echo.NewHTTPError(500, "Internal Server Error")
// 				}
// 			}

// 			// Check if the request count exceeds the limit
// 			if count > int64(cfg.Limit) {
// 				return echo.NewHTTPError(429, "Too Many Requests")
// 			}

// 			//Adding headers with rate limit info for frontend to use
// 			c.Response().Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.Limit))
// 			c.Response().Header().Set("X-RateLimit-Remaining", strconv.Itoa(cfg.Limit-int(count)))
// 			return next(c)
// 		}

// 	}
// }
