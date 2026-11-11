package chat_handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	domain "main/internal/domain/chat_entity"
	domainMsg "main/internal/domain/message_entity"
	"main/internal/transport/http/httpx"
	mddlwr "main/internal/transport/middleware"
	attachUC "main/internal/usecase/attachment_usecase"
	chatUC "main/internal/usecase/chat_usecase"

	"github.com/labstack/echo/v4"
)

const defaultPageLimit = 20

type chatUsecase interface {
	CreateChat(ctx context.Context, creatorID string, p chatUC.CreateParams) (*domain.Chat, bool, error)
	ListChats(ctx context.Context, userID string, before time.Time, limit int64) ([]chatUC.ChatSummary, bool, error)
	GetChat(ctx context.Context, userID, chatID string) (*chatUC.ChatSummary, error)
	RenameChat(ctx context.Context, userID, chatID, name string) (*domain.Chat, error)
	AddMember(ctx context.Context, actorID, chatID, userID string) (*domain.Chat, error)
	RemoveMember(ctx context.Context, actorID, chatID, userID string) error
	SetRole(ctx context.Context, actorID, chatID, userID, role string) error
	MarkRead(ctx context.Context, userID, chatID, messageID string) error
	SetPin(ctx context.Context, userID, chatID, messageID string, pin bool) error
	ListPins(ctx context.Context, userID, chatID string) ([]domainMsg.Message, error)
}

type presenceUsecase interface {
	Online(ctx context.Context, userIDs []string) (map[string]bool, error)
}

type attachmentUsecase interface {
	CreateUpload(ctx context.Context, userID, chatID, fileName string, size int64) (*attachUC.SignedURL, error)
	CreateDownload(ctx context.Context, userID, chatID, key string) (*attachUC.SignedURL, error)
}

type Handler struct {
	logger      *slog.Logger
	chats       chatUsecase
	presence    presenceUsecase
	attachments attachmentUsecase
}

func NewHandler(
	logger *slog.Logger,
	chats chatUsecase,
	presence presenceUsecase,
	attachments attachmentUsecase) *Handler {
	return &Handler{
		logger:      logger,
		chats:       chats,
		presence:    presence,
		attachments: attachments,
	}
}

// DTO

type ChatPath struct {
	ChatID string `param:"chat_id" validate:"required,ulid"`
}

type CreateChatRequest struct {
	Type string `json:"type" validate:"required,oneof=direct group"`
	// Для группового чата обязательно, для личного игнорируется
	Name      string   `json:"name" validate:"max=100"`
	MemberIDs []string `json:"member_ids" validate:"max=199,dive,ulid"`
}

type ListChatsRequest struct {
	// Курсор: значение next_before из предыдущей страницы
	Before time.Time `query:"before"`
	Limit  int64     `query:"limit" validate:"omitempty,min=1,max=100"`
}

type ListChatsResponse struct {
	Chats   []chatUC.ChatSummary `json:"chats"`
	HasMore bool                 `json:"has_more"`
	// Передать в before, чтобы получить следующую страницу
	NextBefore *time.Time `json:"next_before,omitempty"`
}

type RenameChatRequest struct {
	ChatID string `param:"chat_id" validate:"required,ulid"`
	Name   string `json:"name" validate:"required,max=100"`
}

type AddMemberRequest struct {
	ChatID string `param:"chat_id" validate:"required,ulid"`
	UserID string `json:"user_id" validate:"required,ulid"`
}

type MemberPath struct {
	ChatID string `param:"chat_id" validate:"required,ulid"`
	UserID string `param:"user_id" validate:"required,ulid"`
}

type SetRoleRequest struct {
	ChatID string `param:"chat_id" validate:"required,ulid"`
	UserID string `param:"user_id" validate:"required,ulid"`
	Role   string `json:"role" validate:"required,oneof=owner admin member"`
}

