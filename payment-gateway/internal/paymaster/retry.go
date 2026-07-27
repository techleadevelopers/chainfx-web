package paymaster

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"
)

// ErrNonRetryable wraps errors that should not trigger a backoff retry
// (e.g., 4xx responses from the signer — invalid sig, permit expired).
// Callers wrap their errors: fmt.Errorf("%w: %v", ErrNonRetryable, underlying).
var ErrNonRetryable = errors.New("paymaster: non-retryable error")

// RetryConfig controls the exponential-backoff behaviour.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	CapDelay    time.Duration
}

// DefaultRetryConfig returns the production-grade defaults:
// 4 attempts, base 500 ms, cap 8 s, full-jitter.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 4,
		BaseDelay:   500 * time.Millisecond,
		CapDelay:    8 * time.Second,
	}
}

// ExecuteWithRetry runs operation with exponential backoff + full jitter.
//
//   - Non-retryable errors (wrapping ErrNonRetryable) surface immediately.
//   - Context cancellation is honoured during sleep.
//   - Returns the last error after MaxAttempts are exhausted.
func ExecuteWithRetry(ctx context.Context, cfg RetryConfig, label string, operation func(ctx context.Context) error) error {
	var lastErr error

	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		lastErr = operation(ctx)
		if lastErr == nil {
			return nil
		}

		// Surface non-retryable errors immediately (4xx from signer).
		if errors.Is(lastErr, ErrNonRetryable) {
			slog.Warn("relay: non-retryable error, aborting",
				"label", label,
				"attempt", attempt+1,
				"error", lastErr,
			)
			return lastErr
		}

		if attempt == cfg.MaxAttempts-1 {
			break
		}

		// Full-jitter sleep: uniform(0, min(cap, base * 2^attempt))
		window := float64(cfg.BaseDelay) * float64(uint(1)<<attempt)
		capF := float64(cfg.CapDelay)
		if window > capF {
			window = capF
		}
		jitter := time.Duration(rand.Int63n(int64(window) + 1))

		slog.Warn("relay: attempt failed, will retry",
			"label", label,
			"attempt", attempt+1,
			"max", cfg.MaxAttempts,
			"backoff_ms", jitter.Milliseconds(),
			"error", lastErr,
		)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jitter):
		}
	}

	slog.Error("relay: all attempts exhausted, moving to DLQ",
		"label", label,
		"attempts", cfg.MaxAttempts,
		"error", lastErr,
	)
	return lastErr
}
