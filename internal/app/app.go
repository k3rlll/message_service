package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	config "main/internal/configs"
	handler "main/internal/transport/http"

	otter "github.com/maypok86/otter/v2"

	uc "main/internal/usecase"
	errHandler "main/pkg/error_handler"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"
)

type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter функция для создания адаптера
func NewSlogAdapter(l *slog.Logger) *SlogAdapter {
	return &SlogAdapter{logger: l}
}

// Реализуем метод Warn для интерфейса otter.Logger
func (a *SlogAdapter) Warn(ctx context.Context, msg string, err error) {
	// Вызываем WarnContext у slog, чтобы не потерять контекст,
	// и прокидываем ошибку как атрибут
	a.logger.WarnContext(ctx, msg, slog.Any("error", err))
}

// Реализуем метод Error для интерфейса otter.Logger
func (a *SlogAdapter) Error(ctx context.Context, msg string, err error) {
	a.logger.ErrorContext(ctx, msg, slog.Any("error", err))
}

type CustomValidator struct {
	validator *validator.Validate
}

func (cv *CustomValidator) Validate(i interface{}) error {
	return cv.validator.Struct(i)
}

func Run(
	cfg config.Config,
	logger *slog.Logger,
	jwt JWTManager,
	usecase *uc.Usecase,
	redisClient *redis.Client) *echo.Echo {

	// адаптер для otter, чтобы он логировал через slog и не терять контекст
	otterLogger := NewSlogAdapter(logger)
	cacheOptions := &otter.Options[int, int]{
		MaximumSize:      cfg.InMemoryCache.MaximumSize,
		ExpiryCalculator: otter.ExpiryAccessing[int, int](time.Minute * time.Duration(cfg.InMemoryCache.ExpiryMinutes)),
		InitialCapacity:  cfg.InMemoryCache.InitialCapacity,
		Logger:           otterLogger,
	}

	e := echo.New()
	e.HideBanner = true
	e.Validator = &CustomValidator{validator: validator.New()}

	// custom error handler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		errHandler.CustomHTTPErrorHandler(err, c, logger)
	}

	e.Use(middleware.Recover())
	e.Use(middleware.CORS())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:      true,
		LogStatus:   true,
		LogLatency:  true, // log the time taken to process the request
		LogMethod:   true,
		LogError:    true,
		LogRemoteIP: true, // log the client's IP address for better traceability in future, jusst in case
		HandleError: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			attrs := []any{
				slog.String("request_id", c.Response().Header().Get("X-Request-ID")),
				slog.String("method", v.Method),
				slog.String("uri", v.URI),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
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
				logger.Error("HTTP_SERVER_ERROR", attrs...)
			case v.Status >= 400:
				logger.Warn("HTTP_CLIENT_ERROR", attrs...)
			default:
				logger.Info("HTTP_OK", attrs...)
			}
			return nil
		},
	}))

	handler := handler.NewHandler(e, usecase, redisClient)
	MapRoutes(e, handler, jwt)

	return e
}
