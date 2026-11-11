package middleware

import (
	"errors"
	"net/http"
	"strings"

	domain "main/internal/domain/token_entity"
	errHandler "main/pkg/error_handler"

	"github.com/labstack/echo/v4"
)

const (
	userIDContextKey = "user_id"
	bearerPrefix     = "Bearer "
	// Браузерный WebSocket API не умеет ставить заголовки,
	// поэтому для /ws токен можно передать query-параметром
	tokenQueryParam = "token"
)

type TokenVerifier interface {
	VerifyAccessToken(tokenString string) (string, error)
}

// UserID возвращает ID пользователя, положенный в контекст AuthMiddleware.
// Пустая строка означает, что запрос не прошел аутентификацию.
func UserID(c echo.Context) string {
	userID, _ := c.Get(userIDContextKey).(string)
	return userID
}

// AuthMiddleware проверяет access-токен из заголовка Authorization: Bearer <token>
func AuthMiddleware(verifier TokenVerifier) echo.MiddlewareFunc {
	return authMiddleware(verifier, false)
}

// WSAuthMiddleware дополнительно принимает токен из query-параметра token
func WSAuthMiddleware(verifier TokenVerifier) echo.MiddlewareFunc {
	return authMiddleware(verifier, true)
}

func authMiddleware(verifier TokenVerifier, allowQueryToken bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			var accessToken string

			header := c.Request().Header.Get(echo.HeaderAuthorization)
			switch {
			case strings.HasPrefix(header, bearerPrefix):
				accessToken = strings.TrimPrefix(header, bearerPrefix)
			case allowQueryToken:
				accessToken = c.QueryParam(tokenQueryParam)
			}

			if accessToken == "" {
				return unauthorized("access token is missing")
			}

			userID, err := verifier.VerifyAccessToken(accessToken)
			if err != nil {
				if errors.Is(err, domain.ErrTokenExpired) {
					return unauthorized("access token expired")
				}
				if errors.Is(err, domain.ErrInvalidToken) {
					return unauthorized("access token is invalid")
				}

				return &errHandler.AppError{
					HTTPCode: http.StatusInternalServerError,
					Code:     "INTERNAL_ERROR",
					Message:  "internal server error",
				}
			}

			c.Set(userIDContextKey, userID)

			return next(c)
		}
	}
}

func unauthorized(message string) error {
	return &errHandler.AppError{
		HTTPCode: http.StatusUnauthorized,
		Code:     "UNAUTHORIZED",
		Message:  message,
	}
}
