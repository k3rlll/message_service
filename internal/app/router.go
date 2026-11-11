package app

import (
	msgHandlers "main/internal/transport/http/message_handlers"
	mddlwr "main/internal/transport/middleware"

	"github.com/labstack/echo/v4"
)

// MapRoutes maps the API routes to their corresponding handler functions.
func MapRoutes(
	e *echo.Echo,
	handlerMsg *msgHandlers.Handler,
	verifier mddlwr.TokenVerifier,
) {
	// Swagger documentation route
	// e.GET("/swagger/*", echoSwagger.WrapHandler)
	v1 := e.Group("/api/v1")

	messages := v1.Group("/messages", mddlwr.AuthMiddleware(verifier))
	{
		messages.POST("", handlerMsg.SendMessage)
		messages.GET("", handlerMsg.ListMessages)
		messages.DELETE("", handlerMsg.DeleteMessages)
		messages.PUT("/:message_id", handlerMsg.UpdateMessage)
		messages.GET("/search", handlerMsg.SearchMessages)
	}

	v1.GET("/ws", handlerMsg.WS, mddlwr.WSAuthMiddleware(verifier))
}
