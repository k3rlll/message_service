package message_entity

import "time"

const (
	TypeText   = "text"
	TypeImage  = "image"
	TypeVideo  = "video"
	TypeAudio  = "audio"
	TypeFile   = "file"
	TypeSystem = "system"

	// Максимальная длина содержимого сообщения в символах
	MaxContentLength = 4000
	MaxAttachments   = 10
	MaxEmojiLength   = 32 // в байтах: составные эмодзи бывают длинными
	// Максимальная длина клиентского ID сообщения (ключ идемпотентности)
	MaxClientMessageIDLength = 64
)

// Attachment - файл, загруженный в объектное хранилище.
// Key - путь объекта в бакете; ссылку на скачивание клиент получает отдельным запросом.
type Attachment struct {
	Key         string `bson:"key" json:"key"`
	Name        string `bson:"name" json:"name"`
	ContentType string `bson:"content_type" json:"content_type"`
	Size        int64  `bson:"size" json:"size"`
}

type Reaction struct {
	Emoji  string `bson:"emoji" json:"emoji"`
	UserID string `bson:"user_id" json:"user_id"`
}

// Message - сообщение в чате.
// ID - это ULID: он лексикографически сортируется по времени создания,
// поэтому используется и как первичный ключ, и как курсор пагинации.
type Message struct {
	ID       string `bson:"_id" json:"id"`
	ChatID   string `bson:"chat_id" json:"chat_id"`
	SenderID string `bson:"sender_id" json:"sender_id"`
	Type     string `bson:"type" json:"type"`
	Content  string `bson:"content" json:"content"`
	// ClientMessageID - ID, который клиент генерирует перед отправкой.
	// Повторная отправка с тем же ID не создает дубликат сообщения.
	ClientMessageID string         `bson:"client_msg_id,omitempty" json:"client_msg_id,omitempty"`
	ReplyToID       string         `bson:"reply_to_id,omitempty" json:"reply_to_id,omitempty"`
	Attachments     []Attachment   `bson:"attachments,omitempty" json:"attachments,omitempty"`
	Reactions       []Reaction     `bson:"reactions,omitempty" json:"reactions,omitempty"`
	Metadata        map[string]any `bson:"metadata,omitempty" json:"metadata,omitempty"`
	IsEdited        bool           `bson:"is_edited" json:"is_edited"`
	// DeletedAt задан у удаленных сообщений. Сообщение остается в истории как "надгробие":
	// содержимое, вложения и реакции стираются, но на него могут ссылаться ответы.
	DeletedAt *time.Time `bson:"deleted_at,omitempty" json:"deleted_at,omitempty"`
	CreatedAt time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time  `bson:"updated_at" json:"updated_at"`
}

func (m *Message) IsDeleted() bool {
	return m.DeletedAt != nil
}
