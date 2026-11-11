package chat_repository

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	mongodb "main/internal/database/mongo"
	domainComm "main/internal/domain"
	domain "main/internal/domain/chat_entity"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const chatsCollection = "chats"

type ChatRepository struct {
	coll   *mongo.Collection
	logger *slog.Logger
}

func NewChatRepository(db *mongo.Database, logger *slog.Logger) *ChatRepository {
	return &ChatRepository{
		coll:   db.Collection(chatsCollection),
		logger: logger,
	}
}

// EnsureIndexes создает индексы коллекции чатов. Операция идемпотентна.
func (r *ChatRepository) EnsureIndexes(ctx context.Context) error {
	const op = "ChatRepository.EnsureIndexes"

	indexes := []mongo.IndexModel{
		{
			// Список чатов пользователя, отсортированный по последней активности
			Keys: bson.D{
				{Key: "members.user_id", Value: 1},
				{Key: "updated_at", Value: -1},
			},
		},
		{
			// Один личный чат на пару пользователей
			Keys: bson.D{{Key: "direct_key", Value: 1}},
			Options: options.Index().
				SetUnique(true).
				SetPartialFilterExpression(bson.D{
					{Key: "direct_key", Value: bson.D{{Key: "$type", Value: "string"}}},
				}),
		},
	}

	if _, err := r.coll.Indexes().CreateMany(ctx, indexes); err != nil {
		return r.mapError(ctx, op, err)
	}
	return nil
}

// CreateChat сохраняет новый чат. Если личный чат между этими пользователями
// уже существует, возвращает ErrDuplicate.
func (r *ChatRepository) CreateChat(ctx context.Context, chat *domain.Chat) error {
	const op = "ChatRepository.CreateChat"

	if _, err := r.coll.InsertOne(ctx, chat); err != nil {
		return r.mapError(ctx, op, err)
	}
	return nil
}

// GetChat возвращает чат по ID или ErrChatNotFound
func (r *ChatRepository) GetChat(ctx context.Context, chatID string) (*domain.Chat, error) {
	const op = "ChatRepository.GetChat"
	return r.findOne(ctx, op, bson.M{"_id": chatID})
}

// GetDirectChat возвращает личный чат по ключу пары участников или ErrChatNotFound
func (r *ChatRepository) GetDirectChat(ctx context.Context, directKey string) (*domain.Chat, error) {
	const op = "ChatRepository.GetDirectChat"
	return r.findOne(ctx, op, bson.M{"direct_key": directKey})
}

// ListChats возвращает чаты пользователя, сначала с самой свежей активностью.
// Если before не нулевое, возвращаются только чаты с более ранней активностью.
func (r *ChatRepository) ListChats(
	ctx context.Context,
	userID string,
	before time.Time,
	limit int64,
) ([]domain.Chat, error) {
	const op = "ChatRepository.ListChats"

	filter := bson.M{"members.user_id": userID}
	if !before.IsZero() {
		filter["updated_at"] = bson.M{"$lt": before}
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}}).
		SetLimit(limit)

	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, r.mapError(ctx, op, err, "userID", userID)
	}

	chats := make([]domain.Chat, 0, limit)
	if err := cur.All(ctx, &chats); err != nil {
		return nil, r.mapError(ctx, op, err, "userID", userID)
	}
	return chats, nil
}

// ChatMembers возвращает участников чата или ErrChatNotFound
func (r *ChatRepository) ChatMembers(ctx context.Context, chatID string) ([]domain.Member, error) {
	const op = "ChatRepository.ChatMembers"

	var chat domain.Chat

	opts := options.FindOne().SetProjection(bson.M{"members": 1})
	if err := r.coll.FindOne(ctx, bson.M{"_id": chatID}, opts).Decode(&chat); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("%s: %w", op, domain.ErrChatNotFound)
		}
		return nil, r.mapError(ctx, op, err, "chatID", chatID)
	}
	return chat.Members, nil
}

// ContactIDs возвращает ID всех пользователей, с которыми у userID есть общие чаты
func (r *ChatRepository) ContactIDs(ctx context.Context, userID string) ([]string, error) {
	const op = "ChatRepository.ContactIDs"

	values, err := r.coll.Distinct(ctx, "members.user_id", bson.M{"members.user_id": userID})
	if err != nil {
		return nil, r.mapError(ctx, op, err, "userID", userID)
	}

	contactIDs := make([]string, 0, len(values))
	for _, value := range values {
		if id, ok := value.(string); ok && id != userID {
			contactIDs = append(contactIDs, id)
		}
	}
	return contactIDs, nil
}

// RenameChat меняет название чата
func (r *ChatRepository) RenameChat(ctx context.Context, chatID, name string) error {
	const op = "ChatRepository.RenameChat"

	return r.updateOne(ctx, op,
		bson.M{"_id": chatID},
		bson.M{"$set": bson.M{"name": name, "updated_at": time.Now().UTC()}},
	)
}

// AddMember добавляет участника. Если он уже в чате, возвращает ErrConflict.
func (r *ChatRepository) AddMember(ctx context.Context, chatID string, member domain.Member) error {
	const op = "ChatRepository.AddMember"

	err := r.updateOne(ctx, op,
		// $ne в фильтре делает добавление идемпотентным при гонке двух запросов
		bson.M{"_id": chatID, "members.user_id": bson.M{"$ne": member.UserID}},
		bson.M{
			"$push": bson.M{"members": member},
			"$set":  bson.M{"updated_at": time.Now().UTC()},
		},
	)
	if errors.Is(err, domainComm.ErrNotFound) {
		return fmt.Errorf("%s: %w", op, domainComm.ErrConflict)
	}
	return err
}

