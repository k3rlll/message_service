package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainComm "main/internal/domain"
	domain "main/internal/domain/message_entity"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	daysForSearch = -60
)

func (r *MessageRepository) SaveMessage(ctx context.Context, msg *domain.Message) error {
	const op = "MessageRepository.SaveMessage"
	r.logger.InfoContext(
		ctx, "Saving message to database",
		"op", op,
		"chatID", msg.ChatID,
		"senderID", msg.SenderID)

	_, err := r.coll.InsertOne(ctx, msg)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			r.logger.ErrorContext(
				ctx, "op", op,
				"Database operation timed out", "error", err)
			return domainComm.ErrDatabaseTimeout
		}

		// Например, ошибка лимита документа (16MB)
		if mongo.IsDuplicateKeyError(err) {
			r.logger.WarnContext(
				ctx, "Duplicate message ID detected",
				"op", op,
				"chatID", msg.ChatID,
				"senderID", msg.SenderID,
			)
			//TODO: Handle this case properly, maybe return a specific error to the usecase
			// Если  есть уникальный индекс на ID сообщения
			return nil // Или игнорируем если это повторная попытка
		}

		r.logger.ErrorContext(
			ctx, "Failed to save message to database",
			"op", op,
			"chatID", msg.ChatID,
			"senderID", msg.SenderID,
			"error", err,
		)
		return fmt.Errorf("failed to save message: %w", domainComm.ErrInternal)
	}

	return nil
}

func (r *MessageRepository) ListMessages(
	ctx context.Context,
	chatID string,
	anchorID string,
	limit int64,
) ([]domain.Message, error) {
	const op = "MessageRepository.ListMessages"

	filter := bson.M{"chat_id": chatID}

	if anchorID != "" {
		objID, err := primitive.ObjectIDFromHex(anchorID)
		if err != nil {
			return nil, domainComm.ErrInvalidInput
		}
		filter["_id"] = bson.M{"$lt": objID}

	}

	opt := options.Find().
		SetSort(bson.D{{Key: "_id", Value: -1}}). // Новые сообщения первыми
		SetLimit(limit)

	cur, err := r.coll.Find(ctx, filter, opt)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			r.logger.ErrorContext(ctx, "database operation timed out",
				"op", op, "chatID", chatID, "error", err)
			return nil, fmt.Errorf("%s: %w", op, domainComm.ErrDatabaseTimeout)
		}

		r.logger.ErrorContext(ctx, "failed to execute find query",
			"op", op, "chatID", chatID, "error", err)
		return nil, fmt.Errorf("%s: %w", op, domainComm.ErrInternal)
	}
	defer cur.Close(ctx) // Обязательно освобождаем курсор

	var messages []domain.Message
	if err = cur.All(ctx, &messages); err != nil {
		r.logger.ErrorContext(ctx, "failed to decode cursor results",
			"op", op, "chatID", chatID, "error", err)
		return nil, fmt.Errorf("%s: %w", op, domainComm.ErrInternal)
	}

	r.logger.DebugContext(ctx, "messages successfully retrieved",
		"op", op, "chatID", chatID, "count", len(messages))

	return messages, nil
}

func (r *MessageRepository) SearchMessages(
	ctx context.Context,
	chatID string,
	searchText string,
	anchorID string,
	limit int64,
) ([]domain.Message, error) {

	const op = "MessageRepository.SearchMessages"

	// Формируем базовый фильтр
	filter := bson.M{
		"chat_id": chatID,
		"type":    "text",
		"$text":   bson.M{"$search": searchText},
		// Ограничение поиска последними 60 днями
		// - отличная практика для экономии ресурсов БД!
		"created_at": bson.M{"$gte": time.Now().Add(daysForSearch * 24 * time.Hour)},
	}

	// Добавляем якорь только если он передан
	if anchorID != "" {
		filter["_id"] = bson.M{"$lt": anchorID}
	}

	// Настраиваем опции выборки
	opts := options.Find().
		SetSort(bson.D{{Key: "_id", Value: -1}}). // Сортируем от новых к старым
		SetLimit(limit)

	cursor, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			r.logger.ErrorContext(ctx, "database operation timed out",
				"op", op, "chatID", chatID, "error", err)

			return nil, fmt.Errorf("%s: %w", op, domainComm.ErrDatabaseTimeout)
		}

		r.logger.ErrorContext(ctx, "failed to execute text search query",
			"op", op, "chatID", chatID, "error", err)

		return nil, fmt.Errorf("%s: %w", op, domainComm.ErrInternal)
	}
	defer cursor.Close(ctx)

	var messages []domain.Message
	if err = cursor.All(ctx, &messages); err != nil {
		r.logger.ErrorContext(ctx, "failed to decode search results",
			"op", op, "chatID", chatID, "error", err)
		return nil, fmt.Errorf("%s: %w", op, domainComm.ErrInternal)
	}

	r.logger.DebugContext(ctx, "search query executed successfully",
		"op", op, "chatID", chatID, "found", len(messages))

	return messages, nil
}

func (r *MessageRepository) UpdateMessage(
	ctx context.Context,
	userID string,
	chatID string,
	messageID string,
	content string,
) error {
	const op = "MessageRepository.UpdateMessage"

	// Фильтр защищает от изменения чужих сообщений
	filter := bson.M{
		"_id":       messageID,
		"chat_id":   chatID,
		"sender_id": userID, // Только автор может изменить сообщение
	}

	update := bson.M{
		"$set": bson.M{
			"content":    content,
			"updated_at": time.Now().UTC(),
			"is_edited":  true, // Помечать сообщение как измененное
		},
	}

	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			r.logger.ErrorContext(ctx, "database operation timed out",
				"op", op, "messageID", messageID, "error", err)
			return fmt.Errorf("%s: %w", op, domainComm.ErrDatabaseTimeout)
		}

		r.logger.ErrorContext(ctx, "failed to execute update query",
			"op", op, "messageID", messageID, "error", err)
		return fmt.Errorf("%s: %w", op, domainComm.ErrInternal)
	}

	// Если MatchedCount == 0  значит сообщения нет или юзер не является его автором.
	if res.MatchedCount == 0 {
		r.logger.DebugContext(ctx, "message not found or access denied during update",
			"op", op, "messageID", messageID, "userID", userID)
		return fmt.Errorf("%s: %w", op, domainComm.ErrNotFound)
	}

	r.logger.DebugContext(ctx, "message successfully updated",
		"op", op, "messageID", messageID)

	return nil
}

func (r *MessageRepository) DeleteMessages(
	ctx context.Context,
	chatID string,
	messageIDs []string,
) error {
	const op = "MessageRepository.DeleteMessages"

	filter := bson.M{
		"chat_id": chatID,
		"_id":     bson.M{"$in": messageIDs},
	}

	res, err := r.coll.DeleteMany(ctx, filter)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			r.logger.ErrorContext(ctx, "database operation timed out",
				"op", op, "chatID", chatID, "error", err)
			return fmt.Errorf("%s: %w", op, domainComm.ErrDatabaseTimeout)
		}

		r.logger.ErrorContext(ctx, "failed to execute delete query",
			"op", op, "chatID", chatID, "error", err)
		return fmt.Errorf("%s: %w", op, domainComm.ErrInternal)
	}

	r.logger.DebugContext(ctx, "messages deleted",
		"op", op,
		"chatID", chatID,
		"requested_count", len(messageIDs),
		"deleted_count", res.DeletedCount,
	)

	return nil
}
