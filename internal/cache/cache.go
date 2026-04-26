package cache

import (
	config "main/internal/configs"

	"log/slog"
	"time"

	"github.com/maypok86/otter/v2"
)

type MemoryCache[k comparable, v any] struct {
	client *otter.Cache[k, v]
}

func NewCache[k comparable, v any](
	cfg config.InMemoryCacheConfig,
	logger *slog.Logger) *MemoryCache[k, v] {

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

	return &MemoryCache[k, v]{client: client}
}
