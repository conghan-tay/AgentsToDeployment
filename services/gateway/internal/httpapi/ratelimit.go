package httpapi

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type RateLimiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

type RedisRateLimiter struct {
	client *redis.Client
	limit  int64
	window time.Duration
}

func NewRedisRateLimiter(client *redis.Client, limit int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{client: client, limit: int64(limit), window: window}
}

var fixedWindowScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return count
`)

func (l *RedisRateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	count, err := fixedWindowScript.Run(
		ctx, l.client, []string{"rate:" + key}, l.window.Milliseconds(),
	).Int64()
	if err != nil {
		return false, err
	}
	return count <= l.limit, nil
}

type allowAllLimiter struct{}

func (allowAllLimiter) Allow(context.Context, string) (bool, error) { return true, nil }
