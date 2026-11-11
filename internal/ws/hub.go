package ws

import (
	"context"
	"log/slog"
)

// Hub хранит активные соединения и раздает им сообщения.
// Вся работа с картой соединений идет в одной горутине (Run), поэтому мьютексы не нужны.
type Hub struct {
	// users: userID - набор его активных соединений
	users      map[string]map[*Client]struct{}
	register   chan *Client
	unregister chan *Client
	deliver    chan DeliveryTask
	// done закрывается при остановке Хаба, чтобы отправители не зависали навсегда
	done   chan struct{}
	logger *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		users:      make(map[string]map[*Client]struct{}),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		deliver:    make(chan DeliveryTask, 256),
		done:       make(chan struct{}),
		logger:     logger,
	}
}

// Register добавляет клиента в Хаб. Если Хаб уже остановлен, клиент закрывается.
func (h *Hub) Register(client *Client) {
	select {
	case h.register <- client:
	case <-h.done:
		client.Close()
	}
}

// Unregister убирает клиента из Хаба.
func (h *Hub) Unregister(client *Client) {
	select {
	case h.unregister <- client:
	case <-h.done:
	}
}

// Deliver ставит задачу доставки в очередь Хаба.
func (h *Hub) Deliver(ctx context.Context, task DeliveryTask) {
	select {
	case h.deliver <- task:
	case <-h.done:
	case <-ctx.Done():
	}
}

// Run обрабатывает события Хаба, пока не отменен контекст.
// При остановке закрывает все активные соединения.
func (h *Hub) Run(ctx context.Context) {
	const op = "Hub.Run"
	h.logger.InfoContext(ctx, "Hub is running", "op", op)

	defer func() {
		close(h.done)
		for _, connections := range h.users {
			for client := range connections {
				client.Close()
			}
		}
		h.logger.InfoContext(ctx, "Hub stopped", "op", op)
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case client := <-h.register:
			if _, ok := h.users[client.userID]; !ok {
				h.users[client.userID] = make(map[*Client]struct{})
			}
			h.users[client.userID][client] = struct{}{}

			h.logger.DebugContext(ctx, "Client registered",
				"op", op, "userID", client.userID)

		case client := <-h.unregister:
			h.remove(client)

			h.logger.DebugContext(ctx, "Client unregistered",
				"op", op, "userID", client.userID)

		case task := <-h.deliver:
			for _, userID := range task.TargetUserIDs {
				// Если соединений нет - пользователь оффлайн на этом экземпляре сервера
				for client := range h.users[userID] {
					if client.enqueue(task.Payload) {
						continue
					}

					// Буфер переполнен: клиент медленный или завис.
					// Отключаем его, чтобы Хаб не блокировался
					h.logger.WarnContext(ctx, "Slow client detected, disconnecting",
						"op", op, "userID", client.userID)

					h.remove(client)
					client.Close()
				}
			}
		}
	}
}

func (h *Hub) remove(client *Client) {
	connections, ok := h.users[client.userID]
	if !ok {
		return
	}

	delete(connections, client)
	if len(connections) == 0 {
		delete(h.users, client.userID)
	}
}
