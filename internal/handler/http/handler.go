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
	"github.com/oklog/ulid/v2"
)

type Usecase interface {
	//
	SaveMessage(ctx context.Context, req models.Message) error
	//

	ListMessages(
		ctx context.Context,
		chatID ulid.ULID,
		anchor ulid.ULID,
		limit int64) ([]models.Message, bool, error)
	//

	DeleteMessage(ctx context.Context, chatID ulid.ULID, messageID ulid.ULID) error

	//

	UpdateMessage(ctx context.Context, chatID ulid.ULID, messageID ulid.ULID, content string) error

	//

	GetMessageByText(ctx context.Context, chatID ulid.ULID, text string) error
}

type Handler struct {
	echo    *echo.Echo
	usecase Usecase
}

func NewHandler(echo *echo.Echo, usecase Usecase) *Handler {
	return &Handler{
		echo:    echo,
		usecase: usecase,
	}
}

// MessageRequest represents the payload for creating a new message. DTO.
type MessageRequest struct {
	ChatID   string         `json:"chat_id" validate:"required"`
	SenderID string         `json:"sender_id" validate:"required"`
	Type     string         `json:"type" validate:"required,oneof=text image video audio system"`
	Content  string         `json:"content" validate:"required"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// post /messages
func (h *Handler) SaveMessage(c echo.Context) error {

	var req MessageRequest
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

	chatID, err := ulid.Parse(req.ChatID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid chat ID")
	}

	//

	//

	senderID, err := ulid.Parse(req.SenderID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid sender ID")
	}

	//

	//

	domainMsg := models.Message{
		ChatID:   chatID,
		SenderID: senderID,
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
	Messages     []models.Message `json:"messages"`
	HasMore      bool             `json:"has_more"`                // optional, indicates if there are more messages to fetch
	LastMessage  ulid.ULID        `json:"last_message,omitempty"`  // optional, for pagination
	FirstMessage ulid.ULID        `json:"first_message,omitempty"` // optional, for pagination
}

// get messages/?chat_id=xxx&anchor=xxx&limit=xxx
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

	//
	chatID, err := ulid.Parse(req.ChatID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid chat ID")
	}
	AnchorID, err := ulid.Parse(req.AnchorID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid anchor ID")
	}

	//

	listMessages, HasMore, err := h.usecase.ListMessages(c.Request().Context(), chatID, AnchorID, req.Limit)
	if err != nil {
		//TODO: Handle errors properly (database errors, etc.)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to list messages")
	}

	return c.JSON(http.StatusOK, ListMessagesResponse{
		Messages:     listMessages,
		HasMore:      HasMore,
		LastMessage:  listMessages[len(listMessages)-1].ID, // for pagination
		FirstMessage: listMessages[0].ID,                   // for pagination
	})
}

//============================================================================

type DeleteMessageRequest struct {
	ChatID    string `json:"chat_id" validate:"required,ulid"`
	MessageID string `json:"message_id" validate:"required,ulid"`
}

// delete /messages?chat_id=xxx&message_id=xxx
func (h *Handler) DeleteMessage(c echo.Context) error {
	var req DeleteMessageRequest
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

	//

	chatID, err := ulid.Parse(req.ChatID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid chat ID")
	}

	messageID, err := ulid.Parse(req.MessageID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid message ID")
	}

	//

	if err := h.usecase.DeleteMessage(c.Request().Context(), chatID, messageID); err != nil {
		//TODO: Handle errors properly (message not found, database errors, etc.)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete message")
	}

	return c.NoContent(http.StatusNoContent)
}

type UpdateMessageRequest struct {
	ChatID    string `query:"chat_id" validate:"required,ulid"`
	MessageID string `query:"message_id" validate:"required,ulid"`
	Content   string `json:"content" validate:"required,eq=text"` //has to be "text" because updating pics, vids etc. is permitted
}

// put /messages?chat_id=xxx&message_id=xxx
func (h *Handler) UpdateMessage(c echo.Context) error {
	var req UpdateMessageRequest
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
	chatID, err := ulid.Parse(req.ChatID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid chat ID")
	}

	//

	//

	messageID, err := ulid.Parse(req.MessageID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid message ID")
	}

	//

	//
	err = h.usecase.UpdateMessage(c.Request().Context(), chatID, messageID, req.Content)
	if err != nil {
		//TODO: handle error better
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update message")
	}
	//
	return c.NoContent(http.StatusNoContent)
	//
}

//============================================================================

// get messages/?chat_id=xxx&text=xxx

type SearchMessagesRequest struct {
	ChatID string `query:"chat_id" validate:"required,ulid"`
	Text   string `query:"text" validate:"required"`
}

type SearchMessagesResponse struct {
	Messages []models.Message `json:"messages"`
}

func (h *Handler) SearchMessages(c echo.Context) error {
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
	//

	//

	//
	h.usecase.GetMessageByText()
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
