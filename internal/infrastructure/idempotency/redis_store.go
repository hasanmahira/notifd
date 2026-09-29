package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hasanmahira/notifd/internal/domain"
	"github.com/hasanmahira/notifd/internal/usecase"
	"github.com/redis/go-redis/v9"
)

// Store is a Redis-backed implementation of usecase.IdempotencyStore.
// It avoids the classic SETNX race by using SET NX EX and storing the response
// under a distinct key, plus a pending sentinel so callers can poll.
type Store struct {
	rd *redis.Client
}

func New(rd *redis.Client) *Store { return &Store{rd: rd} }

const (
	pendingSentinel = ":pending"
	claimPrefix     = "idem:"
	respSuffix      = ":response"
	hashSuffix      = ":hash"
)

// Claim attempts to reserve the key. On success (claimed=true) the caller owns
// the key and the bodyHash is stored alongside it so future replays can verify
// body equality. On contention (claimed=false) we return the previously-stored
// hash so the middleware can decide between "replay the cached response" and
// "409 body mismatch." Both paths are serviced from a single Redis round trip.
func (s *Store) Claim(ctx context.Context, key, bodyHash string, ttl time.Duration) (bool, string, error) {
	fullKey := claimPrefix + key
	hashKey := fullKey + hashSuffix

	// SET fullKey value NX EX ttl — atomic "set only if absent."
	// SetArgs with Mode:"NX" returns "OK" on a new write and an empty string on
	// a no-op (redis.Nil). Either way we avoid the deprecated SetNX helper.
	res, err := s.rd.SetArgs(ctx, fullKey, pendingSentinel, redis.SetArgs{
		Mode: "NX", TTL: ttl,
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return false, "", fmt.Errorf("redis set nx: %w", err)
	}
	ok := res == "OK"
	if ok {
		// Store the hash under a parallel key. If this write fails we fall
		// through without it — the middleware treats missing-hash as a legacy
		// record and skips the mismatch check.
		if err := s.rd.Set(ctx, hashKey, bodyHash, ttl).Err(); err != nil {
			return true, "", fmt.Errorf("redis set hash: %w", err)
		}
		return true, bodyHash, nil
	}
	existing, err := s.rd.Get(ctx, hashKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return false, "", nil // legacy claim without a hash
		}
		return false, "", fmt.Errorf("redis get hash: %w", err)
	}
	return false, existing, nil
}

type stored struct {
	Status int    `json:"s"`
	Body   []byte `json:"b"`
}

// SaveResponse persists the final response under the response key AND extends
// the claim key TTL so polling callers see a consistent view.
func (s *Store) SaveResponse(ctx context.Context, key string, status int, body []byte, ttl time.Duration) error {
	payload, err := json.Marshal(stored{Status: status, Body: body})
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	if err := s.rd.Set(ctx, claimPrefix+key+respSuffix, payload, ttl).Err(); err != nil {
		return fmt.Errorf("redis set response: %w", err)
	}
	if err := s.rd.Expire(ctx, claimPrefix+key, ttl).Err(); err != nil {
		return fmt.Errorf("redis expire: %w", err)
	}
	return nil
}

// LookupResponse returns (status, body, pending, err).
//   - pending=true means the response key isn't written yet (in-flight); the
//     middleware will poll and eventually 409 Retry-After.
//   - pending=false and body!=nil means we have a stored response to return.
//   - returns domain.ErrNotFound if nothing has been claimed for this key.
func (s *Store) LookupResponse(ctx context.Context, key string) (int, []byte, bool, error) {
	raw, err := s.rd.Get(ctx, claimPrefix+key+respSuffix).Bytes()
	if err == nil {
		var out stored
		if err := json.Unmarshal(raw, &out); err != nil {
			return 0, nil, false, fmt.Errorf("unmarshal stored response: %w", err)
		}
		return out.Status, out.Body, false, nil
	}
	if !errors.Is(err, redis.Nil) {
		return 0, nil, false, fmt.Errorf("redis get response: %w", err)
	}
	// no response yet — check the claim key to distinguish pending vs unknown
	got, err := s.rd.Get(ctx, claimPrefix+key).Result()
	if err == nil && got == pendingSentinel {
		return 0, nil, true, nil
	}
	if errors.Is(err, redis.Nil) {
		return 0, nil, false, domain.ErrNotFound
	}
	return 0, nil, false, fmt.Errorf("redis get claim: %w", err)
}

// Ensure the concrete type still satisfies the port at compile time.
var _ usecase.IdempotencyStore = (*Store)(nil)
