package chat_entity

import (
	"errors"
	"time"
)

var (
	ErrChatNotFound = errors.New("chat not found")
)

const (
	TypeDirect = "direct" // личный чат двух пользователей
	TypeGroup  = "group"

	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"

	MaxMembers    = 200
	MaxPins       = 50
	MaxNameLength = 100
)

type Member struct {
	UserID   string    `bson:"user_id" json:"user_id"`
	Role     string    `bson:"role" json:"role"`
	JoinedAt time.Time `bson:"joined_at" json:"joined_at"`
	// ID последнего прочитанного сообщения. Все сообщения с меньшим ID считаются прочитанными.
	LastReadMessageID string `bson:"last_read_message_id,omitempty" json:"last_read_message_id,omitempty"`
}

// LastMessage - превью последнего сообщения для списка чатов.
// Денормализовано в документ чата, чтобы список чатов строился одним запросом.
type LastMessage struct {
	ID        string    `bson:"id" json:"id"`
	SenderID  string    `bson:"sender_id" json:"sender_id"`
	Type      string    `bson:"type" json:"type"`
	Content   string    `bson:"content" json:"content"`
	IsDeleted bool      `bson:"is_deleted" json:"is_deleted"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

type Chat struct {
	ID   string `bson:"_id" json:"id"`
	Type string `bson:"type" json:"type"`
	Name string `bson:"name" json:"name"`
	// DirectKey - пара ID участников личного чата. Уникальный индекс по нему
	// гарантирует, что между двумя пользователями существует только один личный чат.
	DirectKey        string       `bson:"direct_key,omitempty" json:"-"`
	Members          []Member     `bson:"members" json:"members"`
	LastMessage      *LastMessage `bson:"last_message,omitempty" json:"last_message,omitempty"`
	PinnedMessageIDs []string     `bson:"pinned_message_ids,omitempty" json:"pinned_message_ids,omitempty"`
	CreatedAt        time.Time    `bson:"created_at" json:"created_at"`
	UpdatedAt        time.Time    `bson:"updated_at" json:"updated_at"`
}

// Member возвращает участника чата по ID пользователя
func (c *Chat) Member(userID string) (Member, bool) {
	for _, m := range c.Members {
		if m.UserID == userID {
			return m, true
		}
	}
	return Member{}, false
}

// CanManage сообщает, может ли роль управлять чатом:
// переименовывать, добавлять и удалять участников, закреплять и модерировать сообщения
func CanManage(role string) bool {
	return role == RoleOwner || role == RoleAdmin
}

// DirectKey строит ключ личного чата, не зависящий от порядка участников
func DirectKey(userA, userB string) string {
	if userA > userB {
		userA, userB = userB, userA
	}
	return userA + ":" + userB
}
