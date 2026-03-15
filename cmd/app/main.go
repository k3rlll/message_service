package main

import (
	"context"
	"fmt"
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
	logger.Info("Starting message service...")
	//

	//

	//
	client, err := mongo.ConnectMongoDB(ctx, cfg)
	if err != nil {
		logger.Error("Failed to connect to MongoDB:", err)
	}

	// Set read preference to primary for strong consistency
	opts := options.Database().SetReadPreference(readpref.Primary())
	db := client.Database(cfg.Mongo.DatabaseName, opts)

	// Ensure indexes are created for the messages collection
	messageRepo := repo.NewMessageRepository(db)
	if err := messageRepo.EnsureIndexes(ctx); err != nil {
		logger.Error("Failed to ensure indexes for messages collection:", err)
	}
	defer func() {
		if err := client.Disconnect(ctx); err != nil {
			logger.Error("Error disconnecting from MongoDB:", err)
		}
	}()
	//

	usecase := uc.NewUsecase(messageRepo)

	//

	//
	go func() {
		e := app.Run(*cfg, logger, usecase)
		if err := e.Start(":" + fmt.Sprintf("%d", cfg.Server.Port)); err != nil {

		}
	}()

	select {}

}
