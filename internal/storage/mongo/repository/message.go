package repository

import (
	"context"
	"fmt"

	domain "main/internal/domain/message_entity"

	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (r *MessageRepository) SaveMessage(ctx context.Context, msg *domain.Message) error {

	_, err := r.coll.InsertOne(ctx, msg)
	if err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}

	//

	return nil
}

func (r *MessageRepository) ListMessages(
	ctx context.Context,
	chatID string,
	anchorID string,
	limit int64) ([]domain.Message, error) {

	//

	filter := bson.M{
		"chat_id": chatID,
		"_id":     bson.M{"$lt": anchorID},
	}

	//

	opt := options.Find().
		SetSort(bson.D{{Key: "_id", Value: -1}}).
		SetLimit(limit)

	//

	cur, err := r.coll.Find(ctx, filter, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}
	defer cur.Close(ctx)

	//

	var messages []domain.Message
	if err = cur.All(ctx, &messages); err != nil {
		return nil, fmt.Errorf("failed to decode message results: %w", err)
	}
	fmt.Println("Found messages\n", messages)

	return messages, nil
}

func (r *MessageRepository) GetMessageByText(
	ctx context.Context,
	chatID string,
	text string,
	anchorID string,
) ([]domain.Message, error) {

	filter := bson.M{
		"chat_id":    chatID,
		"type":       "text",
		"created_at": bson.M{"$gt": time.Now().Add(-60 * 24 * time.Hour)},
		"$text":      bson.M{"$search": text},
	}

	if anchorID != "" {
		filter["_id"] = bson.M{"$lt": anchorID}
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "_id", Value: -1}}).
		SetLimit(51)

	//

	cursor, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search query: %w", err)
	}
	defer cursor.Close(ctx)

	var messages []domain.Message

	//

	if err = cursor.All(ctx, &messages); err != nil {
		return nil, fmt.Errorf("failed to decode search results: %w", err)
	}

	return messages, nil
}

func (r *MessageRepository) UpdateMessage(ctx context.Context, chatID string, messageID string, content string) error {
	filter := bson.M{
		"_id":     messageID,
		"chat_id": chatID,
	}
	update := bson.M{
		"$set": bson.M{
			"content":    content,
			"updated_at": time.Now().UTC(),
		},
	}
	_, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to update message: %w", err)
	}
	return nil
}

func (r *MessageRepository) DeleteMessages(ctx context.Context, chatID string, messageIDs []string) error {
	filter := bson.M{
		"_id":     bson.M{"$in": messageIDs},
		"chat_id": chatID,
	}

	if len(messageIDs) == 1 {
		_, err := r.coll.DeleteOne(ctx, filter)
		if err != nil {
			return fmt.Errorf("failed to delete message: %w", err)
		}
	} else {
		_, err := r.coll.DeleteMany(ctx, filter)
		if err != nil {
			return fmt.Errorf("failed to delete messages: %w", err)
		}
	}

	return nil
}
