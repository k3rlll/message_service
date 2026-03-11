package mongo

import (
	"context"
	"fmt"
	"main/internal/configs"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func Connect(cfg *configs.Config) (*mongo.Client, error) {
	// Set up MongoDB client options with authentication
	credentials := options.Credential{
		AuthMechanism: cfg.Mongo.AuthMechanism,
		Username:      cfg.Mongo.Username,
		Password:      cfg.Mongo.Password,
	}
	//

	//
	//options for MongoDB client connection, including authentication and connection timeout
	//could be extended with additional options like TLS settings, replica set configuration, etc.
	opt := options.ClientOptions{
		Auth:           &credentials,
		ConnectTimeout: &cfg.Mongo.ConnectTimeout,
	}
	//
	//

	//
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Mongo.ConnectTimeout)
	defer cancel()
	client, err := mongo.Connect(ctx, opt.ApplyURI(cfg.Mongo.URI))
	if err != nil {
		fmt.Println("Error connecting to MongoDB:", err)
		return nil, err
	}
	return client, nil
}
