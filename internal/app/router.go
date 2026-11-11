package app

import (
	"log/slog"

	"main/internal/ratelimit"
	chatHandlers "main/internal/transport/http/chat_handlers"
	msgHandlers "main/internal/transport/http/message_handlers"
	wsHandler "main/internal/transport/http/ws_handler"
	mddlwr "main/internal/transport/middleware"

	"github.com/labstack/echo/v4"
)

type Handlers struct {
	Messages *msgHandlers.Handler
	Chats    *chatHandlers.Handler
	WS       *wsHandler.Handler
}

type RouteDeps struct {
	Verifier mddlwr.TokenVerifier
	Limiter  mddlwr.RateLimiter
	HTTPRule ratelimit.Rule
	Logger   *slog.Logger
}

// MapRoutes maps the API routes to their corresponding handler functions.
func MapRoutes(e *echo.Echo, h Handlers, deps RouteDeps) {
	v1 := e.Group("/api/v1")

	// REST: аутентификация, затем лимит по пользователю
	api := v1.Group("",
		mddlwr.AuthMiddleware(deps.Verifier),
		mddlwr.RateLimitMiddleware(deps.Limiter, deps.HTTPRule, deps.Logger),
	)

	messages := api.Group("/messages")
	{
		messages.POST("", h.Messages.SendMessage)
		messages.GET("", h.Messages.ListMessages)
		messages.DELETE("", h.Messages.DeleteMessages)
		messages.GET("/search", h.Messages.SearchMessages)
		messages.PUT("/:message_id", h.Messages.UpdateMessage)
		messages.PUT("/:message_id/reactions", h.Messages.AddReaction)
		messages.DELETE("/:message_id/reactions", h.Messages.RemoveReaction)
	}

	chats := api.Group("/chats")
	{
		chats.POST("", h.Chats.CreateChat)
		chats.GET("", h.Chats.ListChats)
		chats.GET("/:chat_id", h.Chats.GetChat)
		chats.PATCH("/:chat_id", h.Chats.RenameChat)

		chats.POST("/:chat_id/members", h.Chats.AddMember)
		chats.DELETE("/:chat_id/members/:user_id", h.Chats.RemoveMember)
		chats.PUT("/:chat_id/members/:user_id/role", h.Chats.SetRole)

		chats.POST("/:chat_id/read", h.Chats.MarkRead)

		chats.GET("/:chat_id/pins", h.Chats.ListPins)
		chats.PUT("/:chat_id/pins/:message_id", h.Chats.Pin)
		chats.DELETE("/:chat_id/pins/:message_id", h.Chats.Unpin)

		chats.GET("/:chat_id/presence", h.Chats.Presence)

		chats.POST("/:chat_id/attachments", h.Chats.CreateUpload)
		chats.GET("/:chat_id/attachments", h.Chats.CreateDownload)
	}

	// WebSocket: токен можно передать в query, отправка сообщений лимитируется в usecase
	v1.GET("/ws", h.WS.WS, mddlwr.WSAuthMiddleware(deps.Verifier))
}
