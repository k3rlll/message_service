package configs

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

type Config struct {
	Environment string       `env:"ENVIRONMENT" env-default:"development"`
	Mongo       Mongo        `yaml:"mongo"`
	Server      ServerConfig `yaml:"server"`
	JWTSecret   string       `env:"JWT_SECRET" env-required:"true"`
	JWTTTL      int          `env:"JWT_TTL" env-default:"60"`
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" env-default:"0.0.0.0"`
	Port string `env:"SERVER_PORT" env-default:"8080"`
}

type Mongo struct {
	DatabaseName          string        `env:"MONGO_DATABASE" env-required:"true"`
	InitializationTimeout time.Duration `yaml:"initialization_timeout" env-default:"30s"`
	URI                   string        `env:"MONGO_URI" env-required:"true"`
	ConnectTimeout        time.Duration `yaml:"connect_timeout" env-default:"10s"`
	AuthMechanism         string        `env:"MONGO_AUTH_MECHANISM" env-required:"true"`
	Username              string        `env:"MONGO_USERNAME" env-required:"true"`
	Password              string        `env:"MONGO_PASSWORD" env-required:"true"`
}

var (
	ConfigInstance *Config
	Once           sync.Once
)

func LoadConfig() (*Config, error) {
	Once.Do(func() {
		if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
			log.Fatalf("Failed to load .env file: %v", err)
		}

		var cfg Config

		configPath := os.Getenv("CONFIG_PATH")
		if configPath != "" {
			if err := cleanenv.ReadConfig(configPath, &cfg); err != nil {
				log.Fatalf("Failed to read config file at path %s: %v", configPath, err)
			}
		} else {
			if err := cleanenv.ReadEnv(&cfg); err != nil {
				log.Fatalf("Failed to read environment variables: %v", err)
			}
		}

		ConfigInstance = &cfg
	})
	return ConfigInstance, nil
}
