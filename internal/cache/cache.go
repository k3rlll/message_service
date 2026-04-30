package cache

import (
	config "main/internal/configs"

	"log/slog"
	"time"

	"github.com/maypok86/otter/v2"
)

func NewCache[k comparable, v any](
	cfg config.InMemoryCacheConfig,
	logger *slog.Logger) *otter.Cache[k, v] {

	otterLogger := NewSlogAdapter(logger)

	cacheOptions := &otter.Options[k, v]{
		MaximumSize: cfg.MaximumSize,
		ExpiryCalculator: otter.ExpiryAccessing[k, v](
			time.Minute * time.Duration(cfg.ExpiryMinutes),
		),
		InitialCapacity: cfg.InitialCapacity,
		Logger:          otterLogger,
	}

	client := otter.Must(cacheOptions)

	return client
}
