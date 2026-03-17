package app

import (
	handler "main/internal/transport/http"

	"github.com/labstack/echo/v4"
)

// MapRoutes maps the API routes to their corresponding handler functions.
func MapRoutes(
	e *echo.Echo,
	handlerMsg *handler.Handler,
) {
	// Swagger documentation route
	// e.GET("/swagger/*", echoSwagger.WrapHandler)
	v1 := e.Group("/api/v1")
	{
		v1.POST("/messages", handlerMsg.SendMessage)
		v1.GET("/messages", handlerMsg.ListMessages)
		v1.DELETE("/messages", handlerMsg.DeleteMessages)
		v1.PUT("/messages", handlerMsg.UpdateMessage)
		v1.GET("/messages/search", handlerMsg.SearchMessagesByText)
	}
}
