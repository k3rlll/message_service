package usecase

import (
	"context"
	"main/internal/models"
)

type Repo interface {
	SaveMessage(ctx context.Context, msg *models.Message) error
}
