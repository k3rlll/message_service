package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	domain "main/internal/domain/message_entity"
	"main/internal/transport/apierror"

	"github.com/coder/websocket"
	"github.com/oklog/ulid/v2"
)

const (
	writeTimeout = 10 * time.Second
	// PING поддерживает соединение живым и выявляет мертвые соединения.
	// С тем же интервалом продлевается онлайн-статус.
	pingInterval = 30 * time.Second
	// Время на завершающие действия после закрытия соединения
	cleanupTimeout = 5 * time.Second
)

// Backend - все, что нужно соединению от остального сервиса
type Backend interface {
	// Команды клиента
	SendMessage(ctx context.Context, userID string, p SendMessagePayload) (*domain.Message, error)
	Typing(ctx context.Context, userID, chatID string) error
	MarkRead(ctx context.Context, userID, chatID, messageID string) error

	// Онлайн-статус
	Connected(ctx context.Context, userID, connID string)
	Heartbeat(ctx context.Context, userID, connID string)
	Disconnected(ctx context.Context, userID, connID string)

	// Догрузка пропущенных событий
	LastEventID(ctx context.Context) (string, error)
	Replay(ctx context.Context, userID, afterID string) (frames [][]byte, lastID string, complete bool, err error)
}

// Client - одно WebSocket соединение пользователя.
// У одного пользователя может быть несколько соединений (несколько вкладок или устройств).
type Client struct {
	hub     *Hub
	conn    *websocket.Conn
	userID  string
	connID  string
	send    chan []byte
	backend Backend
	logger  *slog.Logger

	// Отмена контекста - единственный способ закрыть клиента.
	// Канал send никогда не закрывается, поэтому запись в него безопасна из любой горутины.
	ctx    context.Context
	cancel context.CancelFunc
}

func NewClient(
	ctx context.Context,
	hub *Hub,
	conn *websocket.Conn,
	userID string,
	sendBufferSize int,
	backend Backend,
	logger *slog.Logger,
) *Client {
	ctx, cancel := context.WithCancel(ctx)

	return &Client{
		hub:     hub,
		conn:    conn,
		userID:  userID,
		connID:  ulid.Make().String(),
		send:    make(chan []byte, sendBufferSize),
		backend: backend,
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Serve обслуживает соединение: догружает пропущенные события, регистрирует клиента
// в Хабе и читает команды. Блокируется, пока соединение не закроется.
//
// lastEventID - ID последнего события, которое клиент получил до обрыва связи.
// Пустая строка означает первое подключение.
func (c *Client) Serve(lastEventID string) {
	defer func() {
		c.cancel()
		c.conn.Close(websocket.StatusNormalClosure, "")

		c.logger.InfoContext(c.ctx, "WebSocket connection closed", "userID", c.userID)
	}()

	go c.writePump()

	cursor, resync := c.catchUp(lastEventID)

	c.hub.Register(c)
	defer c.hub.Unregister(c)

	// Между догрузкой и регистрацией в Хабе могли пройти события.
	// Второй проход закрывает этот зазор; он почти всегда пуст.
	if !resync {
		cursor, resync = c.replay(cursor)
	}
	if resync {
		// Догрузить не удалось: клиент начнет с текущего конца потока
		// и перезагрузит состояние через REST
		cursor = c.lastEventID()
	}

	c.backend.Connected(c.ctx, c.userID, c.connID)
	defer func() {
		// c.ctx к этому моменту отменен, поэтому нужен отдельный контекст
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), cleanupTimeout)
		defer cancel()
		c.backend.Disconnected(ctx, c.userID, c.connID)
	}()

	c.push(outFrame{Type: frameConnected, Payload: connectedPayload{
		UserID:         c.userID,
		LastEventID:    cursor,
		ResyncRequired: resync,
	}}.encode())

	c.readPump()
}

// Close инициирует закрытие соединения. Безопасно вызывать многократно.
func (c *Client) Close() {
	c.cancel()
}

// catchUp определяет стартовую позицию клиента в потоке событий
// и догружает пропущенное, если клиент переподключается
func (c *Client) catchUp(lastEventID string) (cursor string, resync bool) {
	if lastEventID == "" {
		return c.lastEventID(), false
	}
	return c.replay(lastEventID)
}

// replay отправляет клиенту события после cursor.
// resync == true значит, что догрузить без потерь не получилось.
func (c *Client) replay(cursor string) (newCursor string, resync bool) {
	frames, lastID, complete, err := c.backend.Replay(c.ctx, c.userID, cursor)
	if err != nil {
		c.logger.ErrorContext(c.ctx, "Failed to replay events",
			"userID", c.userID, "error", err)
		return cursor, true
	}
	if !complete {
		return cursor, true
	}

	for _, frame := range frames {
		if !c.push(frame) {
			break
		}
	}
	return lastID, false
}

