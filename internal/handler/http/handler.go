package http

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"main/internal/domain"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
)

type Usecase interface {
	//
	SaveMessage(req domain.Message) error
}

type Handler struct {
	echo *echo.Echo
}

func NewHandler(echo *echo.Echo) *Handler {
	return &Handler{
		echo: echo,
	}
}

// MessageRequest represents the payload for creating a new message. DTO.
type MessageRequest struct {
	ChatID   string         `json:"chat_id" validate:"required"`
	SenderID string         `json:"sender_id" validate:"required"`
	Type     string         `json:"type" validate:"required,oneof=text image system"`
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
	if err := c.Validate(&req); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, formatValidationError(err))
	}

	domainMsg := domain.Message{
		ChatID:   primitive.ObjectIDHex(req.ChatID),
		SenderID: primitive.ObjectIDHex(req.SenderID),
		Type:     req.Type,
		Content:  req.Content,
		Metadata: req.Metadata,
	}

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
