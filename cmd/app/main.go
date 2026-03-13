package main

import (
	"context"
	"main/internal/configs"
	"main/internal/storage/mongo"
	"main/internal/storage/mongo/repository"
	"main/pkg/logger"
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
	log := logger.SetupLogger(cfg.Environment)
	log.Info("Starting subscription service...")
	//

	//

	//
	client, err := mongo.ConnectMongoDB(ctx, cfg)
	if err != nil {
		log.Error("Failed to connect to MongoDB:", err)
	}
	defer func() {
		if err := client.Disconnect(ctx); err != nil {
			log.Error("Error disconnecting from MongoDB:", err)
		}
	}()
	//

	//

	//

	repo := repository.NewRepository(c)

}
