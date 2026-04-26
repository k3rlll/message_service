package app

import (
	"errors"
	"log/slog"

	config "main/internal/configs"
	handler "main/internal/transport/http"

	cache "main/internal/cache"

	uc "main/internal/usecase"
	errHandler "main/pkg/error_handler"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/redis/go-redis/v9"
)

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

	//инициализация in memory cache
	memoryCache := cache.NewCache[int, int](cfg.InMemoryCache, logger)

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

	handler := handler.NewHandler(e, logger, usecase, redisClient, cfg)
	MapRoutes(e, handler, jwt)

	return e
}
