package repository

import (
	"go.mongodb.org/mongo-driver/mongo"
)

type Repository struct {
	Client *mongo.Client
}

func NewRepository(client *mongo.Client) *Repository {
	return &Repository{
		Client: client,
	}
}

