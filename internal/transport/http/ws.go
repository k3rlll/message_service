package http

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
)


type Client struct {
	ID   string
	Conn *websocket.Conn
}

type BroadcastMessage struct {
	ChatID  string
	Message string
}

type Hub struct {
	Clients map[string]*Client

	Register chan *Client

	Unregister chan *Client

	Broadcast chan BroadcastMessage
}

func (h *Handler) WSHandler(c echo.Context) error {
	const op = "http.Handler.WSHandler"
	userID := c.Get("userID").(string)
	ctx := c.Request().Context()
	h.logger.InfoContext(
		ctx,
		"Received WebSocket connection",
		slog.String("op", op),
		slog.String("userID", userID),
	)

	function_OnPingReceived := func(ctx context.Context, payload []byte) bool {
		//TODO: Довести до ума редис обновление статуса
		userID, ok := ctx.Value("userID").(string)
		if !ok {
			return true // Все равно отвечаем Pong, чтобы не дропать связь
		}
		userID = userID
		// Обновление статуса в Redis асинхронно
		// Мы используем context.Background(), так как обновление статуса
		// не должно прерываться, если само соединение закроется в этот момент.
		// go func(id string) {
		// 	// Ключ: "user:online:123", Значение: "1" (просто флаг)
		// 	// TTL: 60 секунд. Если пинги перестанут приходить, ключ сам удалится.
		// 	key := fmt.Sprintf("user:online:%s", id)
		// 	err := redisClient.Set(context.Background(), key, "1", 60*time.Second).Err()
		// 	if err != nil {
		// 		log.Printf("Failed to update status in Redis for %s: %v", id, err)
		// 	}
		// }(userID)

		return true // Отправить автоматический Pong клиенту
	}

	options := &websocket.AcceptOptions{
		InsecureSkipVerify:   h.cfg.Websocket.InsecureSkipVerify,
		CompressionThreshold: h.cfg.Websocket.CompressionThreshold,
		OnPingReceived:       function_OnPingReceived,
	}

	// Апгрейд соединения. Echo предоставляет нужные ResponseWriter и Request
	conn, err := websocket.Accept(c.Response(), c.Request(), options)
	if err != nil {
		c.Logger().Errorf("failed to accept ws: %v", err)
		return err // Accept сам отправляет нужные HTTP ошибки
	}

	// Важно закрывать соединение при выходе из функции
	defer conn.Close(websocket.StatusInternalError, "handler closed")

	client := &Client{
		ID:   userID,
		Conn: conn,
	}

	// запуск цикла чтения
	for {
		err := processMessage(ctx, client)
		if err != nil {
			// нормальное закрытие соединения клиентом или обрыв
			status := websocket.CloseStatus(err)
			fmt.Printf("User %s disconnected. Status: %v\n", client.ID, status)
			break
		}
	}

	// Возвращаем nil, Echo поймет, что соединение обработано
	return nil
}

func processMessage(ctx context.Context, c *Client) error {
	// читаем из сокета
	typ, data, err := c.Conn.Read(ctx)
	if err != nil {
		return err
	}

	bytesMessage := string(data)

	return

}
