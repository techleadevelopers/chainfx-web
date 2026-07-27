package resilience

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
)

// RetryConfig controls retry behaviour.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Multiplier  float64
	Jitter      bool
}

// DefaultRetryConfig returns 3 attempts: 3s → 6s → 12s.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 3, BaseDelay: 3 * time.Second, MaxDelay: 30 * time.Second, Multiplier: 2.0, Jitter: true}
}

// FastRetryConfig is for RPC calls: 4 attempts 500ms → 1s → 2s → 4s.
func FastRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 4, BaseDelay: 500 * time.Millisecond, MaxDelay: 8 * time.Second, Multiplier: 2.0, Jitter: true}
}

// IsRetryable decides whether to keep retrying after a given error.
type IsRetryable func(err error) bool

// AlwaysRetry retries on any non-nil error.
func AlwaysRetry(_ error) bool { return true }

// DoWithContext retries fn with exponential backoff.
func DoWithContext(ctx context.Context, cfg RetryConfig, name string, retryable IsRetryable, fn func(context.Context) error) error {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	if retryable == nil {
		retryable = AlwaysRetry
	}
	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = fn(ctx)
		if lastErr == nil {
			if attempt > 1 {
				slog.Info("Retry succeeded", "op", name, "attempt", attempt)
			}
			return nil
		}
		if attempt == cfg.MaxAttempts {
			break
		}
		if !retryable(lastErr) {
			return lastErr
		}
		delay := backoffDelay(cfg, attempt)
		slog.Warn("Retrying", "op", name, "attempt", attempt, "of", cfg.MaxAttempts,
			"delay_ms", delay.Milliseconds(), "error", lastErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("%s: all %d attempts failed: %w", name, cfg.MaxAttempts, lastErr)
}

// Do retries fn with a background context.
func Do(cfg RetryConfig, name string, fn func(context.Context) error) error {
	return DoWithContext(context.Background(), cfg, name, AlwaysRetry, fn)
}

func backoffDelay(cfg RetryConfig, attempt int) time.Duration {
	d := float64(cfg.BaseDelay) * math.Pow(cfg.Multiplier, float64(attempt-1))
	if cfg.MaxDelay > 0 && time.Duration(d) > cfg.MaxDelay {
		d = float64(cfg.MaxDelay)
	}
	if cfg.Jitter {
		jitter := d * 0.2
		// deterministic-ish jitter using nanosecond clock
		ns := float64(time.Now().UnixNano()%1000) / 999.0
		d += jitter*(ns*2-1)
	}
	if d < 0 {
		d = float64(cfg.BaseDelay)
	}
	return time.Duration(d)
}
