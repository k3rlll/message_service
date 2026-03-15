package configs

import (
	"sync"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

type Config struct {
	Environment string       `env:"ENVIRONMENT" env-default:"development"`
	Mongo       Mongo        `yaml:"mongo"`
	Server      ServerConfig `yaml:"server"`
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" env-default:"`
	Port string `env:"SERVER_PORT" env-default:"8080"`
}

type Mongo struct {
	DatabaseName          string        `env:"MONGO_DB" env-required:"true"`
	InitializationTimeout time.Duration `env:"MONGO_INITIALIZATION_TIMEOUT" env-default:"30s"`
	URI                   string        `env:"MONGO_URI" env-required:"true"`
	ConnectTimeout        time.Duration `env:"MONGO_CONNECT_TIMEOUT" env-default:"10s"`
	AuthMechanism         string        `env:"MONGO_AUTH_MECHANISM" env-required:"true"`
	Username              string        `env:"MONGO_USERNAME" env-required:"true"`
	Password              string        `env:"MONGO_PASSWORD" env-required:"true"`
}

var (
	ConfigInstance *Config
	Once           sync.Once
)

// LoadConfig loads the configuration from a file or environment variables.
func LoadConfig() (*Config, error) {
	Once.Do(func() {
		_ = godotenv.Load()

		var cfg Config
		err := cleanenv.ReadConfig("config/config.yaml", &cfg)
		if err != nil {
			panic(err)
		}
		ConfigInstance = &cfg
	})
	return ConfigInstance, nil
}
