package message_usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	domainComm "main/internal/domain"
	domainChat "main/internal/domain/chat_entity"
	domainEvent "main/internal/domain/event_entity"
	domain "main/internal/domain/message_entity"
	"main/internal/ratelimit"

	"github.com/oklog/ulid/v2"
)

// Длина превью последнего сообщения в списке чатов, в символах
const previewLength = 200

type messageRepository interface {
	SaveMessage(ctx context.Context, msg *domain.Message) error
	GetMessage(ctx context.Context, chatID, messageID string) (*domain.Message, error)
	GetMessageByClientID(ctx context.Context, chatID, senderID, clientMessageID string) (*domain.Message, error)
	ListMessages(ctx context.Context, chatID, anchorID string, limit int64) ([]domain.Message, error)
	SearchMessages(ctx context.Context, chatID, searchText, anchorID string, limit int64) ([]domain.Message, error)
	UpdateMessage(ctx context.Context, userID, chatID, messageID, content string) (*domain.Message, error)
	SoftDeleteMessages(ctx context.Context, chatID string, messageIDs []string, senderID string) ([]string, error)
	AddReaction(ctx context.Context, chatID, messageID string, reaction domain.Reaction) (*domain.Message, error)
	RemoveReaction(ctx context.Context, chatID, messageID string, reaction domain.Reaction) (*domain.Message, error)
}

// chatRepository - только то, что нужно для превью последнего сообщения
type chatRepository interface {
	SetLastMessage(ctx context.Context, chatID string, last domainChat.LastMessage) error
	PatchLastMessage(ctx context.Context, chatID string, messageIDs []string, content string, deleted bool) error
}

type membershipChecker interface {
	MemberRole(ctx context.Context, userID, chatID string) (string, error)
}

type eventPublisher interface {
	Publish(ctx context.Context, event domainEvent.Event) error
}

type attachmentValidator interface {
	Validate(ctx context.Context, chatID string, attachments []domain.Attachment) ([]domain.Attachment, error)
}

type rateLimiter interface {
	Allow(ctx context.Context, key string, rule ratelimit.Rule) (ratelimit.Result, error)
}

type MessageUsecase struct {
	repo        messageRepository
	chats       chatRepository
	members     membershipChecker
	publisher   eventPublisher
	attachments attachmentValidator
	limiter     rateLimiter
	sendRule    ratelimit.Rule
	logger      *slog.Logger
}

func NewUsecase(
	repo messageRepository,
	chats chatRepository,
	members membershipChecker,
	publisher eventPublisher,
	attachments attachmentValidator,
	limiter rateLimiter,
	sendRule ratelimit.Rule,
	logger *slog.Logger) *MessageUsecase {
	return &MessageUsecase{
		repo:        repo,
		chats:       chats,
		members:     members,
		publisher:   publisher,
		attachments: attachments,
		limiter:     limiter,
		sendRule:    sendRule,
		logger:      logger,
	}
}

type SendParams struct {
	ChatID  string
	Type    string // по умолчанию "text"
	Content string
	// Необязательный ключ идемпотентности
	ClientMessageID string
	// ID сообщения, на которое это сообщение отвечает
	ReplyToID   string
	Attachments []domain.Attachment
	Metadata    map[string]any
}

