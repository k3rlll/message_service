package cache_usecase

import (
	"context"
	"errors"
	"log/slog"

	domainChat "main/internal/domain/chat_entity"

	"github.com/maypok86/otter/v2"
)

type ChatMembers map[string]struct{} // пустая структура для экономии памяти

type ChatRepository interface {
	IsUserInChat(ctx context.Context, userID, chatID string) (bool, error)
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

func (c *InMemoryCache) IsUserInChat(ctx context.Context, userID, chatID string) (bool, error) {
	const op = "InMemoryCache.IsUserInChat"

	c.logger.InfoContext(
		ctx, "Checking if user is in chat",
		"op", op,
		"chatID", chatID,
		"userID", userID)

	members, err := c.client.Get(ctx, chatID, otter.LoaderFunc[string, ChatMembers](
		func(ctx context.Context, key string) (ChatMembers, error) {
			isInChat, err := c.chatRepo.IsUserInChat(ctx, userID, chatID)
			if err != nil {
				if errors.Is(err, domainChat.ErrChatNotFound) {
					c.logger.InfoContext(
						ctx, "Chat not found",
						"op", op,
						"chatID", chatID)
					return ChatMembers{}, nil
				}
				if errors.Is(err, domainChat.ErrAccessDenied) {
					c.logger.InfoContext(
						ctx, "Access denied",
						"op", op,
						"chatID", chatID)
					return ChatMembers{}, nil
				}
				//else
				c.logger.ErrorContext(
					ctx, "Failed to load chat members from repository",
					"op", op,
					"chatID", chatID,
					"error", err)

				return ChatMembers{}, err
			}

			if isInChat {
				c.logger.InfoContext(
					ctx, "Cache miss - user is in chat",
					"op", op,
					"chatID", chatID,
					"userID", userID)
				return ChatMembers{userID: {}}, nil
			}

			c.logger.InfoContext(
				ctx, "Cache miss - user is not in chat",
				"op", op,
				"chatID", chatID,
				"userID", userID)

			return ChatMembers{}, nil
		},
	))
	if err != nil {
		c.logger.InfoContext(
			ctx, "Cache miss",
			"op", op,
			"chatID", chatID)
		return false, nil
	}

	if _, ok := members[userID]; ok {
		c.logger.InfoContext(
			ctx, "Cache hit - user is in chat",
			"op", op,
			"chatID", chatID,
			"userID", userID)
		return true, nil
	}

	return false, nil
}
