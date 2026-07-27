package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"payment-gateway/internal/webhooks"
)

func chainFXWebhookEvents() []string {
	return []string{"payment.created", "payment.completed", "payment.failed", "order.confirmed", "crypto.sent", "crypto.confirmed", "order.failed"}
}

func validChainFXEvent(event string) bool {
	for _, allowed := range chainFXWebhookEvents() {
		if event == allowed {
			return true
		}
	}
	return false
}

func chainFXEventStatus(event string) string {
	switch event {
	case "payment.created":
		return "created"
	case "payment.completed":
		return "paid"
	case "payment.failed", "order.failed":
		return "failed"
	case "order.confirmed", "crypto.confirmed":
		return "confirmed"
	case "crypto.sent":
		return "sent"
	default:
		return "unknown"
	}
}

func (s *Server) chainFXWebhookPayloadFromOrder(ctx context.Context, orderID, side, event, asset, amount string, sandbox bool) (string, map[string]any, bool) {
	side = strings.ToLower(strings.TrimSpace(side))
	if side == "" || side == "buy" {
		if buy, err := s.db.GetBuyOrder(ctx, orderID); err == nil && buy != nil {
			return "buy", map[string]any{
				"event":     event,
				"orderId":   buy.ID,
				"status":    chainFXEventStatus(event),
				"side":      "buy",
				"asset":     defaultString(asset, buy.Asset),
				"amount":    defaultString(amount, fmt.Sprintf("%.8f", buy.CryptoAmount)),
				"timestamp": time.Now().UTC(),
				"sandbox":   sandbox,
			}, true
		}
	}
	if side == "" || side == "sell" {
		if order, err := s.db.GetOrder(ctx, orderID); err == nil && order != nil {
			return "sell", map[string]any{
				"event":     event,
				"orderId":   order.ID,
				"status":    chainFXEventStatus(event),
				"side":      "sell",
				"asset":     defaultString(asset, order.Asset),
				"amount":    defaultString(amount, fmt.Sprintf("%.8f", order.AmountUSDT)),
				"timestamp": time.Now().UTC(),
				"sandbox":   sandbox,
			}, true
		}
	}
	return "", nil, false
}

func (s *Server) deliverChainFXWebhook(ctx context.Context, targetURL, event string, payload map[string]any) map[string]any {
	targetURL = strings.TrimSpace(targetURL)
	if err := validateWebhookDeliveryTarget(targetURL); err != nil {
		return map[string]any{"attempted": false, "ok": false, "error": "target URL rejected"}
	}
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, targetURL, bytes.NewReader(raw))
	if err != nil {
		return map[string]any{"attempted": false, "ok": false, "error": "invalid webhook request"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChainFX-Webhooks/1.0")
	req.Header.Set("X-ChainFX-Event", event)
	req.Header.Set("X-ChainFX-Signature", signChainFXWebhook(defaultString(s.cfg.WebhookSecret, s.cfg.PixWebhookSecret), raw))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return map[string]any{"attempted": true, "ok": false, "error": "webhook delivery failed"}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return map[string]any{
		"attempted":  true,
		"ok":         resp.StatusCode >= 200 && resp.StatusCode < 300,
		"statusCode": resp.StatusCode,
		"body":       string(body),
	}
}

func signChainFXWebhook(secret string, raw []byte) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func validateManualWebhookTarget(targetURL string) error {
	if err := validateWebhookDeliveryTarget(targetURL); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return err
	}
	if strings.ToLower(parsed.Scheme) != "https" {
		return fmt.Errorf("manual webhook target must use https")
	}
	return nil
}

func validateWebhookDeliveryTarget(targetURL string) error {
	if err := webhooks.ValidateTargetURL(targetURL); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return err
	}
	if strings.EqualFold(parsed.Hostname(), "metadata.google.internal") {
		return fmt.Errorf("cloud metadata host is not allowed")
	}
	return nil
}

func webhookSubscriptionAllowsEvent(events []string, event string) bool {
	for _, item := range events {
		if item == event {
			return true
		}
	}
	return false
}