// SendMessage сохраняет сообщение и публикует событие для доставки участникам чата.
// Единая точка входа и для REST, и для WebSocket.
//
// Второе возвращаемое значение false означает, что сообщение с таким клиентским ID
// уже было отправлено раньше: возвращается оно же, новое не создается.
func (u *MessageUsecase) SendMessage(
	ctx context.Context,
	senderID string,
	p SendParams,
) (*domain.Message, bool, error) {
	const op = "MessageUsecase.SendMessage"

	if err := u.checkSendRate(ctx, senderID); err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	if _, err := u.members.MemberRole(ctx, senderID, p.ChatID); err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	if p.Type == "" {
		p.Type = domain.TypeText
	}
	if len(p.ClientMessageID) > domain.MaxClientMessageIDLength {
		return nil, false, domainComm.Invalid("client_msg_id is too long")
	}

	// Сообщение с вложениями может быть без текста
	content, err := normalizeContent(p.Content, len(p.Attachments) > 0)
	if err != nil {
		return nil, false, err
	}

	if p.ReplyToID != "" {
		target, err := u.repo.GetMessage(ctx, p.ChatID, p.ReplyToID)
		if errors.Is(err, domainComm.ErrNotFound) || (err == nil && target.IsDeleted()) {
			return nil, false, domainComm.Invalid("message to reply to not found in this chat")
		}
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", op, err)
		}
	}

	attachments, err := u.attachments.Validate(ctx, p.ChatID, p.Attachments)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	now := time.Now().UTC()
	msg := &domain.Message{
		ID:              ulid.Make().String(),
		ChatID:          p.ChatID,
		SenderID:        senderID,
		Type:            p.Type,
		Content:         content,
		ClientMessageID: p.ClientMessageID,
		ReplyToID:       p.ReplyToID,
		Attachments:     attachments,
		Metadata:        p.Metadata,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	err = u.repo.SaveMessage(ctx, msg)
	if errors.Is(err, domainComm.ErrDuplicate) && p.ClientMessageID != "" {
		// Клиент повторил отправку (например, не дождался ответа из-за обрыва связи).
		// Возвращаем уже сохраненное сообщение и не рассылаем событие второй раз.
		existing, err := u.repo.GetMessageByClientID(ctx, p.ChatID, senderID, p.ClientMessageID)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", op, err)
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	// Сообщение уже сохранено, поэтому сбои ниже не считаем ошибкой запроса:
	// получатели увидят сообщение при следующей загрузке истории
	err = u.chats.SetLastMessage(ctx, msg.ChatID, domainChat.LastMessage{
		ID:        msg.ID,
		SenderID:  msg.SenderID,
		Type:      msg.Type,
		Content:   preview(msg.Content),
		CreatedAt: msg.CreatedAt,
	})
	if err != nil {
		u.logger.ErrorContext(ctx, "failed to update chat preview",
			"op", op, "chatID", msg.ChatID, "error", err)
	}

	u.publish(ctx, op, domainEvent.Event{
		Type:    domainEvent.TypeMessageCreated,
		ChatID:  msg.ChatID,
		Payload: msg,
	})

	return msg, true, nil
}

func (u *MessageUsecase) ListMessages(
	ctx context.Context,
	userID string,
	chatID string,
	anchorID string,
	limit int64,
) ([]domain.Message, bool, error) {
	const op = "MessageUsecase.ListMessages"

	if _, err := u.members.MemberRole(ctx, userID, chatID); err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	// Запрашиваем на 1 сообщение больше чтобы узнать есть ли еще страницы
	messages, err := u.repo.ListMessages(ctx, chatID, anchorID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	messages, hasMore := trimPage(messages, limit)
	return messages, hasMore, nil
}

func (u *MessageUsecase) SearchMessages(
	ctx context.Context,
	userID string,
	chatID string,
	searchText string,
	anchorID string,
	limit int64,
) ([]domain.Message, bool, error) {
	const op = "MessageUsecase.SearchMessages"

	if _, err := u.members.MemberRole(ctx, userID, chatID); err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	messages, err := u.repo.SearchMessages(ctx, chatID, searchText, anchorID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	messages, hasMore := trimPage(messages, limit)
	return messages, hasMore, nil
}

// UpdateMessage меняет текст сообщения. Изменить сообщение может только его автор.
func (u *MessageUsecase) UpdateMessage(
	ctx context.Context,
	userID string,
	chatID string,
	messageID string,
	content string,
) (*domain.Message, error) {
	const op = "MessageUsecase.UpdateMessage"

	if _, err := u.members.MemberRole(ctx, userID, chatID); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	content, err := normalizeContent(content, false)
	if err != nil {
		return nil, err
	}

	msg, err := u.repo.UpdateMessage(ctx, userID, chatID, messageID, content)
	if err != nil {
		if errors.Is(err, domainComm.ErrNotFound) {
			return nil, domainComm.NotFound("message not found or it is not yours")
		}
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	err = u.chats.PatchLastMessage(ctx, chatID, []string{messageID}, preview(msg.Content), false)
	if err != nil {
		u.logger.ErrorContext(ctx, "failed to update chat preview",
			"op", op, "chatID", chatID, "error", err)
	}

	u.publish(ctx, op, domainEvent.Event{
		Type:    domainEvent.TypeMessageUpdated,
		ChatID:  chatID,
		Payload: msg,
	})

	return msg, nil
}

// DeleteMessages удаляет сообщения (мягко: в истории остаются "надгробия").
// Участник может удалять только свои сообщения, владелец и админ - любые.
// Возвращает ID реально удаленных сообщений.
func (u *MessageUsecase) DeleteMessages(
	ctx context.Context,
	userID string,
	chatID string,
	messageIDs []string,
) ([]string, error) {
	const op = "MessageUsecase.DeleteMessages"

	role, err := u.members.MemberRole(ctx, userID, chatID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	onlySender := userID
	if domainChat.CanManage(role) {
		onlySender = "" // модератор удаляет сообщения любого автора
	}

	deletedIDs, err := u.repo.SoftDeleteMessages(ctx, chatID, messageIDs, onlySender)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	if len(deletedIDs) == 0 {
		return nil, domainComm.NotFound("messages not found or you cannot delete them")
	}

	if err := u.chats.PatchLastMessage(ctx, chatID, deletedIDs, "", true); err != nil {
		u.logger.ErrorContext(ctx, "failed to update chat preview",
			"op", op, "chatID", chatID, "error", err)
	}

	u.publish(ctx, op, domainEvent.Event{
		Type:   domainEvent.TypeMessageDeleted,
		ChatID: chatID,
		Payload: map[string]any{
			"message_ids": deletedIDs,
			"deleted_by":  userID,
		},
	})

	return deletedIDs, nil
}

// SetReaction ставит (add == true) или убирает реакцию пользователя на сообщение
func (u *MessageUsecase) SetReaction(
	ctx context.Context,
	userID string,
	chatID string,
	messageID string,
	emoji string,
	add bool,
) (*domain.Message, error) {
	const op = "MessageUsecase.SetReaction"

	if _, err := u.members.MemberRole(ctx, userID, chatID); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	emoji = strings.TrimSpace(emoji)
	if emoji == "" || len(emoji) > domain.MaxEmojiLength {
		return nil, domainComm.Invalid("emoji is empty or too long")
	}

	reaction := domain.Reaction{Emoji: emoji, UserID: userID}

	var (
		msg       *domain.Message
		err       error
		eventType string
	)
	if add {
		msg, err = u.repo.AddReaction(ctx, chatID, messageID, reaction)
		eventType = domainEvent.TypeMessageReactionAdded
	} else {
		msg, err = u.repo.RemoveReaction(ctx, chatID, messageID, reaction)
		eventType = domainEvent.TypeMessageReactionRemoved
	}
	if err != nil {
		if errors.Is(err, domainComm.ErrNotFound) {
			return nil, domainComm.NotFound("message not found in this chat")
		}
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	u.publish(ctx, op, domainEvent.Event{
		Type:   eventType,
		ChatID: chatID,
		Payload: map[string]any{
			"message_id": messageID,
			"emoji":      emoji,
			"user_id":    userID,
		},
	})

	return msg, nil
}

func (u *MessageUsecase) checkSendRate(ctx context.Context, userID string) error {
	result, err := u.limiter.Allow(ctx, "messages:"+userID, u.sendRule)
	if err != nil {
		// Redis недоступен: пропускаем запрос. Лимит - защита от злоупотреблений,
		// и ее временное отсутствие лучше, чем отказ всем пользователям.
		u.logger.ErrorContext(ctx, "rate limiter failed, request allowed", "error", err)
		return nil
	}
	if !result.Allowed {
		return domainComm.ErrRateLimited
	}
	return nil
}

func (u *MessageUsecase) publish(ctx context.Context, op string, event domainEvent.Event) {
	if err := u.publisher.Publish(ctx, event); err != nil {
		u.logger.ErrorContext(ctx, "failed to publish event",
			"op", op, "chatID", event.ChatID, "type", event.Type, "error", err)
	}
}

// normalizeContent убирает пробелы по краям и проверяет длину
func normalizeContent(content string, allowEmpty bool) (string, error) {
	content = strings.TrimSpace(content)

	if content == "" && !allowEmpty {
		return "", domainComm.Invalid("message content is empty")
	}
	if utf8.RuneCountInString(content) > domain.MaxContentLength {
		return "", domainComm.Invalid(
			fmt.Sprintf("message content is longer than %d characters", domain.MaxContentLength))
	}
	return content, nil
}

func preview(content string) string {
	if runes := []rune(content); len(runes) > previewLength {
		return string(runes[:previewLength])
	}
	return content
}

// trimPage отрезает лишний элемент, запрошенный для определения следующей страницы
func trimPage(messages []domain.Message, limit int64) ([]domain.Message, bool) {
	if int64(len(messages)) > limit {
		return messages[:limit], true
	}
	return messages, false
}