type MarkReadRequest struct {
	ChatID    string `param:"chat_id" validate:"required,ulid"`
	MessageID string `json:"message_id" validate:"required,ulid"`
}

type PinPath struct {
	ChatID    string `param:"chat_id" validate:"required,ulid"`
	MessageID string `param:"message_id" validate:"required,ulid"`
}

type CreateUploadRequest struct {
	ChatID   string `param:"chat_id" validate:"required,ulid"`
	FileName string `json:"file_name" validate:"required,max=255"`
	Size     int64  `json:"size" validate:"required,min=1"`
}

type CreateDownloadRequest struct {
	ChatID string `param:"chat_id" validate:"required,ulid"`
	Key    string `query:"key" validate:"required,max=500"`
}

// CreateChat создает чат. Для уже существующего личного чата возвращает его с кодом 200.
// POST /api/v1/chats
func (h *Handler) CreateChat(c echo.Context) error {
	ctx := c.Request().Context()

	var req CreateChatRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	chat, created, err := h.chats.CreateChat(ctx, mddlwr.UserID(c), chatUC.CreateParams{
		Type:      req.Type,
		Name:      req.Name,
		MemberIDs: req.MemberIDs,
	})
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.CreateChat", err)
	}

	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	return c.JSON(status, chat)
}

// ListChats возвращает чаты пользователя, сначала с самой свежей активностью.
// GET /api/v1/chats?before=xxx&limit=xxx
func (h *Handler) ListChats(c echo.Context) error {
	ctx := c.Request().Context()

	var req ListChatsRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}
	if req.Limit == 0 {
		req.Limit = defaultPageLimit
	}

	chats, hasMore, err := h.chats.ListChats(ctx, mddlwr.UserID(c), req.Before, req.Limit)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.ListChats", err)
	}

	resp := ListChatsResponse{Chats: chats, HasMore: hasMore}
	if resp.Chats == nil {
		resp.Chats = []chatUC.ChatSummary{}
	}
	if hasMore {
		next := chats[len(chats)-1].UpdatedAt
		resp.NextBefore = &next
	}
	return c.JSON(http.StatusOK, resp)
}

// GetChat возвращает чат с участниками и счетчиком непрочитанных.
// GET /api/v1/chats/:chat_id
func (h *Handler) GetChat(c echo.Context) error {
	ctx := c.Request().Context()

	var req ChatPath
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	chat, err := h.chats.GetChat(ctx, mddlwr.UserID(c), req.ChatID)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.GetChat", err)
	}
	return c.JSON(http.StatusOK, chat)
}

// RenameChat меняет название группового чата.
// PATCH /api/v1/chats/:chat_id
func (h *Handler) RenameChat(c echo.Context) error {
	ctx := c.Request().Context()

	var req RenameChatRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	chat, err := h.chats.RenameChat(ctx, mddlwr.UserID(c), req.ChatID, req.Name)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.RenameChat", err)
	}
	return c.JSON(http.StatusOK, chat)
}

// AddMember добавляет участника в групповой чат.
// POST /api/v1/chats/:chat_id/members
func (h *Handler) AddMember(c echo.Context) error {
	ctx := c.Request().Context()

	var req AddMemberRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	chat, err := h.chats.AddMember(ctx, mddlwr.UserID(c), req.ChatID, req.UserID)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.AddMember", err)
	}
	return c.JSON(http.StatusOK, chat)
}

