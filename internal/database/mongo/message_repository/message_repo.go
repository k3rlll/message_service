package message_repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainComm "main/internal/domain"
	domain "main/internal/domain/message_entity"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Поиск ограничен последними 60 днями для экономии ресурсов БД
const searchWindow = 60 * 24 * time.Hour

// SaveMessage сохраняет сообщение. Если сообщение с таким клиентским ID
// уже есть, возвращает ErrDuplicate.
func (r *MessageRepository) SaveMessage(ctx context.Context, msg *domain.Message) error {
	const op = "MessageRepository.SaveMessage"

	if _, err := r.coll.InsertOne(ctx, msg); err != nil {
		return r.mapError(ctx, op, err, "chatID", msg.ChatID, "senderID", msg.SenderID)
	}
	return nil
}

// GetMessage возвращает сообщение чата по ID, включая удаленные
func (r *MessageRepository) GetMessage(ctx context.Context, chatID, messageID string) (*domain.Message, error) {
	const op = "MessageRepository.GetMessage"
	return r.findOne(ctx, op, bson.M{"_id": messageID, "chat_id": chatID})
}

// GetMessageByClientID возвращает сообщение по клиентскому ID (ключу идемпотентности)
func (r *MessageRepository) GetMessageByClientID(
	ctx context.Context,
	chatID string,
	senderID string,
	clientMessageID string,
) (*domain.Message, error) {
	const op = "MessageRepository.GetMessageByClientID"
	return r.findOne(ctx, op, bson.M{
		"chat_id":       chatID,
		"sender_id":     senderID,
		"client_msg_id": clientMessageID,
	})
}

// GetMessages возвращает неудаленные сообщения чата по списку ID, от новых к старым
func (r *MessageRepository) GetMessages(ctx context.Context, chatID string, messageIDs []string) ([]domain.Message, error) {
	const op = "MessageRepository.GetMessages"

	if len(messageIDs) == 0 {
		return []domain.Message{}, nil
	}

	filter := bson.M{
		"_id":        bson.M{"$in": messageIDs},
		"chat_id":    chatID,
		"deleted_at": notDeleted,
	}
	return r.find(ctx, op, chatID, filter, int64(len(messageIDs)))
}

// ListMessages возвращает сообщения чата от новых к старым.
// Если anchorID не пустой, возвращаются только сообщения старше него.
// Удаленные сообщения остаются в выдаче как "надгробия".
func (r *MessageRepository) ListMessages(
	ctx context.Context,
	chatID string,
	anchorID string,
	limit int64,
) ([]domain.Message, error) {
	const op = "MessageRepository.ListMessages"

	filter := bson.M{"chat_id": chatID}
	if anchorID != "" {
		// ULID сортируется лексикографически, поэтому строковое сравнение корректно
		filter["_id"] = bson.M{"$lt": anchorID}
	}

	return r.find(ctx, op, chatID, filter, limit)
}

// SearchMessages ищет текстовые сообщения чата по полнотекстовому индексу.
func (r *MessageRepository) SearchMessages(
	ctx context.Context,
	chatID string,
	searchText string,
	anchorID string,
	limit int64,
) ([]domain.Message, error) {
	const op = "MessageRepository.SearchMessages"

	filter := bson.M{
		"chat_id":    chatID,
		"type":       domain.TypeText, // нужно, чтобы запрос попал в частичный текстовый индекс
		"$text":      bson.M{"$search": searchText},
		"deleted_at": notDeleted,
		"created_at": bson.M{"$gte": time.Now().UTC().Add(-searchWindow)},
	}
	if anchorID != "" {
		filter["_id"] = bson.M{"$lt": anchorID}
	}

	return r.find(ctx, op, chatID, filter, limit)
}

// CountUnread считает чужие неудаленные сообщения чата новее afterID.
// Считает не больше limit: точное число больших значений клиенту не нужно.
func (r *MessageRepository) CountUnread(
	ctx context.Context,
	chatID string,
	userID string,
	afterID string,
	limit int64,
) (int64, error) {
	const op = "MessageRepository.CountUnread"

	filter := bson.M{
		"chat_id":    chatID,
		"sender_id":  bson.M{"$ne": userID},
		"deleted_at": notDeleted,
	}
	if afterID != "" {
		filter["_id"] = bson.M{"$gt": afterID}
	}

	count, err := r.coll.CountDocuments(ctx, filter, options.Count().SetLimit(limit))
	if err != nil {
		return 0, r.mapError(ctx, op, err, "chatID", chatID)
	}
	return count, nil
}

