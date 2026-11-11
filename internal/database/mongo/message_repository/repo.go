package message_repository

import (
	"context"
	"log/slog"

	mongodb "main/internal/database/mongo"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const messagesCollection = "messages"

// notDeleted - условие фильтра "сообщение не удалено"
var notDeleted = bson.M{"$exists": false}

type MessageRepository struct {
	coll   *mongo.Collection
	logger *slog.Logger
}

func NewMessageRepository(db *mongo.Database, logger *slog.Logger) *MessageRepository {
	return &MessageRepository{
		coll:   db.Collection(messagesCollection),
		logger: logger,
	}
}

// EnsureIndexes создает индексы коллекции сообщений. Операция идемпотентна.
func (r *MessageRepository) EnsureIndexes(ctx context.Context) error {
	const op = "MessageRepository.EnsureIndexes"

	indexes := []mongo.IndexModel{
		{
			// Листинг сообщений чата с курсорной пагинацией по _id (ULID)
			Keys: bson.D{
				{Key: "chat_id", Value: 1},
				{Key: "_id", Value: -1},
			},
		},
		{
			// Полнотекстовый поиск внутри чата, только по текстовым сообщениям
			Keys: bson.D{
				{Key: "chat_id", Value: 1},
				{Key: "content", Value: "text"},
			},
			Options: options.Index().SetPartialFilterExpression(
				bson.D{{Key: "type", Value: "text"}},
			),
		},
		{
			// Идемпотентность отправки: один клиентский ID - одно сообщение.
			// Индекс частичный, потому что клиентский ID необязателен.
			Keys: bson.D{
				{Key: "chat_id", Value: 1},
				{Key: "sender_id", Value: 1},
				{Key: "client_msg_id", Value: 1},
			},
			Options: options.Index().
				SetUnique(true).
				SetPartialFilterExpression(bson.D{
					{Key: "client_msg_id", Value: bson.D{{Key: "$type", Value: "string"}}},
				}),
		},
	}

	if _, err := r.coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return r.mapError(ctx, op, err)
	}

	r.logger.DebugContext(ctx, "indexes ensured for messages collection", "op", op)
	return nil
}

func (r *MessageRepository) mapError(ctx context.Context, op string, err error, attrs ...any) error {
	return mongodb.MapError(ctx, r.logger, op, err, attrs...)
}
