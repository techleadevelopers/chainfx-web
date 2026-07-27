package workers

// webhookdelivery.go — Phase 5: Outbound webhook delivery worker.
//
// WebhookDeliveryWorker:
//  1. Subscribes to all Phase 5 bus events.
//  2. For each event, looks up active webhook subscriptions that listen to it.
//  3. Persists a webhook_deliveries row for each subscription.
//  4. Sends HMAC-SHA256-signed HTTP POST requests with exponential backoff
//     (up to 5 retries: 1m, 5m, 30m, 2h, 24h).
//  5. Validates target URL against private/loopback ranges at send time
//     (SSRF protection — see chainfx-webhooks-ssrf.md).

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/models"
)

const webhookMaxAttempts = 5

// backoffSchedule maps attempt number → next retry delay.
var backoffSchedule = []time.Duration{
	time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	24 * time.Hour,
}

// webhookDeliveryEvents lists all event types that trigger webhook delivery.
var webhookDeliveryEvents = models.Phase5WebhookEvents

// WebhookDeliveryWorker dispatches outbound webhooks with retry + HMAC signing.
type WebhookDeliveryWorker struct {
	bus        *EventBus
	db         *database.DB
	cfg        *config.Config
	httpClient *http.Client
}

func NewWebhookDeliveryWorker(bus *EventBus, db *database.DB, cfg *config.Config) *WebhookDeliveryWorker {
	return &WebhookDeliveryWorker{
		bus: bus,
		db:  db,
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (w *WebhookDeliveryWorker) Start(ctx context.Context) {
	slog.Info("WebhookDeliveryWorker iniciado")

	// Subscribe to all webhook-able events
	channels := make([]chan Event, 0, len(webhookDeliveryEvents))
	for _, evType := range webhookDeliveryEvents {
		ch := w.bus.Subscribe(evType)
		channels = append(channels, ch)
		defer w.bus.Unsubscribe(evType, ch)
	}
	merged := mergeEventChannels(ctx, channels...)

	// Retry ticker for stuck / failed deliveries
	retryTicker := time.NewTicker(5 * time.Minute)
	defer retryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("WebhookDeliveryWorker encerrado")
			return
		case ev, ok := <-merged:
			if !ok {
				return
			}
			w.dispatchEvent(ctx, ev)
		case <-retryTicker.C:
			w.retryFailed(ctx)
		}
	}
}

// dispatchEvent creates delivery rows for all subscriptions matching the event
// and immediately attempts the first delivery.
func (w *WebhookDeliveryWorker) dispatchEvent(ctx context.Context, ev Event) {
	subs, err := w.subscriptionsForEvent(ctx, ev.Type)
	if err != nil || len(subs) == 0 {
		return
	}

	payloadBytes, _ := json.Marshal(map[string]any{
		"event":     ev.Type,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data":      ev.Payload,
	})

	for _, sub := range subs {
		deliveryID, err := w.enqueueDelivery(ctx, sub.ID, ev.Type, payloadBytes)
		if err != nil {
			slog.Warn("WebhookDeliveryWorker: erro ao enfileirar", "sub", sub.ID, "error", err)
			continue
		}
		w.attemptDelivery(ctx, deliveryID, sub.TargetURL, sub.Secret, payloadBytes)
	}
}

// attemptDelivery performs a single HTTP POST to the target URL.
func (w *WebhookDeliveryWorker) attemptDelivery(ctx context.Context, deliveryID, targetURL, secret string, payload []byte) {
	// SSRF check at send time (not just at creation)
	if err := validateTargetURL(targetURL); err != nil {
		slog.Warn("WebhookDeliveryWorker: URL bloqueada (SSRF)", "url", targetURL, "error", err)
		w.markResult(ctx, deliveryID, models.WebhookDeliveryFailed, 0, "", err.Error(), nil)
		return
	}

	sig := computeHMAC(secret, payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		w.markResult(ctx, deliveryID, models.WebhookDeliveryFailed, 0, "", err.Error(), nil)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ChainFX-Signature", "sha256="+sig)
	req.Header.Set("X-ChainFX-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))
	req.Header.Set("User-Agent", "ChainFX-Webhook/1.0")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		nextRetry := w.nextRetryAt(ctx, deliveryID)
		w.markResult(ctx, deliveryID, models.WebhookDeliveryRetrying, 0, "", err.Error(), nextRetry)
		return
	}
	defer resp.Body.Close()

	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	respBody := string(buf[:n])

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Debug("WebhookDeliveryWorker: entregue", "delivery_id", deliveryID, "status", resp.StatusCode)
		w.markResult(ctx, deliveryID, models.WebhookDeliveryDelivered, resp.StatusCode, respBody, "", nil)
		return
	}

	// Non-2xx — schedule retry
	nextRetry := w.nextRetryAt(ctx, deliveryID)
	status := models.WebhookDeliveryRetrying
	if nextRetry == nil {
		status = models.WebhookDeliveryFailed
	}
	w.markResult(ctx, deliveryID, status, resp.StatusCode, respBody,
		fmt.Sprintf("target retornou HTTP %d", resp.StatusCode), nextRetry)
}

