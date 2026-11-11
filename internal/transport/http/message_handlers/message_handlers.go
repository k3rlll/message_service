package message_handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	configs "main/internal/configs"
	domainComm "main/internal/domain"
	domainChat "main/internal/domain/chat_entity"
	domain "main/internal/domain/message_entity"
	mddlwr "main/internal/transport/middleware"
	ws "main/internal/ws"

	"github.com/coder/websocket"
	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

const (
	defaultPageLimit  = 20
	maxDeleteMessages = 100
)

type messageUsecase interface {
	SendMessage(
		ctx context.Context,
		senderID string,
		chatID string,
		msgType string,
		content string,
		metadata map[string]any) (*domain.Message, error)

	ListMessages(
		ctx context.Context,
		userID string,
		chatID string,
		anchorID string,
		limit int64) ([]domain.Message, bool, error)

	DeleteMessages(
		ctx context.Context,
		userID string,
		chatID string,
		messageIDs []string) error

	UpdateMessage(
		ctx context.Context,
		userID string,
		chatID string,
		messageID string,
		content string) error

	SearchMessages(
		ctx context.Context,
		userID string,
		chatID string,
		searchText string,
		anchorID string,
		limit int64) ([]domain.Message, bool, error)
}

type Handler struct {
	logger  *slog.Logger
	usecase messageUsecase
	hub     *ws.Hub
	wsCfg   configs.WebsocketConfig
}

func NewHandler(
	logger *slog.Logger,
	usecase messageUsecase,
	hub *ws.Hub,
	wsCfg configs.WebsocketConfig) *Handler {
	return &Handler{
		logger:  logger,
		usecase: usecase,
		hub:     hub,
		wsCfg:   wsCfg,
	}
}

// DTO

// Отправитель берется из токена, а не из тела запроса.
// Тип "system" клиенту недоступен: такие сообщения создает только сервер.
type SendMessageRequest struct {
	ChatID   string         `json:"chat_id" validate:"required,ulid"`
	Type     string         `json:"type" validate:"required,oneof=text image video audio"`
	Content  string         `json:"content" validate:"required,max=4000"`
	Metadata map[string]any `json:"metadata"`
}

type ListMessagesRequest struct {
	ChatID   string `query:"chat_id" validate:"required,ulid"`
	AnchorID string `query:"anchor" validate:"omitempty,ulid"` // курсор: ID самого старого из уже загруженных сообщений
	Limit    int64  `query:"limit" validate:"omitempty,min=1,max=100"`
}

type MessagesResponse struct {
	Messages []domain.Message `json:"messages"`
	HasMore  bool             `json:"has_more"` // есть ли еще страницы
}

type DeleteMessagesRequest struct {
	ChatID     string   `query:"chat_id" validate:"required,ulid"`
	MessageIDs []string `query:"message_ids" validate:"required,min=1,max=100,dive,ulid"`
}

type UpdateMessageRequest struct {
	MessageID string `param:"message_id" validate:"required,ulid"`
	ChatID    string `json:"chat_id" validate:"required,ulid"`
	Content   string `json:"content" validate:"required,max=4000"`
}

type SearchMessagesRequest struct {
	ChatID   string `query:"chat_id" validate:"required,ulid"`
	Text     string `query:"text" validate:"required,max=200"`
	AnchorID string `query:"anchor" validate:"omitempty,ulid"`
	Limit    int64  `query:"limit" validate:"omitempty,min=1,max=50"` // $text поиск тяжелее листинга, поэтому лимит ниже
}

// WS апгрейдит HTTP в WebSocket и обслуживает соединение.
// GET /api/v1/ws
func (h *Handler) WS(c echo.Context) error {
	const op = "Handler.WS"
	ctx := c.Request().Context()
	userID := mddlwr.UserID(c)

	conn, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{
		InsecureSkipVerify:   h.wsCfg.InsecureSkipVerify,
		CompressionMode:      websocket.CompressionNoContextTakeover,
		CompressionThreshold: h.wsCfg.CompressionThreshold,
	})
	if err != nil {
		// Accept уже сам отправил клиенту HTTP-ответ с ошибкой
		h.logger.WarnContext(ctx, "Failed to accept WebSocket connection",
			"op", op, "userID", userID, "error", err)
		return nil
	}
	conn.SetReadLimit(h.wsCfg.MaxMessageSize)

	h.logger.InfoContext(ctx, "WebSocket connection established",
		"op", op, "userID", userID)

	client := ws.NewClient(
		ctx, h.hub, conn, userID, h.wsCfg.ClientChanSize, h.usecase, h.logger)

	// Блокирующий вызов: хэндлер висит здесь, пока клиент не отключится
	client.Serve()

	return nil
}