// UpdateMessage меняет текст сообщения и возвращает обновленное сообщение.
// Изменить сообщение может только его автор. Удаленное сообщение изменить нельзя.
func (r *MessageRepository) UpdateMessage(
	ctx context.Context,
	userID string,
	chatID string,
	messageID string,
	content string,
) (*domain.Message, error) {
	const op = "MessageRepository.UpdateMessage"

	filter := bson.M{
		"_id":        messageID,
		"chat_id":    chatID,
		"sender_id":  userID,
		"deleted_at": notDeleted,
	}
	update := bson.M{
		"$set": bson.M{
			"content":    content,
			"updated_at": time.Now().UTC(),
			"is_edited":  true,
		},
	}

	return r.findOneAndUpdate(ctx, op, filter, update)
}

// SoftDeleteMessages помечает сообщения удаленными и стирает их содержимое.
// Если senderID не пустой, удаляются только сообщения этого автора.
// Возвращает ID реально удаленных сообщений.
func (r *MessageRepository) SoftDeleteMessages(
	ctx context.Context,
	chatID string,
	messageIDs []string,
	senderID string,
) ([]string, error) {
	const op = "MessageRepository.SoftDeleteMessages"

	filter := bson.M{
		"_id":        bson.M{"$in": messageIDs},
		"chat_id":    chatID,
		"deleted_at": notDeleted,
	}
	if senderID != "" {
		filter["sender_id"] = senderID
	}

	// Сначала выясняем, какие именно сообщения будут удалены:
	// их ID нужны для события и обновления превью чата
	cur, err := r.coll.Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, r.mapError(ctx, op, err, "chatID", chatID)
	}

	var found []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &found); err != nil {
		return nil, r.mapError(ctx, op, err, "chatID", chatID)
	}
	if len(found) == 0 {
		return nil, nil
	}

	deletedIDs := make([]string, len(found))
	for i, doc := range found {
		deletedIDs[i] = doc.ID
	}

	now := time.Now().UTC()
	update := bson.M{
		"$set": bson.M{
			"deleted_at": now,
			"updated_at": now,
			"content":    "",
		},
		"$unset": bson.M{
			"attachments": "",
			"reactions":   "",
			"metadata":    "",
		},
	}

	// deleted_at в фильтре защищает от повторного удаления при гонке
	_, err = r.coll.UpdateMany(ctx, bson.M{
		"_id":        bson.M{"$in": deletedIDs},
		"deleted_at": notDeleted,
	}, update)
	if err != nil {
		return nil, r.mapError(ctx, op, err, "chatID", chatID)
	}

	return deletedIDs, nil
}

// AddReaction добавляет реакцию и возвращает обновленное сообщение.
// Повторное добавление той же реакции ничего не меняет.
func (r *MessageRepository) AddReaction(
	ctx context.Context,
	chatID string,
	messageID string,
	reaction domain.Reaction,
) (*domain.Message, error) {
	const op = "MessageRepository.AddReaction"

	return r.findOneAndUpdate(ctx, op,
		bson.M{"_id": messageID, "chat_id": chatID, "deleted_at": notDeleted},
		bson.M{"$addToSet": bson.M{"reactions": reaction}},
	)
}

// RemoveReaction убирает реакцию и возвращает обновленное сообщение
func (r *MessageRepository) RemoveReaction(
	ctx context.Context,
	chatID string,
	messageID string,
	reaction domain.Reaction,
) (*domain.Message, error) {
	const op = "MessageRepository.RemoveReaction"

	return r.findOneAndUpdate(ctx, op,
		bson.M{"_id": messageID, "chat_id": chatID, "deleted_at": notDeleted},
		bson.M{"$pull": bson.M{"reactions": reaction}},
	)
}

func (r *MessageRepository) findOne(ctx context.Context, op string, filter bson.M) (*domain.Message, error) {
	var msg domain.Message

	if err := r.coll.FindOne(ctx, filter).Decode(&msg); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("%s: %w", op, domainComm.ErrNotFound)
		}
		return nil, r.mapError(ctx, op, err)
	}
	return &msg, nil
}

func (r *MessageRepository) findOneAndUpdate(
	ctx context.Context,
	op string,
	filter bson.M,
	update bson.M,
) (*domain.Message, error) {
	var msg domain.Message

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&msg); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("%s: %w", op, domainComm.ErrNotFound)
		}
		return nil, r.mapError(ctx, op, err)
	}
	return &msg, nil
}

func (r *MessageRepository) find(
	ctx context.Context,
	op string,
	chatID string,
	filter bson.M,
	limit int64,
) ([]domain.Message, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "_id", Value: -1}}). // новые сообщения первыми
		SetLimit(limit)

	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, r.mapError(ctx, op, err, "chatID", chatID)
	}

	messages := make([]domain.Message, 0, limit)
	if err := cur.All(ctx, &messages); err != nil {
		return nil, r.mapError(ctx, op, err, "chatID", chatID)
	}

	return messages, nil
}
