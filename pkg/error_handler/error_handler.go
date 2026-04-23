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

	// проверка на кастомную ошибку приложения
	var appErr *AppError
	if errors.As(err, &appErr) {
		httpCode = appErr.HTTPCode
		resp.Error.Code = appErr.Code
		resp.Error.Message = appErr.Message
	} else {
		//проверка стандартных HTTP ошибок Echo
		var he *echo.HTTPError
		if errors.As(err, &he) {
			httpCode = he.Code
			resp.Error.Message = fmt.Sprintf("%v", he.Message)

			// маппинг кодов ошибок Echo на коды ошибок
			switch he.Code {
			case http.StatusBadRequest:
				resp.Error.Code = "INVALID_REQUEST"
			case http.StatusUnauthorized:
				resp.Error.Code = "UNAUTHORIZED"
			case http.StatusForbidden:
				resp.Error.Code = "FORBIDDEN"
			case http.StatusNotFound:
				resp.Error.Code = "NOT_FOUND"
			default:
				resp.Error.Code = "INTERNAL_ERROR"
			}
		} else {
			logger.Error("Unhandled error", slog.Any("error", err))
		}
	}

	// отправляем JSON ответ клиенту
	c.JSON(httpCode, resp)
}
