package event_entity

// Долговечные события: пишутся в Redis Stream, получают ID
// и могут быть догружены клиентом после переподключения.
const (
	TypeMessageCreated         = "message.created"
	TypeMessageUpdated         = "message.updated"
	TypeMessageDeleted         = "message.deleted"
	TypeMessageReactionAdded   = "message.reaction_added"
	TypeMessageReactionRemoved = "message.reaction_removed"
	TypeMessagePinned          = "message.pinned"
	TypeMessageUnpinned        = "message.unpinned"

	TypeChatCreated           = "chat.created"
	TypeChatUpdated           = "chat.updated"
	TypeChatMemberAdded       = "chat.member_added"
	TypeChatMemberRemoved     = "chat.member_removed"
	TypeChatMemberRoleChanged = "chat.member_role_changed"
	TypeChatRead              = "chat.read"
)

// Эфемерные события: идут через Redis Pub/Sub, не хранятся и не догружаются.
// Если получатель оффлайн, событие ему не нужно.
const (
	TypeTyping   = "typing"
	TypePresence = "presence"
)

// Event - событие для доставки клиентам по WebSocket.
type Event struct {
	Type    string
	ChatID  string
	Payload any

	// Получатели. По умолчанию - все участники чата ChatID.

	// Targets задает получателей явно, без привязки к чату
	Targets []string
	// ExtraTargets - получатели помимо участников чата.
	// Например, удаленный из чата пользователь: он уже не участник, но должен узнать об этом.
	ExtraTargets []string
	// Except - пользователь, которому событие не доставляется (обычно сам инициатор)
	Except string
}

// ChangesMembership сообщает, меняет ли событие состав или роли участников чата.
// По таким событиям каждый экземпляр сервиса сбрасывает свой кэш участников.
func ChangesMembership(eventType string) bool {
	switch eventType {
	case TypeChatMemberAdded, TypeChatMemberRemoved, TypeChatMemberRoleChanged:
		return true
	}
	return false
}
