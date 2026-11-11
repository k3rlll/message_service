package ws

import (
	"encoding/json"

	domain "main/internal/domain/message_entity"
)

// Протокол WebSocket. Все фреймы - JSON в общем конверте.
//
// Клиент -> сервер:
//
//	{"type": "message.send", "ref": "1", "payload": {"chat_id": "...", "text": "..."}}
//	{"type": "typing",       "payload": {"chat_id": "..."}}
//	{"type": "chat.read",    "ref": "2", "payload": {"chat_id": "...", "message_id": "..."}}
//
// ref - произвольная строка клиента. Сервер возвращает ее в ответе на команду,
// чтобы клиент мог сопоставить ответ с запросом.
//
// Сервер -> клиент:
//
//	{"type": "connected", "payload": {...}}                  - соединение готово
//	{"type": "ack",   "ref": "1", "payload": {...}}          - команда выполнена
//	{"type": "error", "ref": "1", "payload": {code, message}} - команда не выполнена
//	{"id": "...", "type": "message.created", "chat_id": "...", "payload": {...}} - событие
//
// У долговечных событий есть id. Клиент запоминает id последнего полученного события
// и при переподключении передает его в параметре last_event_id, чтобы получить пропущенное.
// Событие может прийти дважды (при догрузке и вживую), клиент отбрасывает дубликаты по id.

// Команды клиента
const (
	frameMessageSend = "message.send"
	frameTyping      = "typing"
	frameChatRead    = "chat.read"
)

// Служебные фреймы сервера
const (
	frameConnected = "connected"
	frameAck       = "ack"
	frameError     = "error"
)

type inFrame struct {
	Type    string          `json:"type"`
	Ref     string          `json:"ref"`
	Payload json.RawMessage `json:"payload"`
}

type outFrame struct {
	Type    string `json:"type"`
	Ref     string `json:"ref,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

func (f outFrame) encode() []byte {
	data, _ := json.Marshal(f)
	return data
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type connectedPayload struct {
	UserID string `json:"user_id"`
	// Позиция в потоке событий на момент подключения
	LastEventID string `json:"last_event_id"`
	// true - догрузить пропущенные события не удалось (клиент отстал слишком сильно).
	// Клиент должен перезагрузить список чатов и историю через REST.
	ResyncRequired bool `json:"resync_required"`
}

// SendMessagePayload - тело команды message.send
type SendMessagePayload struct {
	ChatID          string              `json:"chat_id"`
	Text            string              `json:"text"`
	ClientMessageID string              `json:"client_msg_id"`
	ReplyToID       string              `json:"reply_to_id"`
	Attachments     []domain.Attachment `json:"attachments"`
}

type typingPayload struct {
	ChatID string `json:"chat_id"`
}

type chatReadPayload struct {
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
}

// DeliveryTask - задача для Хаба: доставить payload списку юзеров
type DeliveryTask struct {
	TargetUserIDs []string
	Payload       []byte
}
