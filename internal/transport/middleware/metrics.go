package middleware

import (
	"strconv"
	"time"

	"main/internal/metrics"

	"github.com/labstack/echo/v4"
)

// MetricsMiddleware считает запросы и их длительность.
//
// В метку route идет шаблон маршрута (/api/v1/chats/:chat_id), а не реальный путь:
// иначе каждый ID породил бы отдельный временной ряд.
func MetricsMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()

			err := next(c)
			if err != nil && !c.Response().Committed {
				// Пишем ответ сразу, чтобы в метрику попал итоговый статус.
				// Ошибку возвращаем дальше: обработчик ошибок пропустит уже отправленный ответ.
				c.Error(err)
			}

			route := c.Path()
			if route == "" {
				route = "unmatched"
			}
			method := c.Request().Method

			metrics.HTTPRequests.
				WithLabelValues(method, route, strconv.Itoa(c.Response().Status)).
				Inc()

			// Длительность WebSocket-запроса - это время жизни соединения, в гистограмму ее не кладем
			if !isWebSocketUpgrade(c) {
				metrics.HTTPDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
			}

			return err
		}
	}
}

func isWebSocketUpgrade(c echo.Context) bool {
	return c.IsWebSocket()
}
