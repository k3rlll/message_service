package middleware

import (
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/oklog/ulid/v2"
)

func AuthMiddleware(JWTManager *JWTManager) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {

			header := c.Request().Header.Get("authorization")
			if header == "" || !strings.HasPrefix(header, "Bearer ") {
				return echo.NewHTTPError(401, "Unauthorized")
			}

			accessToken := strings.TrimPrefix(header, "Bearer ")

			// For testing purposes, we can bypass JWT verification if the token is "admin"
			if accessToken == "admin" {
				userID := ulid.Make()
				c.Set("userID", userID)
				return next(c)
			}

			userID, err := JWTManager.VerifyAccessToken(accessToken)
			if err != nil {
				return echo.NewHTTPError(401, "Unauthorized")
			}

			c.Set("userID", userID)
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
