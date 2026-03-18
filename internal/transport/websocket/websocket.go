package websocket

import (
	"log/slog"
	"main/internal/configs"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/time/rate"
)

type subscriber struct {
	// ID is the unique identifier for the subscriber.
	ID string
	// Send is a channel through which messages are sent to the subscriber.
	Send chan []byte
}

type WebsocketTransport struct {
	echo           *echo.Echo
	publishLimiter *rate.Limiter
	Mu             sync.Mutex
	subscribers    map[*subscriber]struct{}
	logger         *slog.Logger
}

func NewWebsocketTransport(cfg *configs.Config, echo *echo.Echo, logger *slog.Logger) *WebsocketTransport {
	return &WebsocketTransport{
		echo:           echo,
		publishLimiter: rate.NewLimiter(rate.Every(time.Millisecond*time.Duration(cfg.Websocket.Interval)), cfg.Websocket.PublishBurst),
		subscribers:    make(map[*subscriber]struct{}),
		logger:         logger,
	}
}

func (wt *WebsocketTransport) AddSubscriber(sub *subscriber) {
	wt.Mu.Lock()
	defer wt.Mu.Unlock()
	wt.subscribers[sub] = struct{}{}
}

func (wt *WebsocketTransport) RemoveSubscriber(sub *subscriber) {
	wt.Mu.Lock()
	defer wt.Mu.Unlock()
	delete(wt.subscribers, sub)
}
