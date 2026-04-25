package ws

import (
	"context"
	"log/slog"
)

type Hub struct {
	// users: userID -> набор его активных соединений
	users      map[string]map[*Client]bool
	register   chan *Client
	unregister chan *Client
	Deliver    chan DeliveryTask
	logger     *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		users:      make(map[string]map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		Deliver:    make(chan DeliveryTask),
		logger:     logger,
	}
}

func (h *Hub) Run(ctx context.Context) {
	const op = "Hub.Run"
	h.logger.InfoContext(ctx, "Hub is running")

	for {
		select {

		case <-ctx.Done():
			h.logger.InfoContext(
				ctx, "Hub is shutting down",
				"op", op,
			)
			return

		case client := <-h.register:
			if _, ok := h.users[client.userID]; !ok {
				h.logger.InfoContext(
					ctx, "Registering new user in hub",
					"op", op,
					"userID", client.userID)
				h.users[client.userID] = make(map[*Client]bool)
			}
			h.users[client.userID][client] = true

		case client := <-h.unregister:
			if connections, ok := h.users[client.userID]; ok {
				if _, exists := connections[client]; exists {
					delete(connections, client)
					close(client.send)

					if len(connections) == 0 {
						h.logger.InfoContext(
							ctx, "No more active connections for user, removing from hub",
							"op", op,
							"userID", client.userID)
						delete(h.users, client.userID)
					}

				}
			}
		case task := <-h.Deliver:
			for _, userID := range task.TargetUserIDs {
				connections, ok := h.users[userID]
				if !ok {
					continue // Пользователь оффлайн на этом экземпляре сервера
				}

				for client := range connections {
					select {
					case client.send <- task.Payload:
						h.logger.DebugContext(
							ctx, "Delivered message to client",
							"op", op,
							"userID", client.userID,
						)
						// Сообщение успешно поставлено в канал отправки клиента
					default:
						// Буфер переполнен! Клиент медленный или завис
						// Принудительно отключаем его, чтобы Хаб не заблокировался
						h.logger.DebugContext(
							ctx, "Slow client detected, disconnecting",
							"op", op,
							"userID", client.userID,
						)

						close(client.send)
						delete(connections, client)
						if len(connections) == 0 {
							delete(h.users, client.userID)
						}
					}
				}
			}
		}
	}
}
