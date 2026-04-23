package redis_pub_sub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	domain "main/internal/domain/message_entity"

	"github.com/redis/go-redis/v9"
)

type RdbRepo struct {
	Client *redis.Client
	logger *slog.Logger
}

func RedisNewClient(client *redis.Client, logger *slog.Logger) RdbRepo {
	return RdbRepo{
		Client: client,
		logger: logger,
	}
}

func (r *RdbRepo) PublishMessage(
	ctx context.Context,
	chatID string,
	message domain.Message) error {

	channel := fmt.Sprintf("chat:%s", chatID)

	msgBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	//

	err = r.Client.Publish(ctx, channel, msgBytes).Err()
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}
	return nil
}

func (r *RdbRepo) SubscribeToChat(
	ctx context.Context,
	chatID string) (<-chan domain.Message, error) {

	const op = "RdbRepo.SubscribeToChat"

	channel := fmt.Sprintf("chat:%s", chatID)
	pubsub := r.Client.Subscribe(ctx, channel)

	// Канал для отправки сообщений в обработчик
	msgChan := make(chan domain.Message)

	// Запускаем горутину для обработки входящих сообщений
	go func() {
		defer pubsub.Close()
		for {
			msg, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				r.logger.ErrorContext(
					ctx, "Error receiving message",
					"op", op, "err", err)
				continue
			}

			var message domain.Message
			err = json.Unmarshal([]byte(msg.Payload), &message)
			if err != nil {
				r.logger.ErrorContext(
					ctx, "Error unmarshaling message",
					"op", op, "err", err)
				continue
			}

			msgChan <- message
		}
	}()

	return msgChan, nil
}
