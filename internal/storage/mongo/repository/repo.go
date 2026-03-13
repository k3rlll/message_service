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
	col *mongo.Collection
}

func NewMessageRepository(db *mongo.Database) *MessageRepository {
	return &MessageRepository{
		col: db.Collection(messagesCollection),
	}
}

func (r *MessageRepository) EnsureIndexes(ctx context.Context) error {
	//create unique indexes
	indexModel := mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetUnique(true),
	}

	_, err := r.col.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		return fmt.Errorf("failed to create indexes for users collection: %w", err)
	}
	return nil
}
