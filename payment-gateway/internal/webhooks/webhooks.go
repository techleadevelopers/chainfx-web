// Package webhooks implements outbound automation notifications for n8n,
// Zapier, Make.com (Integromat) and any other generic HTTP receiver.
//
// It listens on the internal workers.EventBus and, for every canonical
// automation trigger (order.created, order.completed, order.failed,
// payment.received, payout.sent, price.change, dca.executed), fans the
// event out to every active subscription that opted into it.
//
// The package is split by responsibility:
//   - registry.go     — subscription CRUD (create/list/get/delete/enable).
//   - delivery.go      — a single signed HTTP POST attempt.
//   - retry_queue.go   — async delivery with exponential backoff.
//   - logs.go          — read access to persisted delivery attempts.
//   - dashboard.go     — aggregated health summary for operators.
package webhooks

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/workers"
)

// Canonical automation trigger events, as consumed by n8n / Zapier / Make.
const (
	EventOrderCreated    = "order.created"
	EventOrderCompleted  = "order.completed"
	EventOrderFailed     = "order.failed"
	EventPaymentReceived = "payment.received"
	EventPayoutSent      = "payout.sent"
	EventPriceChange     = "price.change"
	EventDCAExecuted     = "dca.executed"

	// M2M agent payment lifecycle events
	EventM2MIntentCreated    = "m2m.intent.created"
	EventM2MDepositReceived  = "m2m.deposit.received"
	EventM2MOverpayment      = "m2m.overpayment.detected" // excess deposit, manual reconciliation needed
	EventM2MCardPaymentDue   = "m2m.card_payment.due"     // credit-card/payment-link destination ready for RPA/operator
	EventM2MSettlementDone   = "m2m.settlement.done"      // canonical name (was m2m.settled)
	EventM2MSettlementFailed = "m2m.settlement.failed"

	// Marketplace/capability lifecycle events
	EventCapabilityPurchased = "capability.purchased"
	EventCapabilityExecuted  = "capability.executed"
	EventCapabilityGranted   = "capability.granted"

	// ChainFX Tap closed-loop NFC lifecycle events.
	EventNFCCaptured = "nfc.capture.completed"
	EventNFCReversed = "nfc.authorization.reversed"
	EventNFCExpired  = "nfc.authorization.expired"
)

// AllEvents lists every trigger automation platforms can subscribe to.
func AllEvents() []string {
	return []string{
		EventOrderCreated,
		EventOrderCompleted,
		EventOrderFailed,
		EventPaymentReceived,
		EventPayoutSent,
		EventPriceChange,
		EventDCAExecuted,
		EventM2MIntentCreated,
		EventM2MDepositReceived,
		EventM2MOverpayment,
		EventM2MCardPaymentDue,
		EventM2MSettlementDone,
		EventM2MSettlementFailed,
		EventCapabilityPurchased,
		EventCapabilityExecuted,
		EventCapabilityGranted,
		EventNFCCaptured,
		EventNFCReversed,
		EventNFCExpired,
	}
}

// Known third-party automation providers. "generic" is used for any plain
// HTTP receiver that isn't one of the well-known platforms.
const (
	ProviderN8N     = "n8n"
	ProviderZapier  = "zapier"
	ProviderMake    = "make"
	ProviderGeneric = "generic"
)

// Dispatcher subscribes to the internal event bus, translates internal
// events into canonical automation triggers, and hands off delivery to a
// RetryQueue so slow/unreachable receivers never block the event bus.
type Dispatcher struct {
	db         *database.DB
	cfg        *config.Config
	client     *http.Client
	maxRetries int
	queue      *RetryQueue
	running    atomic.Bool
	heartbeat  atomic.Int64
}

// New creates a Dispatcher and its backing retry queue. Call Start to begin
// consuming bus events.
func New(db *database.DB, cfg *config.Config) *Dispatcher {
	maxRetries := cfg.WebhooksMaxRetries
	if maxRetries <= 0 {
		maxRetries = 1
	}
	d := &Dispatcher{
		db:         db,
		cfg:        cfg,
		client:     &http.Client{Timeout: 10 * time.Second},
		maxRetries: maxRetries,
	}
	d.queue = NewRetryQueue(d, 4)
	return d
}

