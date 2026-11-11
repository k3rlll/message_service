package cache_usecase

import (
	"context"
	"fmt"
	"log/slog"

	domainComm "main/internal/domain"
	domainChat "main/internal/domain/chat_entity"

	"github.com/maypok86/otter/v2"
)

// ChatMembers - участники чата: userID - роль
type ChatMembers map[string]string

type ChatRepository interface {
	ChatMembers(ctx context.Context, chatID string) ([]domainChat.Member, error)
}

// InMemoryCache кэширует состав и роли участников чата.
// Нужен на горячем пути: проверка прав на каждое сообщение и рассылка событий.
//
// Кэш локален для экземпляра сервиса. При изменении состава чата его сбрасывает
// шина событий на каждом экземпляре, TTL - страховка на случай потери события.
type InMemoryCache struct {
	client   *otter.Cache[string, ChatMembers]
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

// MemberRole возвращает роль пользователя в чате.
// Если пользователь не участник, возвращает ErrAccessDenied,
// если чата нет - chat_entity.ErrChatNotFound.
func (c *InMemoryCache) MemberRole(ctx context.Context, userID, chatID string) (string, error) {
	const op = "InMemoryCache.MemberRole"

	members, err := c.members(ctx, chatID)
	if err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}

	role, ok := members[userID]
	if !ok {
		return "", fmt.Errorf("%s: %w", op, domainComm.ErrAccessDenied)
	}
	return role, nil
}

// ChatUserIDs возвращает ID всех участников чата.
func (c *InMemoryCache) ChatUserIDs(ctx context.Context, chatID string) ([]string, error) {
	const op = "InMemoryCache.ChatUserIDs"

	members, err := c.members(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	userIDs := make([]string, 0, len(members))
	for userID := range members {
		userIDs = append(userIDs, userID)
	}
	return userIDs, nil
}

// Invalidate сбрасывает кэш участников чата.
// Вызывать при любом изменении состава или ролей.
func (c *InMemoryCache) Invalidate(chatID string) {
	c.client.Invalidate(chatID)
}

// members возвращает состав чата из кэша, при промахе загружает его из репозитория.
// Ошибки загрузки не кэшируются.
func (c *InMemoryCache) members(ctx context.Context, chatID string) (ChatMembers, error) {
	return c.client.Get(ctx, chatID, otter.LoaderFunc[string, ChatMembers](
		func(ctx context.Context, chatID string) (ChatMembers, error) {
			c.logger.DebugContext(ctx, "cache miss, loading chat members", "chatID", chatID)

			list, err := c.chatRepo.ChatMembers(ctx, chatID)
			if err != nil {
				return nil, err
			}

			members := make(ChatMembers, len(list))
			for _, member := range list {
				members[member.UserID] = member.Role
			}
			return members, nil
		},
	))
}
