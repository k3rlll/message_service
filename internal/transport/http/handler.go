package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	configs "main/internal/configs"
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

	DeleteMessage(
		ctx context.Context,
		chatID string,
		messageID []string) error

	UpdateMessage(
		ctx context.Context,
		chatID string,
		messageID string,
		content string) error

	GetMessageByText(
		ctx context.Context,
		chatID string,
		text string,
		anchorID string) ([]domain.Message, error)
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
	ChatID     string   `query:"chat_id" validate:"required,ulid"`
	MessageIDs []string `query:"message_ids" validate:"required,dive,ulid"`
}

type UpdateMessageRequest struct {
	ChatID    string `query:"chat_id" validate:"required,ulid"`
	MessageID string `query:"message_id" validate:"required,ulid"`
	Content   string `json:"content" validate:"required"`
}

type SearchMessagesRequest struct {
	ChatID   string `query:"chat_id" validate:"required,ulid"`
	Text     string `query:"text" validate:"required"`
	AnchorID string `query:"anchor_id"`
}

type SearchMessagesResponse struct {
	Messages []domain.Message `json:"messages"`
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
	var req ListMessagesRequest
	if err := c.Bind(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "request body is empty")
		}
		return err
	}

	if err := c.Validate(&req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return fmt.Errorf("validation system error: %w", err)
	}

	if req.AnchorID == "" {
		// очень большой, чтобы начать с последних сообщений, если якорь не указан
		req.AnchorID = "99999999999999999999999999"
	}
	if req.Limit == 0 {
		req.Limit = 20
	}

	//

	listMessages, HasMore, err := h.usecase.ListMessages(c.Request().Context(), req.ChatID, req.AnchorID, req.Limit)
	if err != nil {
		//TODO: Handle errors properly (database errors, etc.)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list messages")
	}

	fmt.Println("Handler:", listMessages)
	return c.JSON(http.StatusOK, ListMessagesResponse{
		Messages: listMessages,
		HasMore:  HasMore,
	})
}

// delete /messages?chat_id=xxx&message_id=xxx
func (h *Handler) DeleteMessages(c echo.Context) error {
	var req DeleteMessagesRequest
	if err := c.Bind(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "request body is empty")
		}
		return err
	}

	//

	if err := c.Validate(&req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return err
	}

	//

	//

	if err := h.usecase.DeleteMessage(c.Request().Context(), req.ChatID, req.MessageIDs); err != nil {
		//TODO: Handle errors properly (message not found, database errors, etc.)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete message")
	}

	return c.NoContent(http.StatusNoContent)
}

// put /messages?chat_id=xxx&message_id=xxx
func (h *Handler) UpdateMessage(c echo.Context) error {
	var req UpdateMessageRequest

	if err := (&echo.DefaultBinder{}).BindQueryParams(c, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid query params")
	}

	if err := c.Bind(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		return err
	}

	if err := c.Validate(&req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return fmt.Errorf("validation system error: %w", err)
	}

	err := h.usecase.UpdateMessage(c.Request().Context(), req.ChatID, req.MessageID, req.Content)
	if err != nil {
		//TODO: handle error better
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update message")
	}

	return c.NoContent(http.StatusNoContent)

}

// get messages/search?chat_id=xxx&text=xxx
func (h *Handler) SearchMessagesByText(c echo.Context) error {
	var req SearchMessagesRequest
	if err := c.Bind(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		return err
	}

	if err := c.Validate(&req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return fmt.Errorf("validation system error: %w", err)
	}

	if req.AnchorID == "" {
		// очень большой ULID, чтобы начать с последних сообщений, если якорь не указан
		req.AnchorID = "99999999999999999999999999"
	}

	messages, err := h.usecase.GetMessageByText(c.Request().Context(), req.ChatID, req.Text, req.AnchorID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to search messages")
	}

	return c.JSON(http.StatusOK, SearchMessagesResponse{Messages: messages})
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
