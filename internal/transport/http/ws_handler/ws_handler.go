package ws_handler

import (
	"context"
	"log/slog"

	"main/internal/configs"
	domain "main/internal/domain/message_entity"
	mddlwr "main/internal/transport/middleware"
	msgUC "main/internal/usecase/message_usecase"
	"main/internal/ws"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
)

type messageSender interface {
	SendMessage(ctx context.Context, senderID string, p msgUC.SendParams) (*domain.Message, bool, error)
}

type readMarker interface {
	MarkRead(ctx context.Context, userID, chatID, messageID string) error
}

type presenceTracker interface {
	Typing(ctx context.Context, userID, chatID string) error
	Connected(ctx context.Context, userID, connID string)
	Heartbeat(ctx context.Context, userID, connID string)
	Disconnected(ctx context.Context, userID, connID string)
}

type eventReplayer interface {
	LastEventID(ctx context.Context) (string, error)
	Replay(ctx context.Context, userID, afterID string) ([][]byte, string, bool, error)
}

// Backend собирает зависимости WebSocket-соединения из нескольких usecase
type Backend struct {
	messageSender
	readMarker
	presenceTracker
	eventReplayer
}

func NewBackend(
	messages messageSender,
	reads readMarker,
	presence presenceTracker,
	events eventReplayer) *Backend {
	return &Backend{
		messageSender:   messages,
		readMarker:      reads,
		presenceTracker: presence,
		eventReplayer:   events,
	}
}

// SendMessage переводит команду протокола в параметры usecase
func (b *Backend) SendMessage(ctx context.Context, userID string, p ws.SendMessagePayload) (*domain.Message, error) {
	msgType := domain.TypeText
	if len(p.Attachments) > 0 {
		msgType = domain.TypeFile
	}

	msg, _, err := b.messageSender.SendMessage(ctx, userID, msgUC.SendParams{
		ChatID:          p.ChatID,
		Type:            msgType,
		Content:         p.Text,
		ClientMessageID: p.ClientMessageID,
		ReplyToID:       p.ReplyToID,
		Attachments:     p.Attachments,
	})
	return msg, err
}

type Handler struct {
	logger   *slog.Logger
	hub      *ws.Hub
	backend  ws.Backend
	cfg      configs.WebsocketConfig
	instance string
}

func NewHandler(
	logger *slog.Logger,
	hub *ws.Hub,
	backend ws.Backend,
	cfg configs.WebsocketConfig,
	instance string) *Handler {
	return &Handler{
		logger:   logger,
		hub:      hub,
		backend:  backend,
		cfg:      cfg,
		instance: instance,
	}
}

// WS апгрейдит HTTP в WebSocket и обслуживает соединение.
// GET /api/v1/ws?token=xxx&last_event_id=xxx
func (h *Handler) WS(c echo.Context) error {
	const op = "Handler.WS"
	ctx := c.Request().Context()
	userID := mddlwr.UserID(c)

	conn, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{
		InsecureSkipVerify:   h.cfg.InsecureSkipVerify,
		CompressionMode:      websocket.CompressionNoContextTakeover,
		CompressionThreshold: h.cfg.CompressionThreshold,
	})
	if err != nil {
		// Accept уже сам отправил клиенту HTTP-ответ с ошибкой
		h.logger.WarnContext(ctx, "Failed to accept WebSocket connection",
			"op", op, "userID", userID, "error", err)
		return nil
	}
	conn.SetReadLimit(h.cfg.MaxMessageSize)

	h.logger.InfoContext(ctx, "WebSocket connection established", "op", op, "userID", userID)

	client := ws.NewClient(ctx, h.hub, conn, userID, h.cfg.ClientChanSize, h.backend, h.instance, h.logger)

	// Блокирующий вызов: хэндлер висит здесь, пока клиент не отключится
	client.Serve(c.QueryParam("last_event_id"))

	return nil
}
