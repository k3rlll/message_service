package app

import (
	"errors"
	"log/slog"

	mddlwr "main/internal/transport/middleware"
	errHandler "main/pkg/error_handler"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type CustomValidator struct {
	validator *validator.Validate
}

func (cv *CustomValidator) Validate(i interface{}) error {
	return cv.validator.Struct(i)
}

// NewServer собирает Echo: middleware, обработчик ошибок и маршруты.
// Сам сервер не запускает.
func NewServer(logger *slog.Logger, handlers Handlers, deps RouteDeps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Validator = &CustomValidator{validator: validator.New()}

	// custom error handler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		errHandler.CustomHTTPErrorHandler(err, c, logger)
	}

	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(mddlwr.RequestIDToContextMiddleware())
	e.Use(middleware.CORS())
	if deps.Instance != "" {
		// Какой экземпляр обработал запрос: видно, как балансировщик раскидывает нагрузку
		e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				c.Response().Header().Set("X-Instance", deps.Instance)
				return next(c)
			}
		})
	}
	// Метрики снаружи логгера запросов: так в них попадает итоговый статус ответа
	e.Use(mddlwr.MetricsMiddleware())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		// Логируем путь без query-строки: в ней может быть токен WebSocket-подключения
		LogURIPath:  true,
		LogStatus:   true,
		LogLatency:  true, // log the time taken to process the request
		LogMethod:   true,
		LogError:    true,
		LogRemoteIP: true, // log the client's IP address for better traceability
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			// request_id добавит логгер из контекста запроса
			ctx := c.Request().Context()

			attrs := []any{
				slog.String("method", v.Method),
				slog.String("path", v.URIPath),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
				slog.String("remote_ip", v.RemoteIP),
			}

			if v.Error != nil {
				var he *echo.HTTPError
				if errors.As(v.Error, &he) {
					attrs = append(attrs, slog.Any("err", he.Message))
				} else {
					attrs = append(attrs, slog.String("err", v.Error.Error()))
				}
			}

			switch {
			case v.Status >= 500:
				logger.ErrorContext(ctx, "HTTP_SERVER_ERROR", attrs...)
			case v.Status >= 400:
				logger.WarnContext(ctx, "HTTP_CLIENT_ERROR", attrs...)
			default:
				logger.InfoContext(ctx, "HTTP_OK", attrs...)
			}
			return nil
		},
	}))

	MapRoutes(e, handlers, deps)

	return e
}
