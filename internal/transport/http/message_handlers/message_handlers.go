package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	configs "main/internal/configs"
	domainComm "main/internal/domain"
	domain "main/internal/domain/message_entity"
	ws "main/internal/ws"

	"github.com/coder/websocket"
	"github.com/go-playground/validator/v10"
	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"
)

type messageUsecase interface {
	SaveMessage(ctx context.Context, req domain.Message) error

	ListMessages(
		ctx context.Context,
		chatID string,
		anchor string,
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
		chatID string,
		searchText string,
		anchorID string,
		limit int64,
	) ([]domain.Message, bool, error)
}

type redisUsecase interface {
	PublishMessage(
		ctx context.Context,
		chatID string,
		message domain.Message) error
	//
	SubscribeToChat(
		ctx context.Context,
		chatID string) (<-chan domain.Message, error)
}

type Handler struct {
	echo         *echo.Echo
	logger       *slog.Logger
	usecase      messageUsecase
	redisUsecase redisUsecase
	cfg          configs.Config
}

func NewHandler(
	echo *echo.Echo,
	logger *slog.Logger,
	usecase messageUsecase,
	redisUsecase redisUsecase,
	cfg configs.Config) *Handler {
	return &Handler{
		echo:         echo,
		logger:       logger,
		usecase:      usecase,
		redisUsecase: redisUsecase,
		cfg:          cfg,
	}
}

// DTO

type SaveMessageRequest struct {
	ChatID   string         `json:"chat_id" validate:"required,ulid"`
	SenderID string         `json:"sender_id" validate:"required,ulid"`
	Type     string         `json:"type" validate:"required,oneof=text image video audio system"`
	Content  string         `json:"content" validate:"required"`
	Metadata map[string]any `json:"metadata"`
}

type ListMessagesRequest struct {
	ChatID   string `query:"chat_id" validate:"required,ulid"`
	AnchorID string `query:"anchor,ulid"` // optional, for pagination
	Limit    int64  `query:"limit" validate:"omitempty,min=1,max=100"`
}

type ListMessagesResponse struct {
	Messages []domain.Message `json:"messages"`
	HasMore  bool             `json:"has_more"` // optional, indicates if there are more messages to fetch
}

type DeleteMessagesRequest struct {
	ChatID     string   `query:"chat_id" validate:"required,uiid"`
	MessageIDs []string `query:"message_ids" validate:"required,dive,uiid"`
}

type UpdateMessageRequest struct {
	ChatID    string `param:"chat_id" validate:"required"`
	MessageID string `param:"message_id" validate:"required"`
	Content   string `json:"content" validate:"required,min=1,max=4000"`
}

type SearchMessagesRequest struct {
	ChatID   string `query:"chat_id" validate:"required,uiid"`
	Text     string `query:"text" validate:"required"`
	AnchorID string `query:"anchor_id"`
	Limit    int64  `query:"limit" validate:"omitempty,min=1,max=50"`
}

type SearchMessagesResponse struct {
	Messages []domain.Message `json:"messages"`
	HasMore  bool             `json:"has_more"` // optional, indicates if there are more messages to fetch
}

