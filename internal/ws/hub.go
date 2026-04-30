package ws

import (
	"context"
	"log/slog"
)

type Hub struct {
	// users: userID - набор его активных соединений
	Users      map[string]map[*Client]bool
	Register   chan *Client
	Unregister chan *Client
	Deliver    chan DeliveryTask
	logger     *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		Users:      make(map[string]map[*Client]bool),
		Register:   make(chan *Client),
		Unregister: make(chan *Client),
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

		case client := <-h.Register:
			if _, ok := h.Users[client.UserID]; !ok {
				h.logger.InfoContext(
					ctx, "Registering new user in hub",
					"op", op,
					"userID", client.UserID)
				h.Users[client.UserID] = make(map[*Client]bool)
			}
			h.Users[client.UserID][client] = true

		case client := <-h.Unregister:
			if connections, ok := h.Users[client.UserID]; ok {
				if _, exists := connections[client]; exists {
					delete(connections, client)
					close(client.Send)

					if len(connections) == 0 {
						h.logger.InfoContext(
							ctx, "No more active connections for user, removing from hub",
							"op", op,
							"userID", client.UserID)
						delete(h.Users, client.UserID)
					}

				}
			}
		case task := <-h.Deliver:
			for _, userID := range task.TargetUserIDs {
				connections, ok := h.Users[userID]
				if !ok {
					h.logger.DebugContext(
						ctx, "User is offline, skipping delivery",
						"op", op,
						"userID", userID,
					)
					continue // Пользователь оффлайн на этом экземпляре сервера
				}

				for client := range connections {
					select {
					case client.Send <- task.Payload:
						h.logger.DebugContext(
							ctx, "Delivered message to client",
							"op", op,
							"userID", client.UserID,
						)
						// Сообщение успешно поставлено в канал отправки клиента
					default:
						// Буфер переполнен! Клиент медленный или завис
						// Принудительно отключаем его чтобы Хаб не заблокировался
						h.logger.DebugContext(
							ctx, "Slow client detected, disconnecting",
							"op", op,
							"userID", client.UserID,
						)

						close(client.Send)
						delete(connections, client)
						if len(connections) == 0 {
							delete(h.Users, client.UserID)
						}
					}
				}
			}
		}
	}
}