// busEventMapping translates an internal worker bus event type into zero or
// more canonical automation trigger names (as published to webhook subscribers).
//
// Rule: bus event names (internal) → canonical webhook event names (public).
// Both sides must be kept in sync — add a row here whenever the workers.go
// publishes a new event type, and add the canonical name to AllEvents().
func busEventMapping(eventType string) []string {
	switch eventType {
	// ── Order / buy lifecycle (existing) ──────────────────────────────────────
	case "order.created", "buy.created":
		return []string{EventOrderCreated}
	case "buy.paid":
		return []string{EventPaymentReceived}
	case "payout.settled":
		return []string{EventOrderCompleted, EventPayoutSent}
	case "buy.sent", "sweep.sent":
		return []string{EventOrderCompleted}
	case "price.updated":
		return []string{EventPriceChange}
	case "dca.buy.requested":
		return []string{EventDCAExecuted}
	case "order.failed", "buy.failed":
		return []string{EventOrderFailed}

	// ── M2M agent payment lifecycle ───────────────────────────────────────────
	// Bus: "m2m.intent.created"     → Canonical: EventM2MIntentCreated
	// Bus: "m2m.deposit.confirmed"  → Canonical: EventM2MDepositReceived
	// Bus: "m2m.settlement.done"    → Canonical: EventM2MSettlementDone
	// Bus: "m2m.settlement.failed"  → Canonical: EventM2MSettlementFailed
	case "m2m.intent.created":
		return []string{EventM2MIntentCreated}
	case "m2m.deposit.confirmed":
		return []string{EventM2MDepositReceived}
	case "m2m.overpayment.detected":
		return []string{EventM2MOverpayment}
	case "m2m.card_payment.due":
		return []string{EventM2MCardPaymentDue}
	case "m2m.settlement.done":
		return []string{EventM2MSettlementDone}
	case "m2m.settlement.failed":
		return []string{EventM2MSettlementFailed}

	// ── Capability marketplace lifecycle ──────────────────────────────────────
	// Bus: "marketplace.capability.purchased" → Canonical: EventCapabilityPurchased
	// Bus: "marketplace.capability.granted"   → Canonical: EventCapabilityGranted
	// Bus: "marketplace.capability.executed"  → Canonical: EventCapabilityExecuted
	case "marketplace.capability.purchased":
		return []string{EventCapabilityPurchased}
	case "marketplace.capability.granted":
		return []string{EventCapabilityGranted}
	case "marketplace.capability.executed":
		return []string{EventCapabilityExecuted}

	// ChainFX Tap closed-loop NFC lifecycle.
	case "nfc.capture.completed":
		return []string{EventNFCCaptured, EventPaymentReceived}
	case "nfc.authorization.reversed":
		return []string{EventNFCReversed}
	case "nfc.authorization.expired":
		return []string{EventNFCExpired}

	default:
		return nil
	}
}

// Start subscribes to every relevant bus event type and dispatches webhooks
// in the background until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context, bus *workers.EventBus) {
	if !d.cfg.WebhooksEnabled {
		slog.Info("Webhooks: automação desabilitada (WEBHOOKS_ENABLED=false)")
		return
	}
	d.running.Store(true)
	d.heartbeat.Store(time.Now().UTC().Unix())
	go d.runHeartbeat(ctx)
	d.queue.Start(ctx)
	busTypes := []string{
		// Order / buy lifecycle (existing)
		"order.created", "buy.created", "buy.paid", "payout.settled",
		"buy.sent", "sweep.sent", "price.updated", "dca.buy.requested",
		"order.failed", "buy.failed",
		// M2M agent payment lifecycle (new)
		"m2m.intent.created", "m2m.deposit.confirmed",
		"m2m.overpayment.detected", "m2m.card_payment.due",
		"m2m.settlement.done", "m2m.settlement.failed",
		// Capability marketplace lifecycle (new)
		"marketplace.capability.purchased", "marketplace.capability.granted",
		"marketplace.capability.executed",
		// ChainFX Tap closed-loop NFC lifecycle
		"nfc.capture.completed", "nfc.authorization.reversed", "nfc.authorization.expired",
	}
	for _, t := range busTypes {
		ch := bus.Subscribe(t)
		go d.consume(ctx, bus, t, ch)
	}
	slog.Info("Webhooks: dispatcher de automação (n8n/Zapier/Make) iniciado")
}