// RemoveMember убирает участника из чата. Выйти из чата - убрать самого себя.
// DELETE /api/v1/chats/:chat_id/members/:user_id
func (h *Handler) RemoveMember(c echo.Context) error {
	ctx := c.Request().Context()

	var req MemberPath
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	if err := h.chats.RemoveMember(ctx, mddlwr.UserID(c), req.ChatID, req.UserID); err != nil {
		return httpx.Error(ctx, h.logger, "Handler.RemoveMember", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// SetRole меняет роль участника. Роль owner передает владение.
// PUT /api/v1/chats/:chat_id/members/:user_id/role
func (h *Handler) SetRole(c echo.Context) error {
	ctx := c.Request().Context()

	var req SetRoleRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	if err := h.chats.SetRole(ctx, mddlwr.UserID(c), req.ChatID, req.UserID, req.Role); err != nil {
		return httpx.Error(ctx, h.logger, "Handler.SetRole", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// MarkRead отмечает чат прочитанным до сообщения включительно.
// POST /api/v1/chats/:chat_id/read
func (h *Handler) MarkRead(c echo.Context) error {
	ctx := c.Request().Context()

	var req MarkReadRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	if err := h.chats.MarkRead(ctx, mddlwr.UserID(c), req.ChatID, req.MessageID); err != nil {
		return httpx.Error(ctx, h.logger, "Handler.MarkRead", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// ListPins возвращает закрепленные сообщения чата.
// GET /api/v1/chats/:chat_id/pins
func (h *Handler) ListPins(c echo.Context) error {
	ctx := c.Request().Context()

	var req ChatPath
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	messages, err := h.chats.ListPins(ctx, mddlwr.UserID(c), req.ChatID)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.ListPins", err)
	}
	if messages == nil {
		messages = []domainMsg.Message{}
	}
	return c.JSON(http.StatusOK, map[string]any{"messages": messages})
}

// Pin закрепляет сообщение.
// PUT /api/v1/chats/:chat_id/pins/:message_id
func (h *Handler) Pin(c echo.Context) error {
	return h.setPin(c, true)
}

// Unpin открепляет сообщение.
// DELETE /api/v1/chats/:chat_id/pins/:message_id
func (h *Handler) Unpin(c echo.Context) error {
	return h.setPin(c, false)
}

func (h *Handler) setPin(c echo.Context, pin bool) error {
	ctx := c.Request().Context()

	var req PinPath
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	if err := h.chats.SetPin(ctx, mddlwr.UserID(c), req.ChatID, req.MessageID, pin); err != nil {
		return httpx.Error(ctx, h.logger, "Handler.SetPin", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Presence возвращает онлайн-статус участников чата.
// GET /api/v1/chats/:chat_id/presence
func (h *Handler) Presence(c echo.Context) error {
	ctx := c.Request().Context()

	var req ChatPath
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	// GetChat заодно проверяет, что запрашивающий - участник чата:
	// чужой онлайн-статус посторонним не отдаем
	chat, err := h.chats.GetChat(ctx, mddlwr.UserID(c), req.ChatID)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.Presence", err)
	}

	userIDs := make([]string, len(chat.Members))
	for i, member := range chat.Members {
		userIDs[i] = member.UserID
	}

	online, err := h.presence.Online(ctx, userIDs)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.Presence", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"online": online})
}

// CreateUpload выдает ссылку для загрузки вложения методом PUT.
// POST /api/v1/chats/:chat_id/attachments
func (h *Handler) CreateUpload(c echo.Context) error {
	ctx := c.Request().Context()

	var req CreateUploadRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	upload, err := h.attachments.CreateUpload(ctx, mddlwr.UserID(c), req.ChatID, req.FileName, req.Size)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.CreateUpload", err)
	}
	return c.JSON(http.StatusCreated, upload)
}

// CreateDownload выдает ссылку для скачивания вложения.
// GET /api/v1/chats/:chat_id/attachments?key=xxx
func (h *Handler) CreateDownload(c echo.Context) error {
	ctx := c.Request().Context()

	var req CreateDownloadRequest
	if err := httpx.Bind(c, &req); err != nil {
		return err
	}

	download, err := h.attachments.CreateDownload(ctx, mddlwr.UserID(c), req.ChatID, req.Key)
	if err != nil {
		return httpx.Error(ctx, h.logger, "Handler.CreateDownload", err)
	}
	return c.JSON(http.StatusOK, download)
}
