package chat_entity

import (
	"errors"
	"time"
)

var (
	ErrChatNotFound = errors.New("chat not found")
	ErrAccessDenied = errors.New("access denied: user is not a member of this chat")
	ErrUserBanned   = errors.New("user is banned in this chat")
)

type Chat struct {
	ID        string    `bson:"_id,omitempty"`
	Name      string    `bson:"name"`
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
}
