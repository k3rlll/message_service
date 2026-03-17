package usecase

import (
	"context"
	"fmt"
	"main/internal/models"
	"time"

	"github.com/oklog/ulid/v2"
)

type Repo interface {
	SaveMessage(ctx context.Context, msg *models.Message) error

	//
	ListMessages(ctx context.Context, chatID, anchor string, limit int64) ([]models.Message, error)

	//
	DeleteMessages(ctx context.Context, chatID string, messageIDs []string) error

	//
	UpdateMessage(ctx context.Context, chatID string, messageID string, content string) error

	//
	GetMessageByText(ctx context.Context, chatID string, text string, anchorID string) ([]models.Message, error)
}

type Usecase struct {
	repo Repo
}

func NewUsecase(repo Repo) *Usecase {
	return &Usecase{
		repo: repo,
	}
}

func (u *Usecase) SaveMessage(ctx context.Context, req models.Message) error {

	//

	req.ID = ulid.Make().String()
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	//

	err := u.repo.SaveMessage(ctx, &req)
	if err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}

	//

	return nil
}

func (u *Usecase) ListMessages(
	ctx context.Context,
	chatID string,
	anchor string,
	limit int64) ([]models.Message, bool, error) {

	//

	messages, err := u.repo.ListMessages(ctx, chatID, anchor, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list messages: %w", err)
	}

	//

	HasMore := len(messages) > int(limit)
	if HasMore {
		messages = messages[:limit]
	}
	fmt.Println(messages)
	//

	return messages, HasMore, nil
}

func (u *Usecase) GetMessageByText(ctx context.Context, chatID string, text string, anchorID string) ([]models.Message, error) {

	//

	messages, err := u.repo.GetMessageByText(ctx, chatID, text, anchorID)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages by text: %w", err)
	}

	//

	return messages, nil
}

func (u *Usecase) DeleteMessage(ctx context.Context, chatID string, messageIDs []string) error {

	//

	err := u.repo.DeleteMessages(ctx, chatID, messageIDs)
	if err != nil {
		return fmt.Errorf("failed to delete message: %w", err)
	}

	//

	return nil
}

func (u *Usecase) UpdateMessage(ctx context.Context, chatID string, messageID string, content string) error {

	//

	if err := u.repo.UpdateMessage(ctx, chatID, messageID, content); err != nil {
		return fmt.Errorf("failed to update message: %w", err)
	}

	//

	return nil
}
