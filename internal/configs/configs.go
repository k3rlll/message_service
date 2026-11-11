package configs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"main/internal/ratelimit"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

const defaultConfigPath = "config/config.yaml"

type Config struct {
	Environment   string              `yaml:"env" env:"ENVIRONMENT" env-default:"development"`
	Server        ServerConfig        `yaml:"server"`
	Mongo         MongoConfig         `yaml:"mongo"`
	Redis         RedisConfig         `yaml:"redis"`
	JWT           JWTConfig           `yaml:"jwt"`
	Websocket     WebsocketConfig     `yaml:"websocket"`
	InMemoryCache InMemoryCacheConfig `yaml:"in_memory_cache"`
	Events        EventsConfig        `yaml:"events"`
	Presence      PresenceConfig      `yaml:"presence"`
	RateLimit     RateLimitConfig     `yaml:"rate_limit"`
	S3            S3Config            `yaml:"s3"`
}

type ServerConfig struct {
	Host string `env:"SERVER_HOST" env-default:"0.0.0.0"`
	Port string `env:"SERVER_PORT" env-default:"8080"`
	// Служебный порт: /metrics, /healthz, /readyz. Наружу через балансировщик не публикуется.
	AdminPort       string        `env:"ADMIN_PORT" env-default:"8081"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" env-default:"10s"`
}

type MongoConfig struct {
	URI          string `env:"MONGO_URI" env-required:"true"`
	DatabaseName string `env:"MONGO_DATABASE" env-required:"true"`
	// Username/Password нужны только если учетные данные не зашиты в URI
	Username              string        `env:"MONGO_USERNAME"`
	Password              string        `env:"MONGO_PASSWORD"`
	AuthMechanism         string        `env:"MONGO_AUTH_MECHANISM"`
	InitializationTimeout time.Duration `yaml:"initialization_timeout" env-default:"30s"`
	ConnectTimeout        time.Duration `yaml:"connect_timeout" env-default:"10s"`
}

type RedisConfig struct {
	Addr       string `env:"REDIS_ADDR" env-default:"localhost:6379"`
	ClientName string `env:"REDIS_CLIENT_NAME" env-default:"message_service"`
	Password   string `env:"REDIS_PASSWORD"`
}

// Токены выпускает отдельный сервис авторизации и подписывает их RS256.
// Этому сервису нужен только публичный ключ для проверки подписи.
type JWTConfig struct {
	PublicKeyPath string `env:"JWT_PUBLIC_KEY_PATH" env-default:"keys/jwt_public.pem"`
}

type WebsocketConfig struct {
	// Сообщения меньше порога (в байтах) не сжимаются
	CompressionThreshold int `yaml:"compression_threshold" env-default:"512"`
	// Отключает проверку Origin. В проде должно быть false!
	InsecureSkipVerify bool `yaml:"insecure_skip_verify" env-default:"false"`
	// Размер буфера канала отправки сообщений клиенту
	ClientChanSize int `yaml:"client_chan_size" env-default:"256"`
	// Максимальный размер входящего фрейма в байтах
	MaxMessageSize int64 `yaml:"max_message_size" env-default:"16384"`
}

type InMemoryCacheConfig struct {
	MaximumSize     int           `yaml:"max_size" env-default:"1000"`
	TTL             time.Duration `yaml:"ttl" env-default:"5m"`
	InitialCapacity int           `yaml:"initial_capacity" env-default:"100"`
}

type EventsConfig struct {
	// Примерный размер потока событий в Redis. Более старые события обрезаются.
	StreamMaxLen int64 `yaml:"stream_max_len" env-default:"10000"`
	// Сколько событий максимум догружается клиенту после переподключения.
	// Если клиент отстал сильнее, он перезагружает состояние через REST.
	ReplayLimit int64 `yaml:"replay_limit" env-default:"500"`
}

type PresenceConfig struct {
	// Сколько соединение считается живым без heartbeat.
	// Должно быть больше интервала PING (30 секунд).
	TTL time.Duration `yaml:"ttl" env-default:"75s"`
	// Сколько живет отметка "печатает" без повторного сигнала от клиента
	TypingTTL time.Duration `yaml:"typing_ttl" env-default:"3s"`
}

// Лимиты считаются на пользователя. limit: 0 отключает ограничение.
type RateLimitConfig struct {
	// Все REST-запросы
	HTTP ratelimit.Rule `yaml:"http"`
	// Отправка сообщений, по REST и по WebSocket суммарно
	Messages ratelimit.Rule `yaml:"messages"`
}

// S3-совместимое хранилище вложений. Если S3_ENDPOINT не задан, вложения отключены.
type S3Config struct {
	// Адрес хранилища для самого сервиса: URL или host:port
	Endpoint string `env:"S3_ENDPOINT"`
	// Адрес хранилища для клиентов, если он отличается от внутреннего
	PublicEndpoint string `env:"S3_PUBLIC_ENDPOINT"`
	AccessKey      string `env:"S3_ACCESS_KEY"`
	SecretKey      string `env:"S3_SECRET_KEY"`
	Bucket         string `env:"S3_BUCKET" env-default:"attachments"`
	Region         string `env:"S3_REGION" env-default:"us-east-1"`

	UploadURLTTL   time.Duration `yaml:"upload_url_ttl" env-default:"15m"`
	DownloadURLTTL time.Duration `yaml:"download_url_ttl" env-default:"1h"`
	// Максимальный размер одного файла в байтах
	MaxFileSize int64 `yaml:"max_file_size" env-default:"52428800"`
}

func (c S3Config) Enabled() bool {
	return c.Endpoint != ""
}

// LoadConfig читает .env (если есть), затем YAML-конфиг и переменные окружения.
// Переменные окружения имеют приоритет над YAML.
// Путь к YAML берется из CONFIG_PATH, по умолчанию config/config.yaml.
func LoadConfig() (*Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("load .env file: %w", err)
	}

	configPath := os.Getenv("CONFIG_PATH")
	explicitPath := configPath != ""
	if !explicitPath {
		configPath = defaultConfigPath
	}

	var cfg Config

	_, statErr := os.Stat(configPath)
	switch {
	case statErr == nil:
		if err := cleanenv.ReadConfig(configPath, &cfg); err != nil {
			return nil, fmt.Errorf("read config %s: %w", configPath, err)
		}
	case explicitPath:
		// Путь задан явно, но файла нет - это ошибка, а не повод молча взять дефолты
		return nil, fmt.Errorf("config file %s: %w", configPath, statErr)
	default:
		if err := cleanenv.ReadEnv(&cfg); err != nil {
			return nil, fmt.Errorf("read environment: %w", err)
		}
	}

	return &cfg, nil
}
