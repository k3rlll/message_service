package redis_pub_sub

import (
	"context"
	"encoding/json"
	"log/slog"

	ws "main/internal/ws"

	"github.com/redis/go-redis/v9"
)

type PubSub struct {
	Client *redis.Client

	cache  InMemoryCache
	logger *slog.Logger
}

func NewPubSub(client *redis.Client, cache InMemoryCache, logger *slog.Logger) PubSub {
	return PubSub{
		Client: client,

		cache:  cache,
		logger: logger,
	}
}

// Usecase логика, которая нужна клиенту для проверки прав доступа
// и получения информации о чатах
type InMemoryCache interface {
	ChatUserIDs(ctx context.Context, chatID string) ([]string, error)
}

type RedisRepoInterface interface {
	Set(ctx context.Context, key string, value interface{}) error
	Get(ctx context.Context, key string) (string, error)
}

// StartRedisSubscriber запускает горутину, которая подписывается на канал Redis
// и обрабатывает входящие сообщения
func (r *PubSub) StartRedisSubscriber(
	ctx context.Context,
	rdb *redis.Client,
	hub *ws.Hub) {

	op := "PubSub.StartRedisSubscriber"
	r.logger.InfoContext(ctx, "Starting Redis subscriber", "op", op)

	pubsub := rdb.Subscribe(ctx, "channel:chat_events")
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			r.logger.InfoContext(
				ctx, "Shutting down Redis subscriber",
				"op", op,
			)
			return
		case msg := <-ch:

			var outMsg ws.OutgoingMessage

			if err := json.Unmarshal([]byte(msg.Payload), &outMsg); err != nil {
				r.logger.ErrorContext(
					ctx, "Failed to unmarshal Redis message",
					"op", op,
					"err", err,
				)
				continue
			}

			// Узнаем, кому доставить сообщение.
			// Идем в Redis Set (или кэш), где хранятся участники ChatID
			// members := rdb.SMembers(ctx, "chat_members:" + outMsg.ChatID).Val()
			members, err := r.cache.ChatUserIDs(ctx, outMsg.ChatID)
			if err != nil {
				//TODO: обработка ошибок
				r.logger.ErrorContext(
					ctx, "Failed to get chat user IDs",
					"op", op,
					"chatID", outMsg.ChatID,
					"err", err,
				)
				continue
			}

			// Отправляем задачу в Хаб
			hub.Deliver <- ws.DeliveryTask{
				TargetUserIDs: members,
				Payload:       []byte(msg.Payload),
			}
		}
	}
}
