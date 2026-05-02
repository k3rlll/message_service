package usecase

import (
	"context"
	"fmt"

	domainComm "main/internal/domain"
	domain "main/internal/domain/message_entity"
	"strings"
	"time"

	"github.com/google/uuid"
)

type messageRepository interface {
	SaveMessage(ctx context.Context, msg *domain.Message) error

	//
	ListMessages(
		ctx context.Context,
		chatID,
		anchor string,
		limit int64) ([]domain.Message, error)

	//
	DeleteMessages(ctx context.Context, chatID string, messageIDs []string) error

	//
	UpdateMessage(
		ctx context.Context,
		userID string,
		chatID string,
		messageID string,
		content string) error

	//
	SearchMessages(
		ctx context.Context,
		chatID string,
		searchText string,
		anchorID string,
		limit int64) ([]domain.Message, error)
}

type MessageUsecase struct {
	repo messageRepository
}

func NewUsecase(repo messageRepository) *MessageUsecase {
	return &MessageUsecase{
		repo: repo,
	}
}

func (u *MessageUsecase) SaveMessage(ctx context.Context, req domain.Message) error {

	//

	req.ID = uuid.New().String()
	req.CreatedAt = time.Now().UTC()
	req.UpdatedAt = time.Now().UTC()

	//

	err := u.repo.SaveMessage(ctx, &req)
	if err != nil {
		//TODO: Handle error properly
		return fmt.Errorf("failed to save message: %w", err)
	}

	//

	return nil
}

func (u *MessageUsecase) ListMessages(
	ctx context.Context,
	chatID string,
	anchor string,
	limit int64,
) ([]domain.Message, bool, error) {
	const op = "MessageUsecase.ListMessages"

	// Запрашиваем на 1 сообщение больше чтобы узнать есть ли еще страницы
	messages, err := u.repo.ListMessages(ctx, chatID, anchor, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	hasMore := len(messages) > int(limit)
	if hasMore {
		messages = messages[:limit]
	}

	return messages, hasMore, nil
}

func (u *MessageUsecase) SearchMessages(
	ctx context.Context,
	chatID string,
	searchText string,
	anchorID string,
	limit int64,
) ([]domain.Message, bool, error) {
	const op = "MessageUsecase.SearchMessages"

	// Запрашиваем на 1 элемент больше для проверки наличия следующей страницы
	messages, err := u.repo.SearchMessages(ctx, chatID, searchText, anchorID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	hasMore := len(messages) > int(limit)
	if hasMore {
		// Отрезаем лишний элемент перед отправкой
		messages = messages[:limit]
	}

	return messages, hasMore, nil
}

func (u *MessageUsecase) DeleteMessages(
	ctx context.Context,
	userID string,
	chatID string,
	messageIDs []string,
) error {
	const op = "MessageUsecase.DeleteMessages"

	err := u.repo.DeleteMessages(ctx, chatID, messageIDs)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	//TODO: Опубликовать событие удаления сообщений, чтобы другие клиенты могли обновить UI
	// u.broker.Publish(ctx, chatID, MessageDeletedEvent{IDs: messageIDs})

	return nil
}

func (u *MessageUsecase) UpdateMessage(
	ctx context.Context,
	userID string,
	chatID string,
	messageID string,
	content string,
) error {
	const op = "MessageUsecase.UpdateMessage"

	//убираем лишние пробелы по краям
	cleanContent := strings.TrimSpace(content)
	if cleanContent == "" {
		return fmt.Errorf("%s: %w", op, domainComm.ErrInvalidInput)
	}

	err := u.repo.UpdateMessage(ctx, userID, chatID, messageID, cleanContent)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	//TODO: публикация события в брокер
	// u.broker.Publish(ctx, chatID, MessageUpdatedEvent{ID: messageID, Content: cleanContent})

	return nil
}
