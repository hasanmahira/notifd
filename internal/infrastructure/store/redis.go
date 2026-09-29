package store

import (
	"context"
	"fmt"
	"time"

	"github.com/hasanmahira/notifd/internal/config"
	"github.com/redis/go-redis/v9"
)

// NewRedis builds a go-redis client used by the idempotency middleware,
// rate limiter, pubsub bridge, and asynq internals.
func NewRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	rd := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     20,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rd.Ping(pingCtx).Err(); err != nil {
		_ = rd.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return rd, nil
}