func (w *WebhookDeliveryWorker) retryFailed(ctx context.Context) {
	rows, err := w.db.SQL.QueryContext(ctx, `
		SELECT wd.id, wd.payload::text, ws.target_url, ws.secret
		FROM webhook_deliveries wd
		JOIN webhook_subscriptions ws ON ws.id = wd.subscription_id
		WHERE wd.status IN ('pending','retrying')
		  AND (wd.next_retry_at IS NULL OR wd.next_retry_at <= NOW())
		  AND wd.attempts < $1
		  AND ws.active = true
		ORDER BY wd.created_at ASC LIMIT 20
		FOR UPDATE OF wd SKIP LOCKED`, webhookMaxAttempts)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, payloadStr, targetURL, secret string
		if rows.Scan(&id, &payloadStr, &targetURL, &secret) == nil {
			w.attemptDelivery(ctx, id, targetURL, secret, []byte(payloadStr))
		}
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func (w *WebhookDeliveryWorker) subscriptionsForEvent(ctx context.Context, eventType string) ([]struct {
	ID        string
	TargetURL string
	Secret    string
}, error) {
	rows, err := w.db.SQL.QueryContext(ctx, `
		SELECT id, target_url, secret
		FROM webhook_subscriptions
		WHERE active=true AND ($1 = ANY(events) OR '*' = ANY(events))`,
		eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		ID        string
		TargetURL string
		Secret    string
	}
	for rows.Next() {
		var s struct {
			ID        string
			TargetURL string
			Secret    string
		}
		if rows.Scan(&s.ID, &s.TargetURL, &s.Secret) == nil {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

func (w *WebhookDeliveryWorker) enqueueDelivery(ctx context.Context, subID, eventType string, payload []byte) (string, error) {
	var id string
	err := w.db.SQL.QueryRowContext(ctx, `
		INSERT INTO webhook_deliveries (subscription_id, event_type, payload, status)
		VALUES ($1, $2, $3::jsonb, 'pending') RETURNING id`,
		subID, eventType, string(payload)).Scan(&id)
	return id, err
}

func (w *WebhookDeliveryWorker) markResult(ctx context.Context, id string,
	status models.WebhookDeliveryStatus, respStatus int, respBody, errMsg string, nextRetry *time.Time) {
	var respStatusPtr *int
	if respStatus > 0 {
		respStatusPtr = &respStatus
	}
	_, _ = w.db.SQL.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status=$2, attempts=attempts+1,
		    response_status=$3, response_body=NULLIF($4,''),
		    last_error=NULLIF($5,''), next_retry_at=$6, updated_at=NOW()
		WHERE id=$1`, id, string(status), respStatusPtr, respBody, errMsg, nextRetry)
}

func (w *WebhookDeliveryWorker) nextRetryAt(ctx context.Context, deliveryID string) *time.Time {
	var attempts int
	_ = w.db.SQL.QueryRowContext(ctx,
		"SELECT attempts FROM webhook_deliveries WHERE id=$1", deliveryID).Scan(&attempts)
	if attempts >= webhookMaxAttempts {
		return nil
	}
	idx := attempts
	if idx >= len(backoffSchedule) {
		idx = len(backoffSchedule) - 1
	}
	t := time.Now().Add(backoffSchedule[idx])
	return &t
}

func computeHMAC(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// validateTargetURL is an SSRF guard used at delivery time (separate from
// the creation-time check in mobile/helpers_phase5.go).
func validateTargetURL(rawURL string) error {
	u, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return fmt.Errorf("URL inválida: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme não permitido: %s", u.Scheme)
	}
	host := u.Hostname()
	blocked := []string{"localhost", "0.0.0.0", "::1", "metadata.google.internal", "169.254.169.254"}
	lhost := strings.ToLower(host)
	for _, b := range blocked {
		if lhost == b {
			return fmt.Errorf("host bloqueado: %s", host)
		}
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil
	}
	privateRanges := []string{
		"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12",
		"192.168.0.0/16", "169.254.0.0/16", "fe80::/10", "fc00::/7",
		"100.64.0.0/10", "0.0.0.0/8", "240.0.0.0/4", "224.0.0.0/4",
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		for _, cidr := range privateRanges {
			_, network, _ := net.ParseCIDR(cidr)
			if network != nil && network.Contains(ip) {
				return fmt.Errorf("endereço interno bloqueado: %s → %s", host, addr)
			}
		}
	}
	return nil
}
