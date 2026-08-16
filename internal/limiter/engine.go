package limiter

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Result represents the outcome of a rate limit check.
type Result struct {
	Allowed     bool
	Remaining   int64
	Limit       int64
	RetryAfter  time.Duration
}

// Engine handles rate limiting using Redis with atomic Lua scripting.
type Engine struct {
	rdb *redis.Client
}

// New creates a new rate limiter engine connected to the given Redis client.
func New(rdb *redis.Client) *Engine {
	return &Engine{rdb: rdb}
}

// script is a Lua script that atomically checks and increments the rate limit counter.
// It uses a sliding window log approach for accurate rate limiting.
// KEYS[1] = rate limit key
// ARGV[1] = current timestamp in milliseconds
// ARGV[2] = window size in milliseconds
// ARGV[3] = max requests allowed
var script = redis.NewScript(`
-- Remove old entries outside the window
local window_start = ARGV[1] - ARGV[2]
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', window_start)

-- Count current requests in window
local current = redis.call('ZCARD', KEYS[1])

if current < tonumber(ARGV[3]) then
    -- Allow: add the request timestamp with a unique member
    local member = ARGV[1] .. "-" .. redis.call('INCR', KEYS[1] .. ":seq")
    redis.call('ZADD', KEYS[1], ARGV[1], member)
    -- Set expiry to auto-clean
    redis.call('EXPIRE', KEYS[1], math.ceil(tonumber(ARGV[2]) / 1000) + 1)
    redis.call('EXPIRE', KEYS[1] .. ":seq", math.ceil(tonumber(ARGV[2]) / 1000) + 1)
    local remaining = tonumber(ARGV[3]) - current - 1
    return {1, remaining, 0}
else
    -- Denied: find when the oldest entry expires
    local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
    local retry_after = 0
    if #oldest >= 2 then
        retry_after = (tonumber(oldest[2]) + tonumber(ARGV[2]) - ARGV[1]) / 1000
        if retry_after < 1 then
            retry_after = 1
        end
    end
    return {0, 0, math.ceil(retry_after)}
end
`)

// Check atomically checks if a request is allowed under the given rate limit.
// key: unique identifier for the client (e.g., client ID + tier)
// limit: maximum requests allowed in the window
// window: time window for the rate limit
func (e *Engine) Check(ctx context.Context, key string, limit int, window time.Duration) (*Result, error) {
	now := time.Now().UnixMilli()
	windowMs := window.Milliseconds()

	result, err := script.Run(ctx, e.rdb, []string{key}, now, windowMs, limit).Result()
	if err != nil {
		return nil, fmt.Errorf("lua script: %w", err)
	}

	vals, ok := result.([]interface{})
	if !ok || len(vals) < 3 {
		return nil, fmt.Errorf("unexpected lua result format: %v", result)
	}

	allowed, ok := vals[0].(int64)
	if !ok {
		allowed = 0
	}
	remaining, ok := vals[1].(int64)
	if !ok {
		remaining = 0
	}
	retryAfterSecs, ok := vals[2].(int64)
	if !ok {
		retryAfterSecs = 0
	}

	return &Result{
		Allowed:    allowed == 1,
		Remaining:  remaining,
		Limit:      int64(limit),
		RetryAfter: time.Duration(retryAfterSecs) * time.Second,
	}, nil
}

// Reset removes all rate limit data for a given key.
func (e *Engine) Reset(ctx context.Context, key string) error {
	return e.rdb.Del(ctx, key).Err()
}

// Health checks the Redis connection.
func (e *Engine) Health(ctx context.Context) error {
	return e.rdb.Ping(ctx).Err()
}