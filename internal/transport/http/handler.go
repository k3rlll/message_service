package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"main/internal/models"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

// Usecase defines the interface for the business logic layer that the HTTP handler will interact with.
type Usecase interface {
	//
	SaveMessage(ctx context.Context, req models.Message) error
	//

	ListMessages(
		ctx context.Context,
		chatID string,
		anchor string,
		limit int64) ([]models.Message, bool, error)
	//

	DeleteMessage(ctx context.Context, chatID string, messageID []string) error

	//

	UpdateMessage(ctx context.Context, chatID string, messageID string, content string) error

	//

	GetMessageByText(ctx context.Context, chatID string, text string, anchorID string) ([]models.Message, error)
}

type RedisUsecase interface {
	PublishMessage(ctx context.Context, chatID string, message models.Message) error
}

type Handler struct {
	echo         *echo.Echo
	usecase      Usecase
	redisUsecase RedisUsecase
}

func NewHandler(echo *echo.Echo, usecase Usecase, redisUsecase RedisUsecase) *Handler {
	return &Handler{
		echo:         echo,
		usecase:      usecase,
		redisUsecase: redisUsecase,
	}
}

// MessageRequest represents the payload for creating a new message. DTO.
type SaveMessageRequest struct {
	ChatID   string         `json:"chat_id" validate:"required,ulid"`
	SenderID string         `json:"sender_id" validate:"required,ulid"`
	Type     string         `json:"type" validate:"required,oneof=text image video audio system"`
	Content  string         `json:"content" validate:"required"`
	Metadata map[string]any `json:"metadata"` // optional, system messages can have metadata like "event": "user_joined"
}

// post /messages
func (h *Handler) SendMessage(c echo.Context) error {

	var req SaveMessageRequest
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
		return fmt.Errorf("validation system error: %w", err)
	}

	//

	//

	domainMsg := models.Message{
		ChatID:   req.ChatID,
		SenderID: req.SenderID,
		Type:     req.Type,
		Content:  req.Content,
		Metadata: req.Metadata,
	}
	//

	//

	//

	if err := h.usecase.SaveMessage(c.Request().Context(), domainMsg); err != nil {
		//TODO: Handle errors properly (duplicate message, database errors, etc.)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save message")
	}

	return c.JSON(http.StatusCreated, domainMsg)
}

type ListMessagesRequest struct {
	ChatID   string `query:"chat_id" validate:"required,ulid"`
	AnchorID string `query:"anchor,ulid"` // optional, for pagination
	Limit    int64  `query:"limit" validate:"omitempty,min=1,max=100"`
}

type ListMessagesResponse struct {
	Messages []models.Message `json:"messages"`
	HasMore  bool             `json:"has_more"` // optional, indicates if there are more messages to fetch
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
	//

	//

	if err := c.Validate(&req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return fmt.Errorf("validation system error: %w", err)
	}

	if req.AnchorID == "" {
		req.AnchorID = "99999999999999999999999999" // a very large ULID to start from the latest messages
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

//============================================================================

type DeleteMessagesRequest struct {
	ChatID     string   `query:"chat_id" validate:"required,ulid"`
	MessageIDs []string `query:"message_ids" validate:"required,dive,ulid"`
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

type UpdateMessageRequest struct {
	ChatID    string `query:"chat_id" validate:"required,ulid"`
	MessageID string `query:"message_id" validate:"required,ulid"`
	Content   string `json:"content" validate:"required"`
}

// put /messages?chat_id=xxx&message_id=xxx
func (h *Handler) UpdateMessage(c echo.Context) error {
	var req UpdateMessageRequest

	// implicitly bind query params first, then bind JSON body for content
	// echo's default binder doesn't support binding query params and body in one step,
	// so we need to do it manually
	if err := (&echo.DefaultBinder{}).BindQueryParams(c, &req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid query params")
	}

	if err := c.Bind(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
		}
		return err
	}

	//

	if err := c.Validate(&req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return fmt.Errorf("validation system error: %w", err)
	}

	//

	//
	err := h.usecase.UpdateMessage(c.Request().Context(), req.ChatID, req.MessageID, req.Content)
	if err != nil {
		//TODO: handle error better
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update message")
	}
	//
	return c.NoContent(http.StatusNoContent)
	//
}

//============================================================================

type SearchMessagesRequest struct {
	ChatID   string `query:"chat_id" validate:"required,ulid"`
	Text     string `query:"text" validate:"required"`
	AnchorID string `query:"anchor_id"` // optional, for pagination
}

type SearchMessagesResponse struct {
	Messages []models.Message `json:"messages"`
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

	//

	//

	//

	if err := c.Validate(&req); err != nil {
		var validationErrs validator.ValidationErrors
		if errors.As(err, &validationErrs) {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
		}
		return fmt.Errorf("validation system error: %w", err)
	}

	if req.AnchorID == "" {
		req.AnchorID = "99999999999999999999999999" // a very large ULID to start from the latest messages
	}
	//

	//

	messages, err := h.usecase.GetMessageByText(c.Request().Context(), req.ChatID, req.Text, req.AnchorID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to search messages")
	}

	//

	return c.JSON(http.StatusOK, SearchMessagesResponse{Messages: messages})
}

func formatValidationError(err error) map[string]string {
	erro := make(map[string]string)
	var vErrs validator.ValidationErrors

	if errors.As(err, &vErrs) {
		for _, f := range vErrs {
			// f.Field() — name, f.Tag() — violated rule (required, email, etc.)
			erro[f.Field()] = fmt.Sprintf("failed on the '%s' tag", f.Tag())
		}
	} else {
		erro["error"] = err.Error()
	}
	return erro
}
