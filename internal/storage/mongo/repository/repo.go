package repository

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var (
	messagesCollection = "messages"
)

type MessageRepository struct {
	coll *mongo.Collection
}

func NewMessageRepository(db *mongo.Database) *MessageRepository {
	return &MessageRepository{
		coll: db.Collection(messagesCollection),
	}
}

// create indexes to speed up operations
func (r *MessageRepository) EnsureIndexes(ctx context.Context) error {
	//

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
		return fmt.Errorf("failed to create indexes for messages collection: %w", err)
	}
	return nil
}
