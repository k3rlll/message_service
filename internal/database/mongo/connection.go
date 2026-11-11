package mongo

import (
	"context"
	"fmt"
	"time"

	"main/internal/configs"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func ConnectMongoDB(ctx context.Context, cfg configs.MongoConfig) (*mongo.Client, error) {
	clientOptions := options.Client().ApplyURI(cfg.URI).
		SetConnectTimeout(cfg.ConnectTimeout).
		SetServerSelectionTimeout(5 * time.Second).
		SetMaxPoolSize(50).
		SetMinPoolSize(10).
		SetMaxConnIdleTime(5 * time.Minute)

	// Учетные данные из URI имеют приоритет.
	// Отдельные переменные используем только если в URI их нет.
	if clientOptions.Auth == nil && cfg.Username != "" {
		clientOptions.SetAuth(options.Credential{
			AuthMechanism: cfg.AuthMechanism,
			Username:      cfg.Username,
			Password:      cfg.Password,
		})
	}

	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("connect to MongoDB: %w", err)
	}

	// Connect не гарантирует, что соединение реально установлено,
	// поэтому проверяем его пингом
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("ping MongoDB: %w", err)
	}

	return client, nil
}
