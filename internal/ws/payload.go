package ws

import "time"

type IncomingMessage struct {
	ChatID string `json:"chat_id" validate:"required,ulid"`
	Text   string `json:"text" validate:"required"`
}

// OutgoingMessage шлем клиенту уже обогащенное
type OutgoingMessage struct {
	ID        string    `json:"id"`
	ChatID    string    `json:"chat_id"`
	SenderID  string    `json:"sender_id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// DeliveryTask - задача для Хаба: доставить payload списку юзеров
type DeliveryTask struct {
	TargetUserIDs []string
	Payload       []byte
}