// RemoveMember убирает участника из чата
func (r *ChatRepository) RemoveMember(ctx context.Context, chatID, userID string) error {
	const op = "ChatRepository.RemoveMember"

	return r.updateOne(ctx, op,
		bson.M{"_id": chatID, "members.user_id": userID},
		bson.M{"$pull": bson.M{"members": bson.M{"user_id": userID}}},
	)
}

// SetRoles меняет роли нескольких участников одной атомарной операцией.
// Атомарность нужна при передаче владения: в чате не должно оказаться двух владельцев.
func (r *ChatRepository) SetRoles(ctx context.Context, chatID string, roles map[string]string) error {
	const op = "ChatRepository.SetRoles"

	set := bson.M{}
	filters := make([]interface{}, 0, len(roles))

	i := 0
	for userID, role := range roles {
		id := fmt.Sprintf("m%d", i)
		set[fmt.Sprintf("members.$[%s].role", id)] = role
		filters = append(filters, bson.M{id + ".user_id": userID})
		i++
	}

	opts := options.Update().SetArrayFilters(options.ArrayFilters{Filters: filters})
	res, err := r.coll.UpdateOne(ctx, bson.M{"_id": chatID}, bson.M{"$set": set}, opts)
	if err != nil {
		return r.mapError(ctx, op, err, "chatID", chatID)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%s: %w", op, domain.ErrChatNotFound)
	}
	return nil
}

// SetLastMessage обновляет превью последнего сообщения и время активности чата.
// Более старое сообщение не перезапишет более новое, даже если запросы придут не по порядку.
func (r *ChatRepository) SetLastMessage(ctx context.Context, chatID string, last domain.LastMessage) error {
	const op = "ChatRepository.SetLastMessage"

	filter := bson.M{
		"_id": chatID,
		"$or": bson.A{
			bson.M{"last_message": bson.M{"$exists": false}},
			bson.M{"last_message.id": bson.M{"$lt": last.ID}},
		},
	}
	update := bson.M{"$set": bson.M{"last_message": last, "updated_at": last.CreatedAt}}

	if _, err := r.coll.UpdateOne(ctx, filter, update); err != nil {
		return r.mapError(ctx, op, err, "chatID", chatID)
	}
	return nil
}

// PatchLastMessage обновляет превью, если последнее сообщение чата - одно из messageIDs.
// Нужен, чтобы превью не показывало старый текст после правки или удаления сообщения.
func (r *ChatRepository) PatchLastMessage(
	ctx context.Context,
	chatID string,
	messageIDs []string,
	content string,
	deleted bool,
) error {
	const op = "ChatRepository.PatchLastMessage"

	filter := bson.M{"_id": chatID, "last_message.id": bson.M{"$in": messageIDs}}
	update := bson.M{"$set": bson.M{
		"last_message.content":    content,
		"last_message.is_deleted": deleted,
	}}

	if _, err := r.coll.UpdateOne(ctx, filter, update); err != nil {
		return r.mapError(ctx, op, err, "chatID", chatID)
	}
	return nil
}

// MarkRead сдвигает отметку прочтения участника вперед.
// Возвращает false, если отметка не изменилась (уже стояла на этом или более новом сообщении).
func (r *ChatRepository) MarkRead(ctx context.Context, chatID, userID, messageID string) (bool, error) {
	const op = "ChatRepository.MarkRead"

	// $max не даст отметке уйти назад: ULID сравниваются как строки
	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": chatID, "members.user_id": userID},
		bson.M{"$max": bson.M{"members.$.last_read_message_id": messageID}},
	)
	if err != nil {
		return false, r.mapError(ctx, op, err, "chatID", chatID)
	}
	return res.ModifiedCount > 0, nil
}

// AddPin закрепляет сообщение. Возвращает false, если оно уже было закреплено.
func (r *ChatRepository) AddPin(ctx context.Context, chatID, messageID string) (bool, error) {
	const op = "ChatRepository.AddPin"

	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": chatID},
		bson.M{"$addToSet": bson.M{"pinned_message_ids": messageID}},
	)
	if err != nil {
		return false, r.mapError(ctx, op, err, "chatID", chatID)
	}
	return res.ModifiedCount > 0, nil
}

// RemovePin открепляет сообщение. Возвращает false, если оно не было закреплено.
func (r *ChatRepository) RemovePin(ctx context.Context, chatID, messageID string) (bool, error) {
	const op = "ChatRepository.RemovePin"

	res, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": chatID},
		bson.M{"$pull": bson.M{"pinned_message_ids": messageID}},
	)
	if err != nil {
		return false, r.mapError(ctx, op, err, "chatID", chatID)
	}
	return res.ModifiedCount > 0, nil
}

func (r *ChatRepository) findOne(ctx context.Context, op string, filter bson.M) (*domain.Chat, error) {
	var chat domain.Chat

	if err := r.coll.FindOne(ctx, filter).Decode(&chat); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("%s: %w", op, domain.ErrChatNotFound)
		}
		return nil, r.mapError(ctx, op, err)
	}
	return &chat, nil
}

// updateOne возвращает ErrNotFound, если под фильтр не попал ни один документ
func (r *ChatRepository) updateOne(ctx context.Context, op string, filter, update bson.M) error {
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return r.mapError(ctx, op, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%s: %w", op, domainComm.ErrNotFound)
	}
	return nil
}

func (r *ChatRepository) mapError(ctx context.Context, op string, err error, attrs ...any) error {
	return mongodb.MapError(ctx, r.logger, op, err, attrs...)
}
