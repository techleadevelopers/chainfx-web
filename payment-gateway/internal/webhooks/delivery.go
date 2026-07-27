package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

// DeliveryResult is the outcome of a single HTTP delivery attempt.
type DeliveryResult struct {
	SubscriptionID string `json:"subscriptionId"`
	Attempted      bool   `json:"attempted"`
	OK             bool   `json:"ok"`
	StatusCode     int    `json:"statusCode,omitempty"`
	Body           string `json:"body,omitempty"`
	Error          string `json:"error,omitempty"`
}

func (r DeliveryResult) toMap() map[string]any {
	m := map[string]any{
		"subscriptionId": r.SubscriptionID,
		"attempted":      r.Attempted,
		"ok":             r.OK,
	}
	if r.StatusCode != 0 {
		m["statusCode"] = r.StatusCode
	}
	if r.Body != "" {
		m["body"] = r.Body
	}
	if r.Error != "" {
		m["error"] = r.Error
	}
	return m
}

// deliverOnce performs a single signed HTTP POST to sub.TargetURL. It
// re-validates the target URL immediately before connecting, since DNS can
// change between subscription creation and delivery (rebinding defense).
func (d *Dispatcher) deliverOnce(ctx context.Context, sub *database.WebhookSubscription, event string, payload map[string]any) DeliveryResult {
	if err := ValidateTargetURL(sub.TargetURL); err != nil {
		return DeliveryResult{SubscriptionID: sub.ID, Attempted: false, OK: false, Error: err.Error()}
	}
	raw, _ := json.Marshal(payload)
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, sub.TargetURL, bytes.NewReader(raw))
	if err != nil {
		return DeliveryResult{SubscriptionID: sub.ID, Attempted: false, OK: false, Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ChainFX-Automation/1.0")
	req.Header.Set("X-ChainFX-Event", event)
	req.Header.Set("X-ChainFX-Provider", providerHeaderName(sub.Provider))
	if sig := sign(sub.Secret, raw); sig != "" {
		req.Header.Set("X-ChainFX-Signature", sig)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return DeliveryResult{SubscriptionID: sub.ID, Attempted: true, OK: false, Error: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	result := DeliveryResult{
		SubscriptionID: sub.ID,
		Attempted:      true,
		OK:             ok,
		StatusCode:     resp.StatusCode,
		Body:           string(body),
	}
	if !ok {
		result.Error = "unexpected status code"
	}
	return result
}

func providerHeaderName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ProviderN8N:
		return "n8n"
	case ProviderZapier:
		return "zapier"
	case ProviderMake:
		return "make"
	default:
		return "generic"
	}
}

func sign(secret string, raw []byte) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
