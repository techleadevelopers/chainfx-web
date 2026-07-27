package e2e_test

import "testing"

func TestWebhookRetryRejectsArbitraryURL(t *testing.T) {
	c := newE2EClient(t)
	resp := c.post("/webhooks/retry", map[string]any{
		"targetUrl": "http://127.0.0.1:1/internal",
		"event":     "buy.created",
	}, idempotencyKey("webhook-retry-ssrf"))
	requireStatus(t, resp, 400, 403)
}
