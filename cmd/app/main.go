package main

import (
	"context"
	"main/internal/configs"
	"main/internal/storage/mongo"
	"main/internal/storage/mongo/repository"
	"main/pkg/logger"
)

func main() {
	cfg, err := configs.LoadConfig()
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	log := logger.SetupLogger(cfg.Environment)
	log.Info("Starting subscription service...")
	client, err := mongo.Connect(cfg)
	if err != nil {
		log.Fatal("Failed to connect to MongoDB:", err)
	}
	defer func() {
		if err := client.Disconnect(ctx); err != nil {
			log.Error("Error disconnecting from MongoDB:", err)
		}
	}()
	repo := repository.NewRepository(client)

}
