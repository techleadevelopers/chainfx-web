// Package psp defines the Payment Service Provider abstraction layer.
// It allows ChainFX to route PIX intents between Efí Bank and any future
// backup provider without code changes — only config.
//
// Auto-failover: when the active provider accumulates ≥5 consecutive errors
// the Router promotes the backup automatically and retries the original
// provider every 5 minutes until it recovers.
package psp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// PixCharge represents a PIX charge creation request, provider-agnostic.
type PixCharge struct {
	ExternalID  string
	AmountBRL   float64
	Description string
	PayerName   string
	PayerCPF    string // CPF or CNPJ
	ExpirySec   int    // QR code expiry in seconds
}

// PixChargeResult is the provider-agnostic result of a charge creation.
type PixChargeResult struct {
	Provider     string
	ChargeID     string
	TXID         string
	PixCopyPaste string
	QRCodeB64    string
	ExpiresAt    time.Time
	AmountBRL    float64
}

// PixWebhookPayload is delivered to the business layer after normalising the
// provider-specific JSON.  One payload = one PIX payment event.
type PixWebhookPayload struct {
	Provider   string
	TXID       string
	EndToEndID string
	AmountBRL  float64
	PaidAt     time.Time
	PayerName  string
	PayerKey   string
}

// Provider is the core interface every PIX PSP adapter must implement.
type Provider interface {
	// Name returns a short stable identifier, e.g. "efi" or "asaas".
	Name() string

	// CreateCharge creates a PIX QR code charge and returns the result.
	CreateCharge(ctx context.Context, charge PixCharge) (*PixChargeResult, error)

	// ParseWebhook parses and validates the provider-specific webhook body,
	// returning the first normalised PixWebhookPayload.
	ParseWebhook(ctx context.Context, body []byte, secret string) (*PixWebhookPayload, error)

	// ParseWebhookAll returns one PixWebhookPayload per payment event in the
	// webhook body.  Efí Bank batches multiple PIX events in a single POST;
	// this method processes each one independently.
	ParseWebhookAll(ctx context.Context, body []byte, secret string) ([]PixWebhookPayload, error)

	// HealthCheck performs a lightweight liveness probe.
	HealthCheck(ctx context.Context) error
}

// ── Router ────────────────────────────────────────────────────────────────────

// FailureThreshold is the number of consecutive errors before auto-failover.
const FailureThreshold = 5

// ErrNoProvider is returned when no provider is configured.
var ErrNoProvider = errors.New("psp: no provider configured")

// Router holds the active + backup providers and performs automatic failover.
// All public methods are goroutine-safe.
type Router struct {
	mu               sync.RWMutex
	active           Provider
	backup           Provider
	consecutiveFails atomic.Int64
	failedOver       atomic.Bool
	failoverAt       time.Time
}

// NewRouter creates a Router. backup may be nil for single-provider setups.
func NewRouter(active, backup Provider) *Router {
	return &Router{active: active, backup: backup}
}

// ActiveProvider returns the name of the currently active provider.
func (rt *Router) ActiveProvider() string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if rt.active == nil {
		return "none"
	}
	return rt.active.Name()
}

// CreateCharge routes a charge to the active provider.
// On failure it attempts the backup and auto-failovers when the threshold is hit.
func (rt *Router) CreateCharge(ctx context.Context, charge PixCharge) (*PixChargeResult, error) {
	if rt.active == nil {
		return nil, ErrNoProvider
	}

	result, err := rt.activeProvider().CreateCharge(ctx, charge)
	if err == nil {
		rt.consecutiveFails.Store(0)
		return result, nil
	}

	fails := rt.consecutiveFails.Add(1)
	slog.Warn("psp router: active provider error",
		"provider", rt.ActiveProvider(),
		"consecutive_fails", fails,
		"error", err,
	)

	if fails >= FailureThreshold && rt.backup != nil && !rt.failedOver.Load() {
		rt.promoteBackup()
	}

	// Attempt backup immediately.
	rt.mu.RLock()
	backup := rt.backup
	active := rt.active
	rt.mu.RUnlock()

	if backup != nil && backup.Name() != active.Name() {
		slog.Info("psp router: retrying on backup", "provider", backup.Name())
		result, backupErr := backup.CreateCharge(ctx, charge)
		if backupErr == nil {
			return result, nil
		}
		slog.Error("psp router: backup also failed", "provider", backup.Name(), "error", backupErr)
		return nil, fmt.Errorf("psp: all providers failed — primary=%v backup=%v", err, backupErr)
	}

	return nil, err
}

// ParseWebhookAll routes the body to the active provider and returns all
// payment events normalised as []PixWebhookPayload.
// The active provider is always used for incoming webhooks (the URL is
// provider-specific so ambiguity is not possible in practice).
func (rt *Router) ParseWebhookAll(ctx context.Context, body []byte, secret string) ([]PixWebhookPayload, error) {
	p := rt.activeProvider()
	if p == nil {
		return nil, ErrNoProvider
	}
	return p.ParseWebhookAll(ctx, body, secret)
}

// ParseWebhook routes webhook parsing to the named provider (for compatibility
// with providers whose webhook URL does not match the active provider).
func (rt *Router) ParseWebhook(ctx context.Context, providerName string, body []byte, secret string) (*PixWebhookPayload, error) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, p := range []Provider{rt.active, rt.backup} {
		if p != nil && p.Name() == providerName {
			return p.ParseWebhook(ctx, body, secret)
		}
	}
	return nil, fmt.Errorf("psp: unknown provider %q", providerName)
}

// ProbeAll runs HealthCheck on all providers and logs results.
// Automatically attempts to restore the original active after 5 minutes.
func (rt *Router) ProbeAll(ctx context.Context) {
	rt.mu.RLock()
	active := rt.active
	backup := rt.backup
	failedOver := rt.failedOver.Load()
	failoverAt := rt.failoverAt
	rt.mu.RUnlock()

	for _, p := range []Provider{active, backup} {
		if p == nil {
			continue
		}
		if err := p.HealthCheck(ctx); err != nil {
			slog.Warn("psp: health check failed", "provider", p.Name(), "error", err)
		} else {
			slog.Debug("psp: health check ok", "provider", p.Name())
		}
	}

	// After a failover, try to restore the original active provider.
	if failedOver && time.Since(failoverAt) > 5*time.Minute {
		rt.tryRestoreActive(ctx, active)
	}
}

// ── internals ─────────────────────────────────────────────────────────────────

func (rt *Router) activeProvider() Provider {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.active
}

func (rt *Router) promoteBackup() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.backup == nil {
		return
	}
	slog.Warn("psp router: auto-failover triggered",
		"from", rt.active.Name(),
		"to", rt.backup.Name(),
	)
	rt.active, rt.backup = rt.backup, rt.active
	rt.failedOver.Store(true)
	rt.failoverAt = time.Now()
	rt.consecutiveFails.Store(0)
}

func (rt *Router) tryRestoreActive(ctx context.Context, original Provider) {
	if original == nil {
		return
	}
	if err := original.HealthCheck(ctx); err != nil {
		slog.Debug("psp router: original provider still unhealthy, staying on backup",
			"provider", original.Name(), "error", err)
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.active, rt.backup = original, rt.active
	rt.failedOver.Store(false)
	rt.consecutiveFails.Store(0)
	slog.Info("psp router: restored original provider", "provider", original.Name())
}
