package redis

import (
	"main/internal/configs"

	"github.com/redis/go-redis/v9"
)

type RdbRepo struct {
	Client *redis.Client
}

func RedisNewClient(cfg configs.Config) RdbRepo {
	rdb := redis.NewClient(&redis.Options{
		Addr:       "localhost:6379",
		ClientName: cfg.Redis.ClientName,
		Password:   cfg.Redis.Password,

		DB: 0,
	})
	return RdbRepo{
		Client: rdb,
	}

}
