package domain

import (
	"time"

	"github.com/oklog/ulid/v2"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Message represents a chat message in the system.
// will be stored in MongoDB and contains all necessary information about the message,
//
// including its type, content, metadata, and timestamps.
type Message struct {
	ID         ulid.ULID          `bson:"_id,omitempty"`
	ChatID     primitive.ObjectID `bson:"chat_id"`
	SenderID   primitive.ObjectID `bson:"sender_id"`
	Type       string             `bson:"type"` // "text", "image", "system"
	Content    string             `bson:"content"`
	Metadata   map[string]any     `bson:"metadata,omitempty"` // additional info like image URL, system event details, etc.
	SequenceID int64              `bson:"seq_id"`             // monotonic sequence ID for ordering messages
	CreatedAt  time.Time          `bson:"created_at"`
	UpdatedAt  time.Time          `bson:"updated_at"`
	IsDeleted  bool               `bson:"is_deleted"`
}
