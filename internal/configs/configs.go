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
	Environment   string              `env:"ENVIRONMENT" env-default:"development"`
	Mongo         MongoConfig         `yaml:"mongo"`
	Websocket     WebsocketConfig     `yaml:"websocket"`
	Redis         RedisConfig         `yaml:"redis"`
	Server        ServerConfig        `yaml:"server"`
	InMemoryCache InMemoryCacheConfig `yaml:"in_memory_cache"`
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" env-default:"0.0.0.0"`
	Port string `env:"SERVER_PORT" env-default:"8080"`
}

type MongoConfig struct {
	DatabaseName          string        `env:"MONGO_DATABASE" env-required:"true"`
	InitializationTimeout time.Duration `yaml:"initialization_timeout" env-default:"30s"`
	URI                   string        `env:"MONGO_URI" env-required:"true"`
	ConnectTimeout        time.Duration `yaml:"connect_timeout" env-default:"10s"`
	AuthMechanism         string        `env:"MONGO_AUTH_MECHANISM" env-required:"true"`
	Username              string        `env:"MONGO_USERNAME" env-required:"true"`
	Password              string        `env:"MONGO_PASSWORD" env-required:"true"`
}

type WebsocketConfig struct {
	CompressionThreshold int  `yaml:"compression_threshold" env-default:"512"`  // В байтах, например 512 для отключения компрессии для сообщений меньше 512 байт
	InsecureSkipVerify   bool `yaml:"insecure_skip_verify" env-default:"false"` // В проде замените на проверку Origin!
	ClientChanSize       int  `yaml:"client_chan_size" env-default:"256"`       // Размер буфера канала для отправки сообщений клиенту, например 256
}

type RedisConfig struct {
	Addr       string `env:"REDIS_ADDR" env-default:"localhost:6379"`
	ClientName string `env:"REDIS_CLIENT_NAME" env-default:"message_service"`
	Password   string `env:"REDIS_PASSWORD" env-default:""`
}

type JWTConfig struct {
	Secret string `env:"JWT_SECRET" env-required:"true"`
	TTL    int    `yaml:"ttl" env-default:"60"`
}

type InMemoryCacheConfig struct {
	MaximumSize     int           `yaml:"maximum_size" env-default:"1000"` // Максимальное количество элементов в кэше
	ExpiryMinutes   time.Duration `yaml:"expiry_minutes" env-default:"5m"`
	InitialCapacity int           `yaml:"initial_capacity" env-default:"100"` // Начальная емкость кэша, может помочь с производительностью при ожидаемом количестве элементов
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
