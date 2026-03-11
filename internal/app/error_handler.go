package app

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
)

func CustomHTTPErrorHandler(err error, c echo.Context) {

	if c.Response().Committed {
		return
	}

	code := http.StatusInternalServerError
	var msg any
	msg = "Internal Server Error"

	// check if error is echo.HTTPError to get the status code and message
	var he *echo.HTTPError
	if errors.As(err, &he) {
		code = he.Code
		msg = he.Message
	}

	// Log the error with the actual message for server errors, but return a generic message to the client
	if code >= 500 {
		// Log the actual error message for server errors
		c.JSON(code, map[string]string{"error": "Internal server error"})
	} else {
		c.JSON(code, map[string]any{"error": msg})
	}
}