func (d *Dispatcher) runHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer d.running.Store(false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.heartbeat.Store(time.Now().UTC().Unix())
		}
	}
}

func (d *Dispatcher) Status() map[string]any {
	if d == nil {
		return map[string]any{"enabled": false, "running": false}
	}
	lastUnix := d.heartbeat.Load()
	var last any
	if lastUnix > 0 {
		last = time.Unix(lastUnix, 0).UTC()
	}
	return map[string]any{
		"enabled":        d.cfg != nil && d.cfg.WebhooksEnabled,
		"running":        d.running.Load(),
		"last_heartbeat": last,
		"queue":          d.queue.Metrics(),
	}
}

func (d *Dispatcher) consume(ctx context.Context, bus *workers.EventBus, eventType string, ch chan workers.Event) {
	defer bus.Unsubscribe(eventType, ch)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			d.heartbeat.Store(time.Now().UTC().Unix())
			d.handle(ctx, ev)
		}
	}
}

func (d *Dispatcher) handle(ctx context.Context, ev workers.Event) {
	triggers := busEventMapping(ev.Type)
	if len(triggers) == 0 {
		return
	}
	payload := map[string]any{
		"orderId":     ev.OrderID,
		"sourceEvent": ev.Type,
		"data":        ev.Payload,
		"timestamp":   time.Now().UTC(),
	}
	for _, trigger := range triggers {
		d.Emit(ctx, trigger, payload)
	}
}

// Emit enqueues an automation event for every active subscription listening
// for it; delivery (with retry/backoff) happens asynchronously on the
// RetryQueue. It is exported so other parts of the app (or the MCP server)
// can trigger events synthetically, e.g. for testing a Zap/scenario.
func (d *Dispatcher) Emit(ctx context.Context, event string, payload map[string]any) []map[string]any {
	subs, err := d.db.ListActiveWebhookSubscriptionsForEvent(ctx, event)
	if err != nil {
		slog.Warn("Webhooks: erro ao listar assinaturas", "event", event, "err", err)
		return nil
	}
	payload["event"] = event
	results := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		d.queue.Enqueue(sub, event, payload)
		results = append(results, map[string]any{"subscriptionId": sub.ID, "queued": true})
	}
	return results
}

// EmitSync delivers an automation event synchronously (single attempt, no
// retry/backoff) and returns the outcome per subscription. Used by the
// "trigger test webhook" tool/endpoint, where callers want immediate
// feedback rather than a fire-and-forget queue entry.
func (d *Dispatcher) EmitSync(ctx context.Context, event string, payload map[string]any) []map[string]any {
	subs, err := d.db.ListActiveWebhookSubscriptionsForEvent(ctx, event)
	if err != nil {
		slog.Warn("Webhooks: erro ao listar assinaturas", "event", event, "err", err)
		return nil
	}
	payload["event"] = event
	results := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		result := d.deliverOnce(ctx, sub, event, payload)
		_ = d.db.RecordWebhookDelivery(ctx, sub.ID, event, payload, result.StatusCode, result.OK, result.Error, 1)
		results = append(results, result.toMap())
	}
	return results
}

// ValidateTargetURL rejects webhook targets that could be used for SSRF:
// non-http(s) schemes, and hosts resolving to loopback, link-local, or
// private-range addresses. Call this both when a subscription is created
// and immediately before every delivery attempt (DNS can change between
// creation and send time).
func ValidateTargetURL(raw string) error {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("targetUrl inválida: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("targetUrl deve ser http ou https")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("targetUrl sem host")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("targetUrl não pode apontar para localhost")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("não foi possível resolver o host da targetUrl: %w", err)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("targetUrl aponta para um endereço interno/privado não permitido")
		}
	}
	return nil
}

func isDisallowedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	privateBlocks := []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "127.0.0.0/8", "fc00::/7", "::1/128",
		"100.64.0.0/10", // carrier-grade NAT
	}
	for _, block := range privateBlocks {
		_, cidr, err := net.ParseCIDR(block)
		if err == nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// IsKnownEvent reports whether event is one of the canonical triggers.
func IsKnownEvent(event string) bool {
	for _, e := range AllEvents() {
		if e == event {
			return true
		}
	}
	return false
}
