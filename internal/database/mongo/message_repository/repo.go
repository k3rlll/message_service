package repository

import (
	"context"
	"fmt"
	"log/slog"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	messagesCollection = "messages"
)

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

// create indexes to speed up operations
func (r *MessageRepository) EnsureIndexes(ctx context.Context) error {
	//
	const op = "MessageRepository.EnsureIndexes"

	indexModel := mongo.IndexModel{
		Keys: bson.D{
			{Key: "chat_id", Value: 1},
			{Key: "content", Value: "text"},
		},
		Options: options.Index().SetPartialFilterExpression(
			bson.D{{Key: "type", Value: "text"}},
		),
	}

	//

	_, err := r.coll.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		r.logger.ErrorContext(
			ctx,
			"op", op,
			"failed to create indexes for messages collection",
			slog.String("error", err.Error()))
		return fmt.Errorf("failed to create indexes for messages collection: %w", err)
	}

	r.logger.DebugContext(
		ctx, "op", op,
		"indexes created successfully for messages collection")
	return nil
}