// SendMessage отправляет сообщение в чат.
// POST /api/v1/messages
func (h *Handler) SendMessage(c echo.Context) error {
	ctx := c.Request().Context()

	var req SendMessageRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}

	msg, err := h.usecase.SendMessage(
		ctx, mddlwr.UserID(c), req.ChatID, req.Type, req.Content, req.Metadata)
	if err != nil {
		return h.mapError(ctx, "Handler.SendMessage", err)
	}

	return c.JSON(http.StatusCreated, msg)
}

// ListMessages возвращает страницу сообщений чата от новых к старым.
// GET /api/v1/messages?chat_id=xxx&anchor=xxx&limit=xxx
func (h *Handler) ListMessages(c echo.Context) error {
	ctx := c.Request().Context()

	var req ListMessagesRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}

	if req.Limit == 0 {
		req.Limit = defaultPageLimit
	}

	messages, hasMore, err := h.usecase.ListMessages(
		ctx, mddlwr.UserID(c), req.ChatID, req.AnchorID, req.Limit)
	if err != nil {
		return h.mapError(ctx, "Handler.ListMessages", err)
	}

	return c.JSON(http.StatusOK, newMessagesResponse(messages, hasMore))
}

// DeleteMessages удаляет сообщения пользователя.
// DELETE /api/v1/messages?chat_id=xxx&message_ids=xxx&message_ids=yyy
func (h *Handler) DeleteMessages(c echo.Context) error {
	ctx := c.Request().Context()

	var req DeleteMessagesRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}

	err := h.usecase.DeleteMessages(ctx, mddlwr.UserID(c), req.ChatID, req.MessageIDs)
	if err != nil {
		return h.mapError(ctx, "Handler.DeleteMessages", err)
	}

	return c.NoContent(http.StatusNoContent)
}

// UpdateMessage меняет текст сообщения.
// PUT /api/v1/messages/:message_id
func (h *Handler) UpdateMessage(c echo.Context) error {
	ctx := c.Request().Context()

	var req UpdateMessageRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}

	err := h.usecase.UpdateMessage(
		ctx, mddlwr.UserID(c), req.ChatID, req.MessageID, req.Content)
	if err != nil {
		return h.mapError(ctx, "Handler.UpdateMessage", err)
	}

	return c.NoContent(http.StatusNoContent)
}

// SearchMessages ищет текстовые сообщения в чате.
// GET /api/v1/messages/search?chat_id=xxx&text=xxx&anchor=xxx&limit=xxx
func (h *Handler) SearchMessages(c echo.Context) error {
	ctx := c.Request().Context()

	var req SearchMessagesRequest
	if err := bindAndValidate(c, &req); err != nil {
		return err
	}

	if req.Limit == 0 {
		req.Limit = defaultPageLimit
	}

	messages, hasMore, err := h.usecase.SearchMessages(
		ctx, mddlwr.UserID(c), req.ChatID, req.Text, req.AnchorID, req.Limit)
	if err != nil {
		return h.mapError(ctx, "Handler.SearchMessages", err)
	}

	return c.JSON(http.StatusOK, newMessagesResponse(messages, hasMore))
}

func newMessagesResponse(messages []domain.Message, hasMore bool) MessagesResponse {
	if messages == nil {
		// чтобы в JSON был пустой массив, а не null
		messages = []domain.Message{}
	}
	return MessagesResponse{Messages: messages, HasMore: hasMore}
}

// bindAndValidate разбирает запрос в DTO и валидирует его.
// Возвращает готовую HTTP ошибку: 400 при некорректном формате, 422 при провале валидации.
func bindAndValidate(c echo.Context, req any) error {
	if err := c.Bind(req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if err := c.Validate(req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(
				http.StatusUnprocessableEntity, formatValidationErrors(validationErrs))
		}
		return echo.NewHTTPError(http.StatusBadRequest, "validation error")
	}

	return nil
}

// formatValidationErrors переводит ошибки валидации в формат поле - причина
func formatValidationErrors(errs validator.ValidationErrors) map[string]string {
	details := make(map[string]string, len(errs))
	for _, f := range errs {
		details[f.Field()] = fmt.Sprintf("failed on the '%s' rule", f.Tag())
	}
	return details
}

// mapError переводит доменные ошибки в HTTP статусы
func (h *Handler) mapError(ctx context.Context, op string, err error) error {
	switch {
	case errors.Is(err, domainComm.ErrInvalidInput):
		return echo.NewHTTPError(http.StatusBadRequest, "message content is empty or too long")
	case errors.Is(err, domainComm.ErrAccessDenied):
		return echo.NewHTTPError(http.StatusForbidden, "you are not a member of this chat")
	case errors.Is(err, domainChat.ErrChatNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "chat not found")
	case errors.Is(err, domainComm.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "message not found or it is not yours")
	case errors.Is(err, domainComm.ErrDatabaseTimeout):
		return echo.NewHTTPError(http.StatusServiceUnavailable, "service temporarily unavailable")
	}

	// Неожиданная ошибка: логируем на границе системы, клиенту детали не отдаем
	h.logger.ErrorContext(ctx, "request failed", "op", op, "error", err)
	return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
}