// Handler для WebSocket соединений. Апгрейдит HTTP в WS, создает клиента и регистрирует его в Хабе.
func (h *Handler) WSHandler(hub *ws.Hub, rdb *redis.Client) echo.HandlerFunc {
	return func(c echo.Context) error {
		op := "Handler.WSHandler"
		userID := c.Get("user_id").(string)
		ctx := c.Request().Context()

		h.logger.InfoContext(
			ctx, "WebSocket connection attempt",
			"op", op,
			"userID", userID,
		)

		// Апгрейд соединения
		conn, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{
			InsecureSkipVerify: true, // Настроить OriginPatterns в проде!
		})
		if err != nil {
			h.logger.ErrorContext(
				ctx, "Failed to accept WebSocket connection",
				"op", op,
				"userID", userID,
				"error", err,
			)
			return err // Ошибка рукопожатия
		}

		client := &ws.Client{
			Hub:    hub,
			Conn:   conn,
			UserID: userID,
			Send:   make(chan []byte, h.cfg.Websocket.ClientChanSize),
		}
		h.logger.DebugContext(
			ctx, "WebSocket client created",
			"op", op,
			"client", client,
		)

		h.logger.InfoContext(
			ctx, "WebSocket connection established",
			"op", op,
			"userID", userID,
		)

		// Регистрация в Хабе
		hub.Register <- client

		// Запускаем writePump в фоне
		go client.WritePump(c.Request().Context())

		// Запускаем readPump В ТЕКУЩЕЙ горутине - блокирующий вызов
		// Хэндлер будет висеть здесь, пока клиент не отключится.
		client.ReadPump(c.Request().Context(), rdb)

		return nil
	}
}

// get messages/list?chat_id=xxx&anchor=xxx&limit=xxx
func (h *Handler) ListMessages(c echo.Context) error {
	const op = "Handler.ListMessages"
	ctx := c.Request().Context()

	var req ListMessagesRequest
	if err := c.Bind(&req); err != nil {
		h.logger.WarnContext(
			ctx, "failed to bind request",
			"op", op, "error", err)

		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "request body is empty")
		}
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if err := c.Validate(&req); err != nil {
		h.logger.WarnContext(
			ctx, "validation failed",
			"op", op, "error", err)

		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return echo.NewHTTPError(http.StatusBadRequest, "validation error")
	}

	// Устанавливаем разумные дефолты и жесткие верхние границы
	if req.Limit <= 0 {
		req.Limit = 20
	} else if req.Limit > 100 { // Защита от выгрузки всей БД в RAM
		req.Limit = 100
	}

	// Якорь AnchorID передаем как есть даже пустой, репозиторий сам с ним разберется
	messages, hasMore, err := h.usecase.ListMessages(ctx, req.ChatID, req.AnchorID, req.Limit)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to process list messages request",
			"op", op,
			"chatID", req.ChatID,
			"error", err,
		)

		if errors.Is(err, domainComm.ErrDatabaseTimeout) {
			return echo.NewHTTPError(
				http.StatusServiceUnavailable,
				"service temporarily unavailable")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, ListMessagesResponse{
		Messages: messages,
		HasMore:  hasMore,
	})
}

// delete /messages?chat_id=xxx&message_id=xxx
func (h *Handler) DeleteMessages(c echo.Context) error {
	const op = "Handler.DeleteMessages"
	ctx := c.Request().Context()

	userID := c.Get("user_id").(string)

	var req DeleteMessagesRequest
	if err := c.Bind(&req); err != nil {
		h.logger.WarnContext(
			ctx, "failed to bind delete request",
			"op", op, "error", err)

		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "request body is empty")
		}
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if err := c.Validate(&req); err != nil {
		h.logger.WarnContext(
			ctx, "validation failed for delete request",
			"op", op, "error", err)

		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return echo.NewHTTPError(http.StatusBadRequest, "validation error")
	}

	if len(req.MessageIDs) == 0 {
		//Возвращаем 204 No Content для идемпотентности
		return c.NoContent(http.StatusNoContent)
	}

	if len(req.MessageIDs) > 100 {
		return echo.NewHTTPError(http.StatusBadRequest, "cannot delete more than 100 messages at once")
	}

	err := h.usecase.DeleteMessages(ctx, userID, req.ChatID, req.MessageIDs)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to process delete messages request",
			"op", op,
			"userID", userID,
			"chatID", req.ChatID,
			"error", err,
		)

		// Маппинг доменных ошибок в HTTP статусы
		if errors.Is(err, domainComm.ErrAccessDenied) {
			return echo.NewHTTPError(http.StatusForbidden, "you can only delete your own messages")
		}
		if errors.Is(err, domainComm.ErrDatabaseTimeout) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "service temporarily unavailable")
		}

		return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
	}

	return c.NoContent(http.StatusNoContent)
}

