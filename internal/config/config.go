package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for api + worker.
// Values come from the environment; defaults make local docker-compose work out of the box.
type Config struct {
	Env      string // "dev", "prod"
	HTTPAddr string // ":8080"
	LogLevel string // "debug", "info", "warn", "error"
	Postgres PostgresConfig
	Redis    RedisConfig
	Provider ProviderConfig
	JWT      JWTConfig
	Worker   WorkerConfig
	OTel     OTelConfig
	WS       WSConfig
}

type PostgresConfig struct {
	DSN         string
	MaxConns    int32
	MinConns    int32
	HealthCheck time.Duration
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type ProviderConfig struct {
	WebhookURL string
	Timeout    time.Duration
}

type JWTConfig struct {
	Secret string
	Issuer string
	TTL    time.Duration
}

type WorkerConfig struct {
	Concurrency     int
	ShutdownTimeout time.Duration
	MaxRetry        int
}

type OTelConfig struct {
	Enabled     bool
	ServiceName string
}

// WSConfig holds WebSocket hardening knobs. OriginAllowList is a comma-separated
// list of host patterns (e.g. "example.com,*.example.com"). Empty in dev is OK
// and falls back to InsecureSkipVerify.
type WSConfig struct {
	OriginAllowList []string
}

func Load() (Config, error) {
	cfg := Config{
		Env:      getenv("APP_ENV", "dev"),
		HTTPAddr: getenv("HTTP_ADDR", ":8080"),
		LogLevel: getenv("LOG_LEVEL", "info"),
		Postgres: PostgresConfig{
			DSN:         getenv("POSTGRES_DSN", "postgres://notifd:notifd@postgres:5432/notifd?sslmode=disable"),
			MaxConns:    int32(getenvInt("POSTGRES_MAX_CONNS", 25)),
			MinConns:    int32(getenvInt("POSTGRES_MIN_CONNS", 2)),
			HealthCheck: getenvDuration("POSTGRES_HEALTH_CHECK", 30*time.Second),
		},
		Redis: RedisConfig{
			Addr:     getenv("REDIS_ADDR", "redis:6379"),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       getenvInt("REDIS_DB", 0),
		},
		Provider: ProviderConfig{
			WebhookURL: getenv("WEBHOOK_URL", "http://mockserver:1080/webhook"),
			Timeout:    getenvDuration("WEBHOOK_TIMEOUT", 5*time.Second),
		},
		JWT: JWTConfig{
			Secret: getenv("JWT_SECRET", "dev-secret-do-not-use-in-prod"),
			Issuer: getenv("JWT_ISSUER", "notifd"),
			TTL:    getenvDuration("JWT_TTL", 24*time.Hour),
		},
		Worker: WorkerConfig{
			Concurrency:     getenvInt("WORKER_CONCURRENCY", 10),
			ShutdownTimeout: getenvDuration("WORKER_SHUTDOWN_TIMEOUT", 30*time.Second),
			MaxRetry:        getenvInt("WORKER_MAX_RETRY", 8),
		},
		OTel: OTelConfig{
			Enabled:     getenvBool("OTEL_ENABLED", true),
			ServiceName: getenv("OTEL_SERVICE_NAME", "notifd"),
		},
		WS: WSConfig{
			OriginAllowList: splitCSV(os.Getenv("WS_ORIGIN_ALLOWLIST")),
		},
	}

	if cfg.JWT.Secret == "" {
		return Config{}, fmt.Errorf("JWT_SECRET required")
	}
	// Fail closed when a production deploy forgot to rotate the default secret.
	// Dev keeps the convenience of a hard-coded fallback so local `docker compose up`
	// works with no setup.
	if cfg.Env != "dev" && cfg.JWT.Secret == "dev-secret-do-not-use-in-prod" {
		return Config{}, fmt.Errorf("JWT_SECRET must be overridden when APP_ENV != dev")
	}
	return cfg, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func getenvDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
