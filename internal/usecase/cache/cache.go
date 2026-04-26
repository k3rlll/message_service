package cache

import "log/slog"

type InMemoryCache struct {
	
	logger *slog.Logger
}

func NewInMemoryCache(logger *slog.Logger) *InMemoryCache {
	return &InMemoryCache{
		logger: logger,
	}
}


func (c *InMemoryCache) ChatUserIDs(chatID string) ([]string, error) {

}

func (c *InMemoryCache) IsUserInChat(userID, chatID string) (bool, error) {

}