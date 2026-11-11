package chat_usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	domainComm "main/internal/domain"
	domain "main/internal/domain/chat_entity"
	domainEvent "main/internal/domain/event_entity"
	domainMsg "main/internal/domain/message_entity"

	"github.com/oklog/ulid/v2"
)

// Счетчик непрочитанных считается не дальше этого значения: клиент покажет "99+"
const unreadCountCap = 100

type chatRepository interface {
	CreateChat(ctx context.Context, chat *domain.Chat) error
	GetChat(ctx context.Context, chatID string) (*domain.Chat, error)
	GetDirectChat(ctx context.Context, directKey string) (*domain.Chat, error)
	ListChats(ctx context.Context, userID string, before time.Time, limit int64) ([]domain.Chat, error)
	RenameChat(ctx context.Context, chatID, name string) error
	AddMember(ctx context.Context, chatID string, member domain.Member) error
	RemoveMember(ctx context.Context, chatID, userID string) error
	SetRoles(ctx context.Context, chatID string, roles map[string]string) error
	MarkRead(ctx context.Context, chatID, userID, messageID string) (bool, error)
	AddPin(ctx context.Context, chatID, messageID string) (bool, error)
	RemovePin(ctx context.Context, chatID, messageID string) (bool, error)
}

type messageRepository interface {
	GetMessage(ctx context.Context, chatID, messageID string) (*domainMsg.Message, error)
	GetMessages(ctx context.Context, chatID string, messageIDs []string) ([]domainMsg.Message, error)
	CountUnread(ctx context.Context, chatID, userID, afterID string, limit int64) (int64, error)
}

type membersCache interface {
	MemberRole(ctx context.Context, userID, chatID string) (string, error)
	Invalidate(chatID string)
}

type eventPublisher interface {
	Publish(ctx context.Context, event domainEvent.Event) error
}

type ChatUsecase struct {
	repo      chatRepository
	messages  messageRepository
	cache     membersCache
	publisher eventPublisher
	logger    *slog.Logger
}

func NewUsecase(
	repo chatRepository,
	messages messageRepository,
	cache membersCache,
	publisher eventPublisher,
	logger *slog.Logger) *ChatUsecase {
	return &ChatUsecase{
		repo:      repo,
		messages:  messages,
		cache:     cache,
		publisher: publisher,
		logger:    logger,
	}
}

// ChatSummary - чат глазами конкретного пользователя
type ChatSummary struct {
	domain.Chat
	// Число непрочитанных чужих сообщений, не больше unreadCountCap
	UnreadCount int64 `json:"unread_count"`
}

type CreateParams struct {
	Type string
	Name string
	// Участники помимо создателя. Для личного чата - ровно один.
	MemberIDs []string
}

