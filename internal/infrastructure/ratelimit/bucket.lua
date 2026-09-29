-- Atomic token-bucket over Redis.
--
-- KEYS[1] = bucket key (e.g. "rl:sms")
-- ARGV[1] = capacity        (e.g. 100)
-- ARGV[2] = refill_per_sec  (e.g. 100)
-- ARGV[3] = cost            (usually 1)
--
-- Returns: { allowed (0|1), retry_after_ms }
--
-- Time comes from Redis's TIME command server-side, so workers with skewed
-- clocks cannot refill the bucket by passing a fast timestamp. This is the
-- standard practice for distributed rate limiters.
--
-- State is stored in a single HSET (tokens, last_ms). HMSET is avoided
-- because it's deprecated since Redis 4.0 and removed in 7.4+.

local capacity       = tonumber(ARGV[1])
local refill_per_sec = tonumber(ARGV[2])
local cost           = tonumber(ARGV[3])

local redis_time     = redis.call('TIME')
local now_ms         = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

local state   = redis.call('HMGET', KEYS[1], 'tokens', 'last_ms')
local tokens  = tonumber(state[1])
local last_ms = tonumber(state[2])

if tokens == nil then
  tokens  = capacity
  last_ms = now_ms
end

local elapsed_ms = math.max(0, now_ms - last_ms)
local refill     = (elapsed_ms * refill_per_sec) / 1000.0
tokens = math.min(capacity, tokens + refill)

local allowed     = 0
local retry_after = 0

if tokens >= cost then
  tokens  = tokens - cost
  allowed = 1
else
  local deficit = cost - tokens
  retry_after   = math.ceil((deficit * 1000.0) / refill_per_sec)
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'last_ms', now_ms)
redis.call('EXPIRE', KEYS[1], 60)

return { allowed, retry_after }
