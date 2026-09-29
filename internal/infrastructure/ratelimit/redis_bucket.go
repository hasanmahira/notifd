package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/usecase"
	"github.com/redis/go-redis/v9"
)

//go:embed bucket.lua
var bucketScript string

// Bucket is a distributed token bucket implemented as a single atomic Lua EVAL.
// One bucket per channel; the key is "rl:<channel>" which the worker computes
// from the message's channel field.
type Bucket struct {
	rd           *redis.Client
	script       *redis.Script
	capacity     int
	refillPerSec int
}

// Config tunes capacity and refill rate. The design goal is 100/s/channel so
// defaults match that.
type Config struct {
	Capacity     int
	RefillPerSec int
}

func New(rd *redis.Client, cfg Config) *Bucket {
	if cfg.Capacity <= 0 {
		cfg.Capacity = 100
	}
	if cfg.RefillPerSec <= 0 {
		cfg.RefillPerSec = 100
	}
	return &Bucket{
		rd:           rd,
		script:       redis.NewScript(bucketScript),
		capacity:     cfg.Capacity,
		refillPerSec: cfg.RefillPerSec,
	}
}

// Acquire requests one token. On allow returns {Allowed:true}. On denial returns
// {Allowed:false, RetryAfter: <ms until next token>}.
// Errors from Redis are bubbled up; the caller decides how to degrade.
func (b *Bucket) Acquire(ctx context.Context, ch domain.Channel) (usecase.RateLimitResult, error) {
	key := "rl:" + string(ch)
	res, err := b.script.Run(ctx, b.rd, []string{key},
		b.capacity,
		b.refillPerSec,
		1, // cost
	).Result()
	if err != nil {
		return usecase.RateLimitResult{}, fmt.Errorf("ratelimit eval: %w", err)
	}
	arr, ok := res.([]any)
	if !ok || len(arr) != 2 {
		return usecase.RateLimitResult{}, fmt.Errorf("ratelimit unexpected response: %#v", res)
	}
	allowed, _ := arr[0].(int64)
	retry, _ := arr[1].(int64)
	return usecase.RateLimitResult{
		Allowed:    allowed == 1,
		RetryAfter: time.Duration(retry) * time.Millisecond,
	}, nil
}

// compile-time port check
var _ usecase.RateLimiter = (*Bucket)(nil)