// put /messages?chat_id=xxx&message_id=xxx
func (h *Handler) UpdateMessage(c echo.Context) error {
	const op = "Handler.UpdateMessage"
	ctx := c.Request().Context()

	userID, ok := c.Get("user_id").(string)
	if !ok || userID == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized access")
	}

	var req UpdateMessageRequest

	if err := c.Bind(&req); err != nil {
		h.logger.WarnContext(ctx, "failed to bind update request",
			"op", op, "error", err)

		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "request body is empty")
		}
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if err := c.Validate(&req); err != nil {
		h.logger.WarnContext(ctx, "validation failed for update request",
			"op", op, "error", err)

		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return echo.NewHTTPError(http.StatusBadRequest, "validation error")
	}

	err := h.usecase.UpdateMessage(ctx, userID, req.ChatID, req.MessageID, req.Content)
	if err != nil {
		h.logger.ErrorContext(ctx, "failed to update message",
			"op", op,
			"userID", userID,
			"messageID", req.MessageID,
			"error", err,
		)

		if errors.Is(err, domainComm.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "message not found or you do not have permission to edit it")
		}
		if errors.Is(err, domainComm.ErrInvalidInput) {
			return echo.NewHTTPError(http.StatusBadRequest, "message content cannot be empty")
		}
		if errors.Is(err, domainComm.ErrDatabaseTimeout) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "service temporarily unavailable")
		}

		return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
	}

	return c.NoContent(http.StatusNoContent)
}

// get messages/search?chat_id=xxx&text=xxx
func (h *Handler) SearchMessages(c echo.Context) error {
	const op = "Handler.SearchMessages"
	ctx := c.Request().Context()

	var req SearchMessagesRequest
	if err := c.Bind(&req); err != nil {
		h.logger.WarnContext(
			ctx, "failed to bind search request",
			"op", op, "error", err)

		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "request body is empty")
		}
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request format")
	}

	if err := c.Validate(&req); err != nil {
		h.logger.WarnContext(
			ctx, "validation failed for search request",
			"op", op, "error", err)

		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return echo.NewHTTPError(http.StatusBadRequest, "validation error")
	}

	//  Жесткие лимиты
	// так как здесь поиск тяжелее обычного List
	if req.Limit <= 0 {
		req.Limit = 20
	} else if req.Limit > 50 {
		// Для $text поиска лимит 50 - это  максимум так как БД начнет страдать
		req.Limit = 50
	}

	// Вызываем юзкейс
	messages, hasMore, err := h.usecase.SearchMessages(
		ctx,
		req.ChatID,
		req.Text,
		req.AnchorID,
		req.Limit)
	if err != nil {
		// Логгируем на границе системы с полным стеком
		h.logger.ErrorContext(ctx, "failed to search messages",
			"op", op,
			"chatID", req.ChatID,
			"error", err,
		)

		// Переводим доменные ошибки в понятные HTTP статусы
		if errors.Is(err, domainComm.ErrDatabaseTimeout) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, "search service is temporarily overloaded")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
	}

	// Возвращаем результат
	return c.JSON(http.StatusOK, SearchMessagesResponse{
		Messages: messages,
		HasMore:  hasMore, // Фронтенд теперь знает, нужно ли показывать кнопку "Показать еще"
	})
}

// функция помошник для форматирования ошибок валидации
//
// в более читаемый формат для клиента
func formatValidationError(err error) map[string]string {
	erro := make(map[string]string)
	var vErrs validator.ValidationErrors

	if errors.As(err, &vErrs) {
		for _, f := range vErrs {
			erro[f.Field()] = fmt.Sprintf("failed on the '%s' tag", f.Tag())
		}
	} else {
		erro["error"] = err.Error()
	}
	return erro
}
