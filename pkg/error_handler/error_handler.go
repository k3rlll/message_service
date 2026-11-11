package error_handler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
)

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details - ошибки по полям при провале валидации: поле - причина
	Details map[string]string `json:"details,omitempty"`
}

type AppError struct {
	HTTPCode int
	Code     string
	Message  string
}

func (e *AppError) Error() string {
	return e.Message
}

func CustomHTTPErrorHandler(err error, c echo.Context, logger *slog.Logger) {
	if c.Response().Committed {
		return
	}

	// дефолтный ответ для нераспознанных ошибок
	httpCode := http.StatusInternalServerError
	resp := ErrorResponse{
		Error: ErrorDetail{
			Code:    "INTERNAL_ERROR",
			Message: "internal server error",
		},
	}

	var (
		appErr *AppError
		he     *echo.HTTPError
	)

	switch {
	// кастомная ошибка приложения
	case errors.As(err, &appErr):
		httpCode = appErr.HTTPCode
		resp.Error.Code = appErr.Code
		resp.Error.Message = appErr.Message

	// стандартная HTTP ошибка Echo
	case errors.As(err, &he):
		httpCode = he.Code
		resp.Error.Code = codeForStatus(he.Code)

		if details, ok := he.Message.(map[string]string); ok {
			resp.Error.Message = "validation failed"
			resp.Error.Details = details
		} else {
			resp.Error.Message = fmt.Sprintf("%v", he.Message)
		}

	default:
		logger.ErrorContext(c.Request().Context(), "Unhandled error", slog.Any("error", err))
	}

	if err := c.JSON(httpCode, resp); err != nil {
		logger.ErrorContext(c.Request().Context(), "Failed to write error response",
			slog.Any("error", err))
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusUnprocessableEntity:
		return "VALIDATION_ERROR"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	default:
		return "INTERNAL_ERROR"
	}
}
