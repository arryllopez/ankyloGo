package ankylogo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lua script for sliding window rate limiting using a sorted set.
var slidingWindowScript = `
-- KEYS[1] = the Redis key (e.g. "sliding:192.168.1.1")
-- ARGV[1] = now (current unix timestamp in nanoseconds, used as score)
-- ARGV[2] = cutoff (now - window in nanoseconds, anything older gets removed)
-- ARGV[3] = limit (max requests allowed in the window)
-- ARGV[4] = window (TTL in seconds so the key doesn't live forever)
-- ARGV[5] = unique member ID (prevents collisions when timestamps are identical)
local key = KEYS[1]
local now = tonumber(ARGV[1])
local cutoff = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local window = tonumber(ARGV[4])
local member = ARGV[5]

redis.call('ZREMRANGEBYSCORE', key, 0, cutoff)

local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, member)
    redis.call('EXPIRE', key, window)
    return 1
else
    return 0
end
`

// Lua script for token bucket rate limiting using a redis hash
var tokenBucketScript = `
-- KEYS[1] = the name of the bucket/key (e.g., "user:123:rate_limit")
-- ARGV[1] = capacity (maximum tokens allowed in the bucket)
-- ARGV[2] = refill rate (tokens per second)
-- ARGV[3] = requested tokens (usually 1)
-- ARGV[4] = current timestamp (e.g., in milliseconds or seconds)

local key = KEYS[1]
local capacity = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local requested_tokens = tonumber(ARGV[3])
local current_timestamp = tonumber(ARGV[4])

-- Get current tokens and last refill time
local bucket_info = redis.call("HMGET", key, "tokens", "last_refill")
local current_tokens = tonumber(bucket_info[1])
local last_refill = tonumber(bucket_info[2])

-- Initialize the bucket if it doesn't exist
if current_tokens == nil then
    current_tokens = capacity
    last_refill = current_timestamp
else
    -- Calculate tokens to add based on time elapsed
    local time_elapsed = current_timestamp - last_refill
    local tokens_to_add = math.floor(time_elapsed * refill_rate)
    
    if tokens_to_add > 0 then
        current_tokens = math.min(capacity, current_tokens + tokens_to_add)
        last_refill = current_timestamp
    end
end

-- Check if enough tokens are available for the request
if current_tokens >= requested_tokens then
    -- Consume tokens and update bucket info
    current_tokens = current_tokens - requested_tokens
    redis.call("HMSET", key, "tokens", current_tokens, "last_refill", last_refill)
    -- Set/reset TTL for the key (e.g., 10 minutes) to allow cleanup of inactive clients
    redis.call("EXPIRE", key, 600) -- Example TTL
    return 1 -- Request allowed
else
    -- Not enough tokens, request denied
    redis.call("HMSET", key, "tokens", current_tokens, "last_refill", last_refill)
    redis.call("EXPIRE", key, 600) -- Example TTL
    return 0 -- Request denied
end
`

type RedisStore struct {
	redisConnect *redis.Client
}

func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{redisConnect: client}
}

func (r *RedisStore) AllowedSlidingWindow(ip string, window int64, limit int) bool {
	ctx := context.Background()
	now := time.Now().UnixNano()
	cutoff := now - (window * 1e9) // Convert window (seconds) to nanoseconds
	key := "sliding:" + ip

	// Generate a unique member ID to avoid collisions when timestamps are identical
	randBytes := make([]byte, 8)
	if _, err := rand.Read(randBytes); err != nil {
		// If random read fails, fall back to timestamp-only member ID
		member := strconv.FormatInt(now, 10)
		result, err := r.redisConnect.Eval(ctx, slidingWindowScript, []string{key}, now, cutoff, limit, window, member).Int64()
		if err != nil {
			return true
		}
		return result == 1
	}
	member := hex.EncodeToString(randBytes)

	result, err := r.redisConnect.Eval(ctx, slidingWindowScript, []string{key}, now, cutoff, limit, window, member).Int64()
	if err != nil {
		// if Redis fails, fail open (allow the request)
		return true
	}
	return result == 1
}

func (r *RedisStore) AllowedTokenBucket(ip string, capacity, tokensPerInterval int, refillRate time.Duration) bool {
	// Validate refillRate to prevent division by zero
	if refillRate <= 0 {
		// Invalid config: fail open (allow the request)
		return true
	}

	ctx := context.Background()
	now := time.Now().Unix()
	key := "bucket:" + ip
	tokensPerSecond := float64(tokensPerInterval) / refillRate.Seconds()

	result, err := r.redisConnect.Eval(ctx, tokenBucketScript, []string{key}, capacity, tokensPerSecond, 1, now).Int64()
	if err != nil {
		return true
	}
	return result == 1
}
