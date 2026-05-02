package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"syscall"
	"time"

	domainChat "main/internal/domain/chat_entity"

	"github.com/coder/websocket"
	"github.com/go-redis/redis/v8"
)

const (
	maxMessageSize = 512
	bufferSize     = 256

	// timeTicker должен быть меньше, чем таймаут для PING/PONG,
	//  чтобы мы успевали отправлять PING до того,
	//  как соединение будет признано мертвым
	timeTicker = 54
)

// Usecase логика, которая нужна клиенту для проверки прав доступа
// и получения информации о чатах
type InMemoryCache interface {
	IsUserInChat(ctx context.Context, userID, chatID string) (bool, error)
}

type MessageUsecase interface {
	SaveMessage(ctx context.Context, chatID, senderID, text string) (string, time.Time, error)
}

type Client struct {
	Hub    *Hub
	Conn   *websocket.Conn
	UserID string
	Send   chan []byte

	MessageUsecase MessageUsecase
	Cache          InMemoryCache
	logger         *slog.Logger
}

func NewClient(hub *Hub, conn *websocket.Conn, userID string, logger *slog.Logger) *Client {
	return &Client{
		Hub:    hub,
		Conn:   conn,
		UserID: userID,
		Send:   make(chan []byte, bufferSize),
		logger: logger,
	}
}

// readPump читает сообщения от клиента, обрабатывает их и публикует в Redis
func (c *Client) ReadPump(ctx context.Context, rdb *redis.Client) {

	op := "Client.readPump"
	c.logger.InfoContext(ctx, "Starting read pump for client", "op", op, "userID", c.UserID)

	// Если мы вышли из цикла, значит сокет закрыт или произошла ошибка
	defer func() {
		c.Hub.Unregister <- c
		c.logger.InfoContext(
			ctx, "Read pump closed for client",
			"op", op,
			"userID", c.UserID,
		)
		c.Conn.Close(websocket.StatusInternalError, "read loop closed")
	}()

	c.Conn.SetReadLimit(maxMessageSize)

	for {
		_, payload, err := c.Conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure ||
				websocket.CloseStatus(err) == websocket.StatusGoingAway ||
				syscall.EPIPE == err || // Клиент закрыл соединение
				net.ErrClosed == err {
				c.logger.InfoContext(
					ctx, "Client closed the connection",
					"op", op,
					"userID", c.UserID,
				)
			} else {
				c.logger.ErrorContext(
					ctx, "Error reading from client connection",
					"op", op,
					"userID", c.UserID,
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
				"userID", c.UserID,
				"error", err,
			)
			continue // Игнорируем мусор
		}

		isUserInChat, err := c.Cache.IsUserInChat(ctx, c.UserID, incoming.ChatID)
		//TODO: Handle errors properly
		if err != nil {
			if errors.Is(err, domainChat.ErrAccessDenied) {
				c.logger.InfoContext(
					ctx, "User is not a member of the chat",
					"op", op,
					"userID", c.UserID,
					"chatID", incoming.ChatID,
				)
				continue
			}
			if errors.Is(err, domainChat.ErrChatNotFound) {
				c.logger.InfoContext(
					ctx, "Chat not found",
					"op", op,
					"userID", c.UserID,
					"chatID", incoming.ChatID,
				)
				continue
			}
			c.logger.ErrorContext(
				ctx, "Error checking if user is in chat",
				"op", op,
				"userID", c.UserID,
				"chatID", incoming.ChatID,
				"error", err,
			)

			continue
		}
		if !isUserInChat {
			c.logger.InfoContext(
				ctx, "User is not a member of the chat",
				"op", op,
				"userID", c.UserID,
				"chatID", incoming.ChatID,
			)
			continue
		}

		// Сохраняем сообщение в БД, получаем ID и дату создания
		messageID, createdAt, err := c.MessageUsecase.SaveMessage(
			ctx,
			incoming.ChatID,
			c.UserID,
			incoming.Text)
		//TODO: Handle errors properly
		if err != nil {
			c.logger.ErrorContext(
				ctx, "Error saving message",
				"op", op,
				"userID", c.UserID,
				"chatID", incoming.ChatID,
				"error", err,
			)
			continue
		}

		// Формируем финальное сообщение
		finalMsg := OutgoingMessage{
			ID:        messageID,
			ChatID:    incoming.ChatID,
			SenderID:  c.UserID,
			Text:      incoming.Text,
			CreatedAt: createdAt,
		}

		// ПУБЛИКАЦИЯ В REDIS PUB/SUB
		// Публикуем в канал чата
		bytesToSend, err := json.Marshal(finalMsg)
		if err != nil {
			c.logger.ErrorContext(
				ctx, "Error marshaling final message",
				"op", op,
				"userID", c.UserID,
				"error", err,
			)
			continue
		}
		rdb.Publish(ctx, "channel:chat_events", bytesToSend)
	}
}

// writePump читает сообщения из Redis канала send и отправляет их клиенту
func (c *Client) WritePump(ctx context.Context) {
	op := "Client.writePump"
	c.logger.InfoContext(ctx, "Starting write pump for client", "op", op, "userID", c.UserID)

	// Определяем частоту отправки ping сообщений чтобы поддерживать соединение живым
	ticker := time.NewTicker(timeTicker * time.Second)

	defer func() {
		ticker.Stop()
		c.logger.InfoContext(
			ctx, "Write pump closed for client",
			"op", op,
			"userID", c.UserID,
		)
		c.Conn.Close(websocket.StatusInternalError, "write loop closed")
	}()

	for {
		select {
		case <-ctx.Done():
			c.logger.InfoContext(
				ctx, "Context cancelled, stopping write pump",
				"op", op,
				"userID", c.UserID,
			)
			return
		case message, ok := <-c.Send:
			if !ok {
				// Канал закрыт это значит хаб решил отключить клиента
				c.logger.InfoContext(
					ctx, "Send channel closed, stopping write pump",
					"op", op,
					"userID", c.UserID,
				)
				return
			}

			ctxWrite, cancel := context.WithTimeout(ctx, 10*time.Second)

			err := c.Conn.Write(ctxWrite, websocket.MessageText, message)
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
					"userID", c.UserID,
				)
			default:
				c.logger.ErrorContext(
					ctx, "Error writing message to client",
					"op", op,
					"userID", c.UserID,
					"error", err,
				)

			}

		case <-ticker.C:
			// Отправляем PING чтобы поддерживать соединение живым
			ctxPing, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Conn.Ping(ctxPing)
			cancel()
			if err != nil {
				c.logger.WarnContext(
					ctx, "Error sending PING to client",
					"op", op,
					"userID", c.UserID,
					"error", err,
				)
				return
			}
		}

	}
}
