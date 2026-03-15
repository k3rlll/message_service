package models

import (
	"time"
)

// Message represents a chat message in the system.
// will be stored in MongoDB and contains all necessary information about the message,
//
// including its type, content, metadata, and timestamps.
type Message struct {
	ID        string         `bson:"_id,omitempty"`
	ChatID    string         `bson:"chat_id"`
	SenderID  string         `bson:"sender_id"`
	Type      string         `bson:"type"`               // "text", "image", "system"
	Content   string         `bson:"content"`            // for text messages, this is the text content; for images, this could be a URL or base64 string; for system events, this could be a description of the event
	Metadata  map[string]any `bson:"metadata,omitempty"` // additional info like image URL, system event details, etc.
	CreatedAt time.Time      `bson:"created_at"`
	UpdatedAt time.Time      `bson:"updated_at"`
}
