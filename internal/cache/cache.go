package cache

import (
	"log/slog"

	config "main/internal/configs"

	"github.com/maypok86/otter/v2"
)

func NewCache[K comparable, V any](
	cfg config.InMemoryCacheConfig,
	logger *slog.Logger) *otter.Cache[K, V] {

	return otter.Must(&otter.Options[K, V]{
		MaximumSize:     cfg.MaximumSize,
		InitialCapacity: cfg.InitialCapacity,
		// TTL отсчитывается от записи, а не от чтения:
		// иначе часто читаемая запись никогда бы не обновилась из БД
		ExpiryCalculator: otter.ExpiryWriting[K, V](cfg.TTL),
		Logger:           NewSlogAdapter(logger),
	})
}
