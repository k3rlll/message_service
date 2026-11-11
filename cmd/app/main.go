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
	pubsub "main/internal/broker/redis_pub_sub"
	"main/internal/cache"
	"main/internal/configs"
	"main/internal/database/mongo"
	chatRepo "main/internal/database/mongo/chat_repository"
	msgRepo "main/internal/database/mongo/message_repository"
	msgHandlers "main/internal/transport/http/message_handlers"
	cacheUC "main/internal/usecase/cache_usecase"
	msgUC "main/internal/usecase/message_usecase"
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

	// MongoDB
	initCtx, cancelInit := context.WithTimeout(ctx, cfg.Mongo.InitializationTimeout)
	defer cancelInit()

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

	// Зависимости
	membersCache := cacheUC.NewInMemoryCache(
		cache.NewCache[string, cacheUC.ChatMembers](cfg.InMemoryCache, logger),
		logger,
		chatRepository,
	)
	broker := pubsub.NewPubSub(redisClient, logger)
	messageUsecase := msgUC.NewUsecase(messageRepo, membersCache, broker, logger)
	hub := ws.NewHub(logger)

	// Фоновые воркеры: Хаб и подписчик Redis
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		hub.Run(ctx)
	}()
	go func() {
		defer workers.Done()
		broker.RunSubscriber(ctx, membersCache, hub)
	}()

	// HTTP сервер
	handler := msgHandlers.NewHandler(logger, messageUsecase, hub, cfg.Websocket)
	server := app.NewServer(logger, handler, tokenVerifier)

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
