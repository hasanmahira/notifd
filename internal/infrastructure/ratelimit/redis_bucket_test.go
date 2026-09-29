package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/redis/go-redis/v9"
)

func newMini(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rd := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rd.Close() })
	return rd, mr
}

func TestBucket_AllowsUnderCapacity(t *testing.T) {
	rd, _ := newMini(t)
	b := New(rd, Config{Capacity: 5, RefillPerSec: 5})

	for i := 0; i < 5; i++ {
		r, err := b.Acquire(context.Background(), domain.ChannelSMS)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if !r.Allowed {
			t.Fatalf("acquire %d: want allowed, got denied", i)
		}
	}
}

func TestBucket_DeniesWhenExhausted(t *testing.T) {
	rd, _ := newMini(t)
	b := New(rd, Config{Capacity: 2, RefillPerSec: 1})

	for i := 0; i < 2; i++ {
		if r, _ := b.Acquire(context.Background(), domain.ChannelEmail); !r.Allowed {
			t.Fatalf("setup acquire %d should succeed", i)
		}
	}
	r, err := b.Acquire(context.Background(), domain.ChannelEmail)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if r.Allowed {
		t.Fatalf("expected denial after exhausting bucket")
	}
	if r.RetryAfter <= 0 {
		t.Fatalf("expected positive RetryAfter, got %s", r.RetryAfter)
	}
}

func TestBucket_RefillsOverTime(t *testing.T) {
	rd, _ := newMini(t)
	b := New(rd, Config{Capacity: 1, RefillPerSec: 50}) // 50 tok/s -> 1 per 20ms

	_, _ = b.Acquire(context.Background(), domain.ChannelPush)
	if r, _ := b.Acquire(context.Background(), domain.ChannelPush); r.Allowed {
		t.Fatalf("should be denied right after drain")
	}

	// Real wall-clock wait: Bucket passes time.Now().UnixMilli() as ARGV[1], so
	// miniredis FastForward wouldn't help — we need actual elapsed time.
	time.Sleep(60 * time.Millisecond)

	r, err := b.Acquire(context.Background(), domain.ChannelPush)
	if err != nil {
		t.Fatalf("acquire after wait: %v", err)
	}
	if !r.Allowed {
		t.Fatalf("expected refill to have restored tokens (retry_after=%s)", r.RetryAfter)
	}
}
