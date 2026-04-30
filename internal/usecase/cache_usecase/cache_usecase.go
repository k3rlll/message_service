package cache_usecase

import (
	"context"
	"log/slog"

	"github.com/maypok86/otter/v2"
)

type ChatMembers map[string]struct{} // пустая структура для экономии памяти

type ChatRepository interface {
	GetChatUsers(ctx context.Context, chatID string) ([]string, error)
}

type InMemoryCache struct {
	client   *otter.Cache[string, ChatMembers] // chatID - userIDs
	logger   *slog.Logger
	chatRepo ChatRepository
}

func NewInMemoryCache(
	client *otter.Cache[string, ChatMembers],
	logger *slog.Logger,
	chatRepo ChatRepository) *InMemoryCache {
	return &InMemoryCache{
		client:   client,
		logger:   logger,
		chatRepo: chatRepo,
	}
}

func (c *InMemoryCache) ChatUserIDs(chatID string) ([]string, error) {

}

func (c *InMemoryCache) IsUserInChat(ctx context.Context, userID, chatID string) (bool, error) {
	const op = "InMemoryCache.IsUserInChat"

	c.logger.InfoContext(
		ctx, "Checking if user is in chat",
		"op", op,
		"chatID", chatID,
		"userID", userID)

	members, ok := c.client.Get(ctx, chatID, otter.LoaderFunc[string, ChatMembers](
		func(ctx context.Context, key string) (ChatMembers, error) {
			membersList, err := c.chatRepo.GetChatUsers(ctx, chatID)
			if err != nil {
				if 
				c.logger.ErrorContext(
					ctx, "Failed to load chat members from repository",
					"op", op,
					"chatID", chatID,
					"error", err)
				return nil, err
			}
			return nil, nil
		},
	))

	if !ok {
		c.logger.InfoContext(ctx, "Cache miss", "chatID", chatID)
		// Опять же, логика похода в БД (или возврат ошибки, чтобы слой выше сам сходил в БД)
		return false, nil
	}

	return false, nil
}