// CreateChat создает чат. Для личного чата, который уже существует, возвращает
// существующий, и второе значение равно false.
//
// Существование пользователей не проверяется: ими владеет сервис авторизации.
func (u *ChatUsecase) CreateChat(
	ctx context.Context,
	creatorID string,
	p CreateParams,
) (*domain.Chat, bool, error) {
	const op = "ChatUsecase.CreateChat"

	memberIDs := uniqueWithout(p.MemberIDs, creatorID)
	now := time.Now().UTC()

	chat := &domain.Chat{
		ID:        ulid.Make().String(),
		Type:      p.Type,
		CreatedAt: now,
		UpdatedAt: now,
	}

	switch p.Type {
	case domain.TypeDirect:
		if len(memberIDs) != 1 {
			return nil, false, domainComm.Invalid("a direct chat needs exactly one other member")
		}
		// В личном чате участники равноправны, владельца нет
		chat.DirectKey = domain.DirectKey(creatorID, memberIDs[0])
		chat.Members = []domain.Member{
			{UserID: creatorID, Role: domain.RoleMember, JoinedAt: now},
			{UserID: memberIDs[0], Role: domain.RoleMember, JoinedAt: now},
		}

	case domain.TypeGroup:
		name, err := normalizeName(p.Name)
		if err != nil {
			return nil, false, err
		}
		if len(memberIDs)+1 > domain.MaxMembers {
			return nil, false, domainComm.Invalid(
				fmt.Sprintf("a chat can have at most %d members", domain.MaxMembers))
		}

		chat.Name = name
		chat.Members = append(chat.Members,
			domain.Member{UserID: creatorID, Role: domain.RoleOwner, JoinedAt: now})
		for _, memberID := range memberIDs {
			chat.Members = append(chat.Members,
				domain.Member{UserID: memberID, Role: domain.RoleMember, JoinedAt: now})
		}

	default:
		return nil, false, domainComm.Invalid("chat type must be direct or group")
	}

	err := u.repo.CreateChat(ctx, chat)
	if errors.Is(err, domainComm.ErrDuplicate) && chat.DirectKey != "" {
		// Личный чат с этим пользователем уже есть - создание идемпотентно
		existing, err := u.repo.GetDirectChat(ctx, chat.DirectKey)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", op, err)
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	u.publish(ctx, op, domainEvent.Event{
		Type:    domainEvent.TypeChatCreated,
		ChatID:  chat.ID,
		Payload: chat,
	})

	return chat, true, nil
}

// ListChats возвращает чаты пользователя, сначала с самой свежей активностью
func (u *ChatUsecase) ListChats(
	ctx context.Context,
	userID string,
	before time.Time,
	limit int64,
) ([]ChatSummary, bool, error) {
	const op = "ChatUsecase.ListChats"

	chats, err := u.repo.ListChats(ctx, userID, before, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", op, err)
	}

	hasMore := int64(len(chats)) > limit
	if hasMore {
		chats = chats[:limit]
	}

	// Один запрос подсчета на чат. Страница ограничена, а запрос идет по индексу
	// и останавливается на unreadCountCap, так что это дешевле, чем кажется.
	summaries := make([]ChatSummary, 0, len(chats))
	for i := range chats {
		summary, err := u.summarize(ctx, &chats[i], userID)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", op, err)
		}
		summaries = append(summaries, *summary)
	}

	return summaries, hasMore, nil
}

