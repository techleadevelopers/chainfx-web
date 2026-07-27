package adversarial

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// LAYER 2 — MACHINE LAYER (MCP / AI agents)
//
// Attack: simulate a compromised or malicious AI agent flooding
// POST /mcp/tools/call with no API key (the cheapest possible attacker
// tier — 5 calls/min). A real capability network must not let context
// flooding / brute infra abuse bypass its per-key sliding-window limiter,
// and must not let an oversized "context stuffing" payload bypass or
// crash the handler either.
// ---------------------------------------------------------------------

func TestMCPLayer_AnonymousFlood_RateLimiterBlocksExcessCalls(t *testing.T) {
	h := newHarness(t)

	const spamRequests = 30 // well above the 5/min anonymous tier
	var wg sync.WaitGroup
	var mu sync.Mutex
	var allowed, blocked, other int
	trigger := make(chan struct{})
	client := &http.Client{Timeout: 5 * time.Second}
	url := h.Server.URL + "/mcp/tools/call"

	// 50KB of structured "bloat" per call, simulating an agent trying to
	// exhaust context/parsing budget on every request, not just volume.
	payload, _ := json.Marshal(map[string]any{
		"name": "does_not_exist_probe",
		"arguments": map[string]any{
			"bloat_padding": string(make([]byte, 50_000)),
		},
	})

	for i := 0; i < spamRequests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-trigger

			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			// No API key: this is the anonymous / cheapest attacker tier.
			// A distinct idempotency-style header per request, matching the
			// pattern a real attacker would use to try to dodge a naive
			// per-request-hash limiter (the real limiter keys on client IP,
			// not on request contents, so this must not help).
			req.Header.Set("X-Idempotency-Key", fmt.Sprintf("mcp_flood_%d", id))

			resp, err := client.Do(req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				other++
				return
			}
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusTooManyRequests:
				blocked++
			case http.StatusOK:
				allowed++
			default:
				other++
			}
		}(i)
	}
	close(trigger)
	wg.Wait()

	t.Logf("[Machine Layer Attack] permitidas=%d bloqueadas(429)=%d outro=%d de %d disparos anônimos", allowed, blocked, other, spamRequests)

	// The anonymous tier is 5/min; all 30 requests land in the same window
	// bucketed by client IP (loopback, shared across the attack), so no
	// more than 5 should ever be let through regardless of send order.
	if allowed > 5 {
		t.Fatalf("🚨 BRECHA DE SEGURANÇA: %d requisições anônimas foram permitidas; o tier anônimo permite no máximo 5/min", allowed)
	}
	if blocked < spamRequests-5 {
		t.Fatalf("🚨 BRECHA DE SEGURANÇA: rate limiter do MCP deixou passar volume acima do esperado (bloqueadas=%d, esperado >= %d)", blocked, spamRequests-5)
	}
}

// TestMCPLayer_MalformedToolCall_NeverPanics attacks the JSON decoder and
// tool dispatcher directly with adversarial/malformed bodies. A capability
// network exposed to autonomous agents must degrade to a clean 4xx/JSON
// error for garbage input, never crash the process or hang the connection.
func TestMCPLayer_MalformedToolCall_NeverPanics(t *testing.T) {
	h := newHarness(t)
	client := &http.Client{Timeout: 5 * time.Second}
	url := h.Server.URL + "/mcp/tools/call"

	adversarialBodies := []struct {
		name string
		body []byte
	}{
		{"empty body", []byte(``)},
		{"truncated json", []byte(`{"name": "list_webhook_subscriptions", "argum`)},
		{"deeply nested arguments", mustJSON(map[string]any{"name": "x", "arguments": nestedMap(500)})},
		{"non-object arguments type confusion", []byte(`{"name":"x","arguments":"not-an-object"}`)},
		{"null name", []byte(`{"name":null,"arguments":{}}`)},
		{"unicode/control-char flood", mustJSON(map[string]any{"name": "\x00\x01\x02" + string(make([]byte, 1000)), "arguments": map[string]any{}})},
	}

	for _, tc := range adversarialBodies {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("🚨 servidor não respondeu a payload adversarial %q (conexão derrubada?): %v", tc.name, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 500 {
				t.Fatalf("🚨 payload adversarial %q causou erro 5xx (%d) em vez de uma rejeição limpa", tc.name, resp.StatusCode)
			}
		})
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// nestedMap builds a pathologically deep JSON object to probe for
// stack-exhaustion / unbounded-recursion issues in the decoder or dispatcher.
func nestedMap(depth int) map[string]any {
	m := map[string]any{}
	cur := m
	for i := 0; i < depth; i++ {
		next := map[string]any{}
		cur["nested"] = next
		cur = next
	}
	return m
}
