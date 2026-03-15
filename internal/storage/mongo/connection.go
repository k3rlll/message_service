package mongo

import (
	"context"
	"fmt"
	"main/internal/configs"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func ConnectMongoDB(ctx context.Context, cfg *configs.Config) (*mongo.Client, error) {

	//

	//
	//
	// Set up MongoDB client options
	clientOptions := options.Client().ApplyURI(cfg.Mongo.URI).
		SetConnectTimeout(cfg.Mongo.ConnectTimeout).
		SetServerSelectionTimeout(5 * time.Second).
		SetMaxPoolSize(50).
		SetMinPoolSize(10).
		SetMaxConnIdleTime(5 * time.Minute)
	//

	//
	clientOptions = &options.ClientOptions{
		// Set up MongoDB client options with authentication
		Auth: &options.Credential{
			AuthMechanism: cfg.Mongo.AuthMechanism,
			Username:      cfg.Mongo.Username,
			Password:      cfg.Mongo.Password,
		},
	}

	//

	//

	//
	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("Error connecting to MongoDB: %w", err)
	}
	//since connect does not gurantee that the connection is established,
	// we need to ping the database to check if the physical connection is successful
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("Error pinging MongoDB: %w", err)
	}

	return client, nil
}


