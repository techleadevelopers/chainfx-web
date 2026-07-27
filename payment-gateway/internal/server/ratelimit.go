package server

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/config"

	"github.com/redis/go-redis/v9"
)

type rateLimitStore interface {
	Allow(key string) bool
	AllowN(key string, max int) (bool, time.Time, int)
	Stats() map[string]any
}

func newConfiguredRateLimiter(cfg *config.Config, windowMs, max int) rateLimitStore {
	if cfg == nil {
		return newRateLimiter(windowMs, max)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.RateLimitBackend)) {
	case "", "memory":
		return newRateLimiter(windowMs, max)
	case "redis":
		if strings.TrimSpace(cfg.RedisURL) == "" {
			slog.Warn("RATE_LIMIT_BACKEND=redis ignored because REDIS_URL is empty; using memory rate limiter")
			return newRateLimiter(windowMs, max)
		}
		limiter, err := newRedisRateLimiter(cfg.RedisURL, windowMs, max)
		if err != nil {
			slog.Warn("RATE_LIMIT_BACKEND=redis unavailable; using memory rate limiter", "error", err)
			return newRateLimiter(windowMs, max)
		}
		return limiter
	default:
		slog.Warn("unknown RATE_LIMIT_BACKEND; using memory rate limiter", "backend", cfg.RateLimitBackend)
		return newRateLimiter(windowMs, max)
	}
}

type rateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	max      int
	counters map[string]rateBucket
}

type rateBucket struct {
	ResetAt time.Time
	Count   int
}

func newRateLimiter(windowMs, max int) *rateLimiter {
	if windowMs <= 0 {
		windowMs = 60000
	}
	if max <= 0 {
		max = 20
	}
	return &rateLimiter{window: time.Duration(windowMs) * time.Millisecond, max: max, counters: make(map[string]rateBucket)}
}

func (l *rateLimiter) Allow(key string) bool {
	allowed, _, _ := l.AllowN(key, l.max)
	return allowed
}

func (l *rateLimiter) AllowN(key string, max int) (bool, time.Time, int) {
	if max <= 0 {
		max = l.max
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.counters[key]
	if b.ResetAt.IsZero() || now.After(b.ResetAt) {
		b = rateBucket{ResetAt: now.Add(l.window)}
	}
	b.Count++
	l.counters[key] = b
	remaining := max - b.Count
	if remaining < 0 {
		remaining = 0
	}
	return b.Count <= max, b.ResetAt, remaining
}

func (l *rateLimiter) Stats() map[string]any {
	if l == nil {
		return map[string]any{"backend": "memory", "status": "nil"}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]any{
		"backend": "memory",
		"status":  "ok",
		"window":  l.window.String(),
		"max":     l.max,
		"buckets": len(l.counters),
	}
}

type redisRateLimiter struct {
	client *redis.Client
	window time.Duration
	max    int
}

var redisRateLimitScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
local ttl = redis.call("PTTL", KEYS[1])
if ttl < 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return { current, ttl }
`)

func newRedisRateLimiter(redisURL string, windowMs, max int) (*redisRateLimiter, error) {
	if windowMs <= 0 {
		windowMs = 60000
	}
	if max <= 0 {
		max = 20
	}
	opt, err := redis.ParseURL(strings.TrimSpace(redisURL))
	if err != nil {
		return nil, err
	}
	opt.PoolSize = 32
	opt.MinIdleConns = 4
	opt.MaxRetries = 2
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = 700 * time.Millisecond
	opt.WriteTimeout = 700 * time.Millisecond
	opt.PoolTimeout = 800 * time.Millisecond
	client := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &redisRateLimiter{client: client, window: time.Duration(windowMs) * time.Millisecond, max: max}, nil
}

func (l *redisRateLimiter) Allow(key string) bool {
	allowed, _, _ := l.AllowN(key, l.max)
	return allowed
}

func (l *redisRateLimiter) AllowN(key string, max int) (bool, time.Time, int) {
	if l == nil || l.client == nil {
		return true, time.Now().Add(time.Minute), max
	}
	if max <= 0 {
		max = l.max
	}
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	redisKey := "rl:" + sanitizeRedisRateKey(key)
	result, err := redisRateLimitScript.Run(ctx, l.client, []string{redisKey}, int(l.window.Milliseconds())).Result()
	if err != nil {
		slog.Warn("redis rate limiter failed open", "error", err)
		return true, time.Now().Add(l.window), max
	}
	values, ok := result.([]any)
	if !ok || len(values) < 2 {
		slog.Warn("redis rate limiter unexpected script response")
		return true, time.Now().Add(l.window), max
	}
	count := redisInt(values[0])
	ttlMs := redisInt(values[1])
	if ttlMs <= 0 {
		ttlMs = int(l.window.Milliseconds())
	}
	remaining := max - count
	if remaining < 0 {
		remaining = 0
	}
	return count <= max, time.Now().Add(time.Duration(ttlMs) * time.Millisecond), remaining
}

func (l *redisRateLimiter) Stats() map[string]any {
	if l == nil || l.client == nil {
		return map[string]any{"backend": "redis", "status": "nil"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	stats := l.client.PoolStats()
	status := "ok"
	if err := l.client.Ping(ctx).Err(); err != nil {
		status = "error: " + err.Error()
	}
	return map[string]any{
		"backend":    "redis",
		"status":     status,
		"window":     l.window.String(),
		"max":        l.max,
		"hits":       stats.Hits,
		"misses":     stats.Misses,
		"timeouts":   stats.Timeouts,
		"totalConns": stats.TotalConns,
		"idleConns":  stats.IdleConns,
		"staleConns": stats.StaleConns,
	}
}

func sanitizeRedisRateKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(" ", "_", "\n", "_", "\r", "_", "\t", "_")
	return replacer.Replace(key)
}

func redisInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case string:
		n := 0
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
		}
		return n
	default:
		return 0
	}
}
