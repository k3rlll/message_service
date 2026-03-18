package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"main/internal/models"
)

func (r *RdbRepo) PublishMessage(ctx context.Context, chatID string, message models.Message) error {
	channel := fmt.Sprintf("chat:%s", chatID)
	msgBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	err = r.Client.Publish(ctx, channel, msgBytes).Err()
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}
	return nil
}
