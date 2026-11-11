package presence_usecase

import (
	"context"
	"fmt"
	"log/slog"

	domainEvent "main/internal/domain/event_entity"
)

type store interface {
	Connect(ctx context.Context, userID, connID string) (bool, error)
	Heartbeat(ctx context.Context, userID, connID string) error
	Disconnect(ctx context.Context, userID, connID string) (bool, error)
	Online(ctx context.Context, userIDs []string) (map[string]bool, error)
	TryTyping(ctx context.Context, chatID, userID string) (bool, error)
}

type contactsProvider interface {
	ContactIDs(ctx context.Context, userID string) ([]string, error)
}

type membershipChecker interface {
	MemberRole(ctx context.Context, userID, chatID string) (string, error)
}

type ephemeralPublisher interface {
	PublishEphemeral(ctx context.Context, event domainEvent.Event) error
}

type PresenceUsecase struct {
	store     store
	contacts  contactsProvider
	members   membershipChecker
	publisher ephemeralPublisher
	logger    *slog.Logger
}

func NewUsecase(
	store store,
	contacts contactsProvider,
	members membershipChecker,
	publisher ephemeralPublisher,
	logger *slog.Logger) *PresenceUsecase {
	return &PresenceUsecase{
		store:     store,
		contacts:  contacts,
		members:   members,
		publisher: publisher,
		logger:    logger,
	}
}

// Connected отмечает новое соединение. Если пользователь только что стал онлайн,
// об этом узнают все, с кем у него есть общие чаты.
//
// Presence - вспомогательная функция: ее сбой не должен мешать обмену сообщениями,
// поэтому ошибки только логируются.
func (u *PresenceUsecase) Connected(ctx context.Context, userID, connID string) {
	becameOnline, err := u.store.Connect(ctx, userID, connID)
	if err != nil {
		u.logger.ErrorContext(ctx, "failed to register presence", "userID", userID, "error", err)
		return
	}
	if becameOnline {
		u.notifyContacts(ctx, userID, true)
	}
}

// Heartbeat продлевает онлайн-статус, пока соединение открыто
func (u *PresenceUsecase) Heartbeat(ctx context.Context, userID, connID string) {
	if err := u.store.Heartbeat(ctx, userID, connID); err != nil {
		u.logger.WarnContext(ctx, "failed to refresh presence", "userID", userID, "error", err)
	}
}

// Disconnected убирает соединение. Если оно было последним, контакты узнают,
// что пользователь оффлайн.
func (u *PresenceUsecase) Disconnected(ctx context.Context, userID, connID string) {
	becameOffline, err := u.store.Disconnect(ctx, userID, connID)
	if err != nil {
		u.logger.ErrorContext(ctx, "failed to remove presence", "userID", userID, "error", err)
		return
	}
	if becameOffline {
		u.notifyContacts(ctx, userID, false)
	}
}

// Online возвращает онлайн-статус для каждого из пользователей
func (u *PresenceUsecase) Online(ctx context.Context, userIDs []string) (map[string]bool, error) {
	online, err := u.store.Online(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("PresenceUsecase.Online: %w", err)
	}
	return online, nil
}

// Typing сообщает остальным участникам чата, что пользователь печатает.
// Событие рассылается не чаще раза в typing_ttl, сколько бы сигналов ни прислал клиент.
func (u *PresenceUsecase) Typing(ctx context.Context, userID, chatID string) error {
	const op = "PresenceUsecase.Typing"

	if _, err := u.members.MemberRole(ctx, userID, chatID); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	first, err := u.store.TryTyping(ctx, chatID, userID)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if !first {
		return nil
	}

	err = u.publisher.PublishEphemeral(ctx, domainEvent.Event{
		Type:    domainEvent.TypeTyping,
		ChatID:  chatID,
		Payload: map[string]any{"user_id": userID},
		Except:  userID,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

func (u *PresenceUsecase) notifyContacts(ctx context.Context, userID string, online bool) {
	contactIDs, err := u.contacts.ContactIDs(ctx, userID)
	if err != nil {
		u.logger.ErrorContext(ctx, "failed to load contacts for presence",
			"userID", userID, "error", err)
		return
	}
	if len(contactIDs) == 0 {
		return
	}

	err = u.publisher.PublishEphemeral(ctx, domainEvent.Event{
		Type:    domainEvent.TypePresence,
		Payload: map[string]any{"user_id": userID, "online": online},
		Targets: contactIDs,
	})
	if err != nil {
		u.logger.ErrorContext(ctx, "failed to publish presence",
			"userID", userID, "error", err)
	}
}
