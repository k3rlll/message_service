package ws

import (
	"context"
	"encoding/json"
	"time"

	"github.com/coder/websocket"
	"github.com/go-redis/redis/v8"
)

const (
	maxMessageSize = 512
)

type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	userID string
	send   chan []byte
}

func (c *Client) readPump(ctx context.Context, rdb *redis.Client) {
	// Если мы вышли из цикла, значит сокет закрыт или произошла ошибка
	defer func() {
		c.hub.unregister <- c
		c.conn.Close(websocket.StatusInternalError, "read loop closed")
	}()

	c.conn.SetReadLimit(maxMessageSize)

	for {
		_, payload, err := c.conn.Read(ctx)
		if err != nil {
			break // Нормальный дисконнект (CloseStatus) или обрыв сети
		}

		var incoming IncomingMessage
		if err := json.Unmarshal(payload, &incoming); err != nil {
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
		// Публикуем в канал чата (или в общий пайплайн)
		bytesToSend, _ := json.Marshal(finalMsg)
		rdb.Publish(ctx, "channel:chat_events", bytesToSend)
	}
}
