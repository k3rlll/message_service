package app

import (
	handler "main/internal/handler/http"

	"github.com/labstack/echo/v4"
)

// MapRoutes maps the API routes to their corresponding handler functions.
func MapRoutes(
	e *echo.Echo,
	userHandler *handler.Handler,
) {
	// Swagger documentation route
	// e.GET("/swagger/*", echoSwagger.WrapHandler)
	v1 := e.Group("/api/v1")
	{
		v1.POST("/messages", userHandler.SaveMessage)
	}
}