// GetChat возвращает чат с участниками. Доступно только участникам.
func (u *ChatUsecase) GetChat(ctx context.Context, userID, chatID string) (*ChatSummary, error) {
	const op = "ChatUsecase.GetChat"

	chat, _, err := u.loadAsMember(ctx, userID, chatID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	summary, err := u.summarize(ctx, chat, userID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return summary, nil
}

// RenameChat меняет название группового чата. Доступно владельцу и админам.
func (u *ChatUsecase) RenameChat(ctx context.Context, userID, chatID, name string) (*domain.Chat, error) {
	const op = "ChatUsecase.RenameChat"

	chat, _, err := u.loadAsManager(ctx, userID, chatID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	name, err = normalizeName(name)
	if err != nil {
		return nil, err
	}

	if err := u.repo.RenameChat(ctx, chatID, name); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	chat.Name = name

	u.publish(ctx, op, domainEvent.Event{
		Type:    domainEvent.TypeChatUpdated,
		ChatID:  chatID,
		Payload: map[string]any{"name": name, "updated_by": userID},
	})

	return chat, nil
}

// AddMember добавляет участника в групповой чат. Доступно владельцу и админам.
func (u *ChatUsecase) AddMember(ctx context.Context, actorID, chatID, userID string) (*domain.Chat, error) {
	const op = "ChatUsecase.AddMember"

	chat, _, err := u.loadAsManager(ctx, actorID, chatID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	if _, exists := chat.Member(userID); exists {
		return nil, domainComm.Conflict("user is already a member of this chat")
	}
	if len(chat.Members) >= domain.MaxMembers {
		return nil, domainComm.Conflict(
			fmt.Sprintf("a chat can have at most %d members", domain.MaxMembers))
	}

	member := domain.Member{UserID: userID, Role: domain.RoleMember, JoinedAt: time.Now().UTC()}

	if err := u.repo.AddMember(ctx, chatID, member); err != nil {
		if errors.Is(err, domainComm.ErrConflict) {
			return nil, domainComm.Conflict("user is already a member of this chat")
		}
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	chat.Members = append(chat.Members, member)
	u.cache.Invalidate(chatID)

	// Новый участник о чате еще ничего не знает, поэтому в событии чат целиком
	u.publish(ctx, op, domainEvent.Event{
		Type:   domainEvent.TypeChatMemberAdded,
		ChatID: chatID,
		Payload: map[string]any{
			"user_id":  userID,
			"role":     member.Role,
			"added_by": actorID,
			"chat":     chat,
		},
	})

	return chat, nil
}

// RemoveMember убирает участника из группового чата.
//
// Себя может убрать любой участник, кроме владельца: он сначала передает владение.
// Других убирают владелец (кого угодно) и админы (только обычных участников).
func (u *ChatUsecase) RemoveMember(ctx context.Context, actorID, chatID, userID string) error {
	const op = "ChatUsecase.RemoveMember"

	chat, actor, err := u.loadAsMember(ctx, actorID, chatID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if chat.Type != domain.TypeGroup {
		return domainComm.Invalid("members of a direct chat cannot be changed")
	}

	target, exists := chat.Member(userID)
	if !exists {
		return domainComm.NotFound("user is not a member of this chat")
	}

	switch {
	case actorID == userID:
		if actor.Role == domain.RoleOwner {
			return domainComm.Conflict("the owner must transfer ownership before leaving the chat")
		}
	case !domain.CanManage(actor.Role):
		return domainComm.Forbidden("only the owner and admins can remove members")
	case target.Role == domain.RoleOwner:
		return domainComm.Forbidden("the owner cannot be removed")
	case target.Role == domain.RoleAdmin && actor.Role != domain.RoleOwner:
		return domainComm.Forbidden("only the owner can remove an admin")
	}

	if err := u.repo.RemoveMember(ctx, chatID, userID); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	u.cache.Invalidate(chatID)

	u.publish(ctx, op, domainEvent.Event{
		Type:    domainEvent.TypeChatMemberRemoved,
		ChatID:  chatID,
		Payload: map[string]any{"user_id": userID, "removed_by": actorID},
		// Удаленный уже не участник, но должен узнать, что чат для него закрыт
		ExtraTargets: []string{userID},
	})

	return nil
}

// SetRole меняет роль участника. Доступно только владельцу.
// Назначение роли owner передает владение: прежний владелец становится админом.
func (u *ChatUsecase) SetRole(ctx context.Context, actorID, chatID, userID, role string) error {
	const op = "ChatUsecase.SetRole"

	chat, actor, err := u.loadAsMember(ctx, actorID, chatID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if chat.Type != domain.TypeGroup {
		return domainComm.Invalid("a direct chat has no roles")
	}
	if actor.Role != domain.RoleOwner {
		return domainComm.Forbidden("only the owner can change roles")
	}
	if actorID == userID {
		return domainComm.Invalid("you cannot change your own role")
	}
	if _, exists := chat.Member(userID); !exists {
		return domainComm.NotFound("user is not a member of this chat")
	}

	roles := map[string]string{userID: role}
	switch role {
	case domain.RoleAdmin, domain.RoleMember:
	case domain.RoleOwner:
		roles[actorID] = domain.RoleAdmin
	default:
		return domainComm.Invalid("role must be owner, admin or member")
	}

	if err := u.repo.SetRoles(ctx, chatID, roles); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	u.cache.Invalidate(chatID)

	u.publish(ctx, op, domainEvent.Event{
		Type:    domainEvent.TypeChatMemberRoleChanged,
		ChatID:  chatID,
		Payload: map[string]any{"roles": roles, "changed_by": actorID},
	})

	return nil
}

// MarkRead отмечает чат прочитанным до сообщения messageID включительно
func (u *ChatUsecase) MarkRead(ctx context.Context, userID, chatID, messageID string) error {
	const op = "ChatUsecase.MarkRead"

	if _, err := u.cache.MemberRole(ctx, userID, chatID); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	if _, err := u.messages.GetMessage(ctx, chatID, messageID); err != nil {
		if errors.Is(err, domainComm.ErrNotFound) {
			return domainComm.NotFound("message not found in this chat")
		}
		return fmt.Errorf("%s: %w", op, err)
	}

	changed, err := u.repo.MarkRead(ctx, chatID, userID, messageID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if !changed {
		// Отметка уже стояла на этом или более новом сообщении - рассылать нечего
		return nil
	}

	u.publish(ctx, op, domainEvent.Event{
		Type:    domainEvent.TypeChatRead,
		ChatID:  chatID,
		Payload: map[string]any{"user_id": userID, "message_id": messageID},
	})

	return nil
}

// SetPin закрепляет (pin == true) или открепляет сообщение.
// В групповом чате доступно владельцу и админам, в личном - обоим участникам.
func (u *ChatUsecase) SetPin(ctx context.Context, userID, chatID, messageID string, pin bool) error {
	const op = "ChatUsecase.SetPin"

	chat, member, err := u.loadAsMember(ctx, userID, chatID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if chat.Type == domain.TypeGroup && !domain.CanManage(member.Role) {
		return domainComm.Forbidden("only the owner and admins can pin messages")
	}

	var (
		changed   bool
		eventType string
	)
	if pin {
		msg, err := u.messages.GetMessage(ctx, chatID, messageID)
		if errors.Is(err, domainComm.ErrNotFound) || (err == nil && msg.IsDeleted()) {
			return domainComm.NotFound("message not found in this chat")
		}
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		if len(chat.PinnedMessageIDs) >= domain.MaxPins {
			return domainComm.Conflict(
				fmt.Sprintf("a chat can have at most %d pinned messages", domain.MaxPins))
		}

		changed, err = u.repo.AddPin(ctx, chatID, messageID)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		eventType = domainEvent.TypeMessagePinned
	} else {
		changed, err = u.repo.RemovePin(ctx, chatID, messageID)
		if err != nil {
			return fmt.Errorf("%s: %w", op, err)
		}
		eventType = domainEvent.TypeMessageUnpinned
	}

	if changed {
		u.publish(ctx, op, domainEvent.Event{
			Type:    eventType,
			ChatID:  chatID,
			Payload: map[string]any{"message_id": messageID, "user_id": userID},
		})
	}

	return nil
}

// ListPins возвращает закрепленные сообщения чата. Удаленные сообщения в список не попадают.
func (u *ChatUsecase) ListPins(ctx context.Context, userID, chatID string) ([]domainMsg.Message, error) {
	const op = "ChatUsecase.ListPins"

	chat, _, err := u.loadAsMember(ctx, userID, chatID)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	messages, err := u.messages.GetMessages(ctx, chatID, chat.PinnedMessageIDs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return messages, nil
}

// loadAsMember загружает чат и проверяет, что пользователь в нем состоит.
// Читает из БД, а не из кэша: операциям управления нужно свежее состояние.
func (u *ChatUsecase) loadAsMember(ctx context.Context, userID, chatID string) (*domain.Chat, domain.Member, error) {
	chat, err := u.repo.GetChat(ctx, chatID)
	if err != nil {
		return nil, domain.Member{}, err
	}

	member, ok := chat.Member(userID)
	if !ok {
		return nil, domain.Member{}, domainComm.ErrAccessDenied
	}
	return chat, member, nil
}

// loadAsManager дополнительно требует, чтобы чат был групповым,
// а пользователь - владельцем или админом
func (u *ChatUsecase) loadAsManager(ctx context.Context, userID, chatID string) (*domain.Chat, domain.Member, error) {
	chat, member, err := u.loadAsMember(ctx, userID, chatID)
	if err != nil {
		return nil, domain.Member{}, err
	}
	if chat.Type != domain.TypeGroup {
		return nil, domain.Member{}, domainComm.Invalid("a direct chat cannot be changed")
	}
	if !domain.CanManage(member.Role) {
		return nil, domain.Member{}, domainComm.Forbidden("only the owner and admins can manage the chat")
	}
	return chat, member, nil
}

func (u *ChatUsecase) summarize(ctx context.Context, chat *domain.Chat, userID string) (*ChatSummary, error) {
	member, _ := chat.Member(userID)

	unread, err := u.messages.CountUnread(ctx, chat.ID, userID, member.LastReadMessageID, unreadCountCap)
	if err != nil {
		return nil, err
	}
	return &ChatSummary{Chat: *chat, UnreadCount: unread}, nil
}

func (u *ChatUsecase) publish(ctx context.Context, op string, event domainEvent.Event) {
	if err := u.publisher.Publish(ctx, event); err != nil {
		u.logger.ErrorContext(ctx, "failed to publish event",
			"op", op, "chatID", event.ChatID, "type", event.Type, "error", err)
	}
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > domain.MaxNameLength {
		return "", domainComm.Invalid(
			fmt.Sprintf("chat name must be between 1 and %d characters", domain.MaxNameLength))
	}
	return name, nil
}

// uniqueWithout убирает дубликаты и значение exclude, сохраняя порядок
func uniqueWithout(ids []string, exclude string) []string {
	seen := map[string]struct{}{exclude: {}}
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
