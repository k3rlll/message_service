package main

import (
	"context"
	"main/internal/app"
	"main/internal/configs"
	"main/internal/storage/mongo"
	repo "main/internal/storage/mongo/repository"
	uc "main/internal/usecase"
	stplog "main/pkg/logger"

	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func main() {
	// Load configuration
	cfg, err := configs.LoadConfig()
	if err != nil {
		panic(err)
	}
	//
	//

	//Setup logger
	ctx := context.Background()
	logger := stplog.SetupLogger(cfg.Environment)
	//

	//

	//
	client, err := mongo.ConnectMongoDB(ctx, cfg)
	if err != nil {
		logger.Error("Failed to connect to MongoDB:", "error", err)
		return
	}

	// Set read preference to primary for strong consistency
	opts := options.Database().SetReadPreference(readpref.Primary())
	db := client.Database(cfg.Mongo.DatabaseName, opts)

	// Ensure indexes are created for the messages collection
	messageRepo := repo.NewMessageRepository(db)
	if err := messageRepo.EnsureIndexes(ctx); err != nil {
		logger.Error("Failed to ensure indexes for messages collection:", "error", err)
		return
	}
	defer func() {
		if err := client.Disconnect(ctx); err != nil {
			logger.Error("Error disconnecting from MongoDB:", "error", err)
			return
		}
	}()
	//

	usecase := uc.NewUsecase(messageRepo)
	logger.Info("Starting message service on " + cfg.Server.Host + ":" + cfg.Server.Port)
	//

	// Start the server in a separate goroutine
	go func() {
		e := app.Run(*cfg, logger, usecase)
		if err := e.Start(":" + cfg.Server.Port); err != nil {
			logger.Error("Error starting server:", "error", err)
			return
		}

	}()

	select {}

}
