package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/go-redis/redis/v8"
)

const (
	maxMessageSize = 512
	bufferSize     = 256
	timeTicker     = 54 // timeTicker должен быть меньше, чем таймаут для PING/PONG, чтобы мы успевали отправлять PING до того, как соединение будет признано мертвым
)

type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	userID string
	send   chan []byte

	logger *slog.Logger
}

func NewClient(hub *Hub, conn *websocket.Conn, userID string, logger *slog.Logger) *Client {
	return &Client{
		hub:    hub,
		conn:   conn,
		userID: userID,
		send:   make(chan []byte, bufferSize),
		logger: logger,
	}
}

// readPump читает сообщения от клиента, обрабатывает их и публикует в Redis
func (c *Client) readPump(ctx context.Context, rdb *redis.Client) {

	op := "Client.readPump"
	c.logger.InfoContext(ctx, "Starting read pump for client", "op", op, "userID", c.userID)

	// Если мы вышли из цикла, значит сокет закрыт или произошла ошибка
	defer func() {
		c.hub.unregister <- c
		c.logger.InfoContext(
			ctx, "Read pump closed for client",
			"op", op,
			"userID", c.userID,
		)
		c.conn.Close(websocket.StatusInternalError, "read loop closed")
	}()

	c.conn.SetReadLimit(maxMessageSize)

	for {
		_, payload, err := c.conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway ||
				syscall.EPIPE == err || // Клиент закрыл соединение
				net.ErrClosed == err {
				c.logger.InfoContext(
					ctx, "Client closed the connection",
					"op", op,
					"userID", c.userID,
				)
			} else {
				c.logger.ErrorContext(
					ctx, "Error reading from client connection",
					"op", op,
					"userID", c.userID,
					"error", err,
				)
			}
			return
		}

		var incoming IncomingMessage
		if err := json.Unmarshal(payload, &incoming); err != nil {
			c.logger.ErrorContext(
				ctx, "Error unmarshaling incoming message",
				"op", op,
				"userID", c.userID,
				"error", err,
			)
			continue // Игнорируем мусор
		}

		// TODO: Дальше нам нужно:
		// Проверяем в in-memory/Redis/БД: Является ли c.userID членом incoming.ChatID?
		// if !isMember(ctx, c.userID, incoming.ChatID) { continue }
		// Сохраняем сообщение в БД, получаем ID и дату создания

		// Формируем финальное сообщение
		finalMsg := OutgoingMessage{
			//TODO: ID
			ID:        "uuid-v7-from-db", // Генерится БД
			ChatID:    incoming.ChatID,
			SenderID:  c.userID,
			Text:      incoming.Text,
			CreatedAt: time.Now(),
		}

		// ПУБЛИКАЦИЯ В REDIS PUB/SUB
		// Публикуем в канал чата 
		bytesToSend, err := json.Marshal(finalMsg)
		if err != nil {
			c.logger.ErrorContext(
				ctx, "Error marshaling final message",
				"op", op,
				"userID", c.userID,
				"error", err,
			)
			continue
		}
		rdb.Publish(ctx, "channel:chat_events", bytesToSend)
	}
}

// writePump читает сообщения из канала send и отправляет их клиенту
func (c *Client) writePump(ctx context.Context) {
	op := "Client.writePump"
	c.logger.InfoContext(ctx, "Starting write pump for client", "op", op, "userID", c.userID)

	ticker := time.NewTicker(timeTicker * time.Second)

	defer func() {
		ticker.Stop()
		c.logger.InfoContext(
			ctx, "Write pump closed for client",
			"op", op,
			"userID", c.userID,
		)
		c.conn.Close(websocket.StatusInternalError, "write loop closed")
	}()

	for {
		select {
		case <-ctx.Done():
			c.logger.InfoContext(
				ctx, "Context cancelled, stopping write pump",
				"op", op,
				"userID", c.userID,
			)
			return
		case message, ok := <-c.send:
			if !ok {
				// Канал закрыт - значит хаб решил отключить клиента
				c.logger.InfoContext(
					ctx, "Send channel closed, stopping write pump",
					"op", op,
					"userID", c.userID,
				)
				return
			}

			ctxWrite, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Write(ctxWrite, websocket.MessageText, message)
			cancel()
			switch {
			case err == nil:
				// Успешная отправка
			case websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway ||
				syscall.EPIPE == err || // Клиент закрыл соединение
				net.ErrClosed == err:
				c.logger.InfoContext(
					ctx, "Client closed the connection during write",
					"op", op,
					"userID", c.userID,
				)
			default:
				c.logger.ErrorContext(
					ctx, "Error writing message to client",
					"op", op,
					"userID", c.userID,
					"error", err,
				)
			}
		case <-ticker.C:
			// Отправляем PING, чтобы поддерживать соединение живым
			ctxPing, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Ping(ctxPing)
			cancel()
			if err != nil {
				c.logger.WarnContext(
					ctx, "Error sending PING to client",
					"op", op,
					"userID", c.userID,
					"error", err,
				)
				return
			}
		}

	}
}