func (c *Client) lastEventID() string {
	id, err := c.backend.LastEventID(c.ctx)
	if err != nil {
		c.logger.ErrorContext(c.ctx, "Failed to get last event ID",
			"userID", c.userID, "error", err)
	}
	return id
}

// enqueue ставит сообщение в очередь отправки без блокировки.
// Возвращает false, если буфер клиента переполнен.
// Используется Хабом: он не может ждать медленного клиента.
func (c *Client) enqueue(payload []byte) bool {
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

// push ставит сообщение в очередь отправки, дожидаясь места в буфере.
// Используется самим соединением: ответы на команды и догрузка событий
// не должны теряться из-за заполненного буфера.
func (c *Client) push(payload []byte) bool {
	select {
	case c.send <- payload:
		return true
	case <-c.ctx.Done():
		return false
	}
}

// readPump читает и выполняет команды клиента
func (c *Client) readPump() {
	const op = "Client.readPump"

	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			expected := status == websocket.StatusNormalClosure ||
				status == websocket.StatusGoingAway ||
				c.ctx.Err() != nil

			if !expected {
				c.logger.WarnContext(c.ctx, "Error reading from client connection",
					"op", op, "userID", c.userID, "error", err)
			}
			return
		}

		var frame inFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			c.replyError(frame.Ref, apierror.Info{Code: "INVALID_REQUEST", Message: "invalid JSON"})
			continue
		}

		c.handle(frame)
	}
}

func (c *Client) handle(frame inFrame) {
	switch frame.Type {
	case frameMessageSend:
		var p SendMessagePayload
		if !c.decode(frame, &p) {
			return
		}

		// Отправителю сообщение придет еще и событием message.created,
		// тем же путем, что и остальным участникам: через шину и Хаб
		msg, err := c.backend.SendMessage(c.ctx, c.userID, p)
		c.reply(frame, msg, err)

	case frameTyping:
		var p typingPayload
		if !c.decode(frame, &p) {
			return
		}

		// На успешный typing не отвечаем: это частый сигнал, подтверждение ему не нужно
		if err := c.backend.Typing(c.ctx, c.userID, p.ChatID); err != nil {
			c.reply(frame, nil, err)
		}

	case frameChatRead:
		var p chatReadPayload
		if !c.decode(frame, &p) {
			return
		}

		err := c.backend.MarkRead(c.ctx, c.userID, p.ChatID, p.MessageID)
		c.reply(frame, nil, err)

	default:
		c.replyError(frame.Ref, apierror.Info{
			Code:    "INVALID_REQUEST",
			Message: "unknown frame type: " + frame.Type,
		})
	}
}

func (c *Client) decode(frame inFrame, payload any) bool {
	if err := json.Unmarshal(frame.Payload, payload); err != nil {
		c.replyError(frame.Ref, apierror.Info{Code: "INVALID_REQUEST", Message: "invalid payload"})
		return false
	}
	return true
}

// reply отвечает на команду: ack при успехе, error при ошибке
func (c *Client) reply(frame inFrame, payload any, err error) {
	if err == nil {
		c.push(outFrame{Type: frameAck, Ref: frame.Ref, Payload: payload}.encode())
		return
	}

	info, known := apierror.Describe(err)
	if !known && c.ctx.Err() == nil {
		c.logger.ErrorContext(c.ctx, "WebSocket command failed",
			"userID", c.userID, "type", frame.Type, "error", err)
	}
	c.replyError(frame.Ref, info)
}

func (c *Client) replyError(ref string, info apierror.Info) {
	c.push(outFrame{
		Type:    frameError,
		Ref:     ref,
		Payload: errorPayload{Code: info.Code, Message: info.Message},
	}.encode())
}

// writePump отправляет клиенту сообщения из канала send и периодические PING
func (c *Client) writePump() {
	const op = "Client.writePump"

	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		// Любой выход из writePump закрывает клиента целиком, readPump выйдет следом
		c.cancel()
	}()

	for {
		select {
		case <-c.ctx.Done():
			return

		case message := <-c.send:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, message)
			cancel()

			if err != nil {
				if c.ctx.Err() == nil {
					c.logger.WarnContext(c.ctx, "Error writing message to client",
						"op", op, "userID", c.userID, "error", err)
				}
				return
			}

		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.conn.Ping(ctx)
			cancel()

			if err != nil {
				if c.ctx.Err() == nil {
					c.logger.WarnContext(c.ctx, "Client did not respond to PING",
						"op", op, "userID", c.userID, "error", err)
				}
				return
			}

			c.backend.Heartbeat(c.ctx, c.userID, c.connID)
		}
	}
}
