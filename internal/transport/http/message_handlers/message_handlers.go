package message_handlers

import (
	"context"
	"log/slog"
	"net/http"

	domain "main/internal/domain/message_entity"
	"main/internal/transport/http/httpx"
	mddlwr "main/internal/transport/middleware"
	msgUC "main/internal/usecase/message_usecase"

	"github.com/labstack/echo/v4"
)

const defaultPageLimit = 20

type messageUsecase interface {
	SendMessage(ctx context.Context, senderID string, p msgUC.SendParams) (*domain.Message, bool, error)
	ListMessages(ctx context.Context, userID, chatID, anchorID string, limit int64) ([]domain.Message, bool, error)
	SearchMessages(ctx context.Context, userID, chatID, searchText, anchorID string, limit int64) ([]domain.Message, bool, error)
	UpdateMessage(ctx context.Context, userID, chatID, messageID, content string) (*domain.Message, error)
	DeleteMessages(ctx context.Context, userID, chatID string, messageIDs []string) ([]string, error)
	SetReaction(ctx context.Context, userID, chatID, messageID, emoji string, add bool) (*domain.Message, error)
}

type Handler struct {
	logger  *slog.Logger
	usecase messageUsecase
}

func NewHandler(logger *slog.Logger, usecase messageUsecase) *Handler {
	return &Handler{
		logger:  logger,
		usecase: usecase,
	}
}

// DTO

type AttachmentDTO struct {
	Key         string `json:"key" validate:"required,max=500"`
	Name        string `json:"name" validate:"max=200"`
	ContentType string `json:"content_type" validate:"max=100"`
}

// Отправитель берется из токена, а не из тела запроса.
// Тип "system" клиенту недоступен: такие сообщения создает только сервер.
type SendMessageRequest struct {
	ChatID string `json:"chat_id" validate:"required,ulid"`
	Type   string `json:"type" validate:"omitempty,oneof=text image video audio file"`
	// Может быть пустым, если есть вложения
	Content string `json:"content" validate:"max=4000"`
	// Ключ идемпотентности: повтор запроса с тем же значением не создаст дубликат
	ClientMessageID string          `json:"client_msg_id" validate:"max=64"`
	ReplyToID       string          `json:"reply_to_id" validate:"omitempty,ulid"`
	Attachments     []AttachmentDTO `json:"attachments" validate:"max=10,dive"`
	Metadata        map[string]any  `json:"metadata"`
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

type DeleteMessagesResponse struct {
	DeletedIDs []string `json:"deleted_ids"`
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

type AddReactionRequest struct {
	MessageID string `param:"message_id" validate:"required,ulid"`
	ChatID    string `json:"chat_id" validate:"required,ulid"`
	Emoji     string `json:"emoji" validate:"required,max=32"`
}

type RemoveReactionRequest struct {
	MessageID string `param:"message_id" validate:"required,ulid"`
	ChatID    string `query:"chat_id" validate:"required,ulid"`
	Emoji     string `query:"emoji" validate:"required,max=32"`
}

// SendMessage отправляет сообщение в чат.
// 201 - сообщение создано, 200 - сообщение с таким client_msg_id уже было отправлено раньше.
// POST /api/v1/messages
func (h *Handler) SendMessage(c echo.Context) error {
	ctx := c.Request().Context()

	var req SendMessageRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	attachments := make([]domain.Attachment, len(req.Attachments))
	for i, a := range req.Attachments {
		attachments[i] = domain.Attachment{Key: a.Key, Name: a.Name, ContentType: a.ContentType}
	}

	msg, created, err := h.usecase.SendMessage(ctx, mddlwr.UserID(c), msgUC.SendParams{
		ChatID:          req.ChatID,
		Type:            req.Type,
		Content:         req.Content,
		ClientMessageID: req.ClientMessageID,
		ReplyToID:       req.ReplyToID,
		Attachments:     attachments,
		Metadata:        req.Metadata,
	})
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.SendMessage", err)
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	return c.JSON(status, msg)
}

// ListMessages возвращает страницу сообщений чата от новых к старым.
// GET /api/v1/messages?chat_id=xxx&anchor=xxx&limit=xxx
func (h *Handler) ListMessages(c echo.Context) error {
	ctx := c.Request().Context()

	var req ListMessagesRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	if req.Limit == 0 {
		req.Limit = defaultPageLimit
	}

	messages, hasMore, err := h.usecase.ListMessages(
		ctx, mddlwr.UserID(c), req.ChatID, req.AnchorID, req.Limit)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.ListMessages", err)
	}

	return c.JSON(http.StatusOK, newMessagesResponse(messages, hasMore))
}

// DeleteMessages удаляет сообщения. Участник удаляет свои, владелец и админ - любые.
// DELETE /api/v1/messages?chat_id=xxx&message_ids=xxx&message_ids=yyy
func (h *Handler) DeleteMessages(c echo.Context) error {
	ctx := c.Request().Context()

	var req DeleteMessagesRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	deletedIDs, err := h.usecase.DeleteMessages(ctx, mddlwr.UserID(c), req.ChatID, req.MessageIDs)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.DeleteMessages", err)
	}

	return c.JSON(http.StatusOK, DeleteMessagesResponse{DeletedIDs: deletedIDs})
}

// UpdateMessage меняет текст сообщения.
// PUT /api/v1/messages/:message_id
func (h *Handler) UpdateMessage(c echo.Context) error {
	ctx := c.Request().Context()

	var req UpdateMessageRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	msg, err := h.usecase.UpdateMessage(
		ctx, mddlwr.UserID(c), req.ChatID, req.MessageID, req.Content)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.UpdateMessage", err)
	}

	return c.JSON(http.StatusOK, msg)
}

// SearchMessages ищет текстовые сообщения в чате.
// GET /api/v1/messages/search?chat_id=xxx&text=xxx&anchor=xxx&limit=xxx
func (h *Handler) SearchMessages(c echo.Context) error {
	ctx := c.Request().Context()

	var req SearchMessagesRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	if req.Limit == 0 {
		req.Limit = defaultPageLimit
	}

	messages, hasMore, err := h.usecase.SearchMessages(
		ctx, mddlwr.UserID(c), req.ChatID, req.Text, req.AnchorID, req.Limit)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.SearchMessages", err)
	}

	return c.JSON(http.StatusOK, newMessagesResponse(messages, hasMore))
}

// AddReaction ставит реакцию на сообщение.
// PUT /api/v1/messages/:message_id/reactions
func (h *Handler) AddReaction(c echo.Context) error {
	ctx := c.Request().Context()

	var req AddReactionRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	msg, err := h.usecase.SetReaction(
		ctx, mddlwr.UserID(c), req.ChatID, req.MessageID, req.Emoji, true)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.AddReaction", err)
	}

	return c.JSON(http.StatusOK, msg)
}

// RemoveReaction убирает реакцию с сообщения.
// DELETE /api/v1/messages/:message_id/reactions?chat_id=xxx&emoji=xxx
func (h *Handler) RemoveReaction(c echo.Context) error {
	ctx := c.Request().Context()

	var req RemoveReactionRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	msg, err := h.usecase.SetReaction(
		ctx, mddlwr.UserID(c), req.ChatID, req.MessageID, req.Emoji, false)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.RemoveReaction", err)
	}

	return c.JSON(http.StatusOK, msg)
}

func newMessagesResponse(messages []domain.Message, hasMore bool) MessagesResponse {
	if messages == nil {
		// чтобы в JSON был пустой массив, а не null
		messages = []domain.Message{}
	}
	return MessagesResponse{Messages: messages, HasMore: hasMore}
}
