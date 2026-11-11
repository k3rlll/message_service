package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"main/internal/app"
	events "main/internal/broker/redis_events"
	"main/internal/cache"
	"main/internal/configs"
	"main/internal/database/mongo"
	chatRepo "main/internal/database/mongo/chat_repository"
	msgRepo "main/internal/database/mongo/message_repository"
	"main/internal/presence"
	"main/internal/ratelimit"
	"main/internal/storage/s3"
	chatHandlers "main/internal/transport/http/chat_handlers"
	msgHandlers "main/internal/transport/http/message_handlers"
	wsHandler "main/internal/transport/http/ws_handler"
	attachUC "main/internal/usecase/attachment_usecase"
	cacheUC "main/internal/usecase/cache_usecase"
	chatUC "main/internal/usecase/chat_usecase"
	msgUC "main/internal/usecase/message_usecase"
	presenceUC "main/internal/usecase/presence_usecase"
	"main/internal/ws"
	"main/pkg/jwt"
	stplog "main/pkg/logger"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Service stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := configs.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := stplog.SetupLogger(cfg.Environment)

	// Публичный ключ сервиса авторизации. Для локальной разработки
	// пару ключей и токен выдает go run ./cmd/devtoken
	tokenVerifier, err := jwt.NewVerifierFromFile(cfg.JWT.PublicKeyPath)
	if err != nil {
		return err
	}

	// Контекст отменяется по Ctrl+C или SIGTERM - это сигнал к graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	initCtx, cancelInit := context.WithTimeout(ctx, cfg.Mongo.InitializationTimeout)
	defer cancelInit()

	// MongoDB
	mongoClient, err := mongo.ConnectMongoDB(initCtx, cfg.Mongo)
	if err != nil {
		return err
	}
	defer func() {
		// ctx к этому моменту уже отменен, поэтому берем свежий контекст
		if err := mongoClient.Disconnect(context.Background()); err != nil {
			logger.Error("Error disconnecting from MongoDB", "error", err)
		}
	}()

	// Read preference primary - для строгой согласованности чтения после записи
	db := mongoClient.Database(
		cfg.Mongo.DatabaseName,
		options.Database().SetReadPreference(readpref.Primary()))

	messageRepo := msgRepo.NewMessageRepository(db, logger)
	if err := messageRepo.EnsureIndexes(initCtx); err != nil {
		return err
	}
	chatRepository := chatRepo.NewChatRepository(db, logger)
	if err := chatRepository.EnsureIndexes(initCtx); err != nil {
		return err
	}

	// Redis
	redisClient := redis.NewClient(&redis.Options{
		Addr:       cfg.Redis.Addr,
		ClientName: cfg.Redis.ClientName,
		Password:   cfg.Redis.Password,
	})
	defer redisClient.Close()

	if err := redisClient.Ping(initCtx).Err(); err != nil {
		return fmt.Errorf("ping Redis: %w", err)
	}

	// Объектное хранилище вложений - необязательное
	var storage attachUC.Storage
	if cfg.S3.Enabled() {
		s3Storage, err := s3.New(initCtx, cfg.S3)
		if err != nil {
			return err
		}
		storage = s3Storage
	} else {
		logger.Warn("S3_ENDPOINT is not set, attachments are disabled")
	}

	// Зависимости
	membersCache := cacheUC.NewInMemoryCache(
		cache.NewCache[string, cacheUC.ChatMembers](cfg.InMemoryCache, logger),
		logger,
		chatRepository,
	)
	eventBus := events.NewEventBus(redisClient, membersCache, logger, events.Config{
		StreamMaxLen: cfg.Events.StreamMaxLen,
		ReplayLimit:  cfg.Events.ReplayLimit,
	})
	limiter := ratelimit.NewLimiter(redisClient)

	attachmentUsecase := attachUC.NewUsecase(storage, membersCache, attachUC.Config{
		UploadURLTTL:   cfg.S3.UploadURLTTL,
		DownloadURLTTL: cfg.S3.DownloadURLTTL,
		MaxFileSize:    cfg.S3.MaxFileSize,
	})
	messageUsecase := msgUC.NewUsecase(
		messageRepo, chatRepository, membersCache, eventBus,
		attachmentUsecase, limiter, cfg.RateLimit.Messages, logger)
	chatUsecase := chatUC.NewUsecase(chatRepository, messageRepo, membersCache, eventBus, logger)
	presenceUsecase := presenceUC.NewUsecase(
		presence.NewStore(redisClient, cfg.Presence.TTL, cfg.Presence.TypingTTL),
		chatRepository, membersCache, eventBus, logger)

	hub := ws.NewHub(logger)

	// Фоновые воркеры: Хаб и чтение событий из Redis
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		hub.Run(ctx)
	}()
	go func() {
		defer workers.Done()
		eventBus.Run(ctx, hub)
	}()

	// Принимаем клиентов только когда шина готова, иначе первые события потеряются
	if err := eventBus.WaitReady(initCtx); err != nil {
		stop()
		workers.Wait()
		return fmt.Errorf("event bus is not ready: %w", err)
	}

	// HTTP сервер
	server := app.NewServer(logger,
		app.Handlers{
			Messages: msgHandlers.NewHandler(logger, messageUsecase),
			Chats:    chatHandlers.NewHandler(logger, chatUsecase, presenceUsecase, attachmentUsecase),
			WS: wsHandler.NewHandler(logger, hub,
				wsHandler.NewBackend(messageUsecase, chatUsecase, presenceUsecase, eventBus),
				cfg.Websocket),
		},
		app.RouteDeps{
			Verifier: tokenVerifier,
			Limiter:  limiter,
			HTTPRule: cfg.RateLimit.HTTP,
			Logger:   logger,
		},
	)

	serverErr := make(chan error, 1)
	go func() {
		addr := net.JoinHostPort(cfg.Server.Host, cfg.Server.Port)
		logger.Info("Starting message service", "addr", addr)

		if err := server.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		stop()
		workers.Wait()
		return fmt.Errorf("start server: %w", err)
	case <-ctx.Done():
		logger.Info("Shutdown signal received")
	}

	// Сначала ждем остановки Хаба: он закрывает WebSocket соединения.
	// Shutdown их не закрывает и ждал бы до таймаута.
	workers.Wait()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancelShutdown()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown server: %w", err)
	}

	logger.Info("Service stopped gracefully")
	return nil
}
