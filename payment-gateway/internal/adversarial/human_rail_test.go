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
// LAYER 1 — HUMAN RAIL (Pix / Efí)
//
// Attack: fire the exact same signed Pix settlement webhook at
// POST /api/pix/webhook/buy from 40 goroutines, released simultaneously.
// A real fiduciary system must apply the settlement exactly once — a
// second acceptance means a buy order could be credited/paid out twice
// from a single incoming Pix payment.
// ---------------------------------------------------------------------

func attackDuplicateWebhook(t *testing.T, h *harness, buyID, providerID string) (successes, blockedOrDuplicate, other int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"buyId":      buyID,
		"status":     "CONCLUIDA",
		"providerId": providerID,
	})
	sig := signHMAC(testSecret, body)

	const workers = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	trigger := make(chan struct{})
	client := &http.Client{Timeout: 5 * time.Second}
	url := h.Server.URL + "/api/pix/webhook/buy"

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-trigger // release every goroutine at once — maximise the race window

			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-efi-signature", sig)

			resp, err := client.Do(req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				other++
				return
			}
			defer resp.Body.Close()
			var parsed struct {
				OK        bool `json:"ok"`
				Duplicate bool `json:"duplicate"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&parsed)
			switch {
			case resp.StatusCode == http.StatusOK && parsed.OK && !parsed.Duplicate:
				successes++
			case resp.StatusCode == http.StatusOK && parsed.Duplicate:
				blockedOrDuplicate++
			default:
				other++
			}
		}()
	}
	close(trigger)
	wg.Wait()
	return successes, blockedOrDuplicate, other
}

func TestHumanRail_ConcurrentDuplicateWebhook_NeverDoubleSettles(t *testing.T) {
	h := newHarness(t)
	buyID := seedBuyOrder(t, h.DB)

	successes, duplicates, other := attackDuplicateWebhook(t, h, buyID, "E00000000202607121200"+buyID[:8])
	t.Logf("[Human Rail Attack] sucesso(settlement aplicado)=%d duplicado(bloqueado)=%d outro=%d", successes, duplicates, other)

	if successes != 1 {
		t.Fatalf("🚨 FALHA DE CUSTÓDIA: a mesma notificação Pix foi aplicada como settlement %d vez(es); esperado exatamente 1", successes)
	}

	// Assert directly against the ledger, not just the HTTP responses: the
	// unique index on (buy_order_id, providerId) must have let exactly one
	// webhook.provider event through.
	var eventCount int
	if err := h.DB.SQL.QueryRow(
		`SELECT count(*) FROM buy_order_events WHERE buy_order_id = $1 AND type = 'webhook.provider'`,
		buyID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("falha ao verificar buy_order_events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("🚨 FALHA DE CUSTÓDIA: buy_order_events tem %d linhas webhook.provider para o mesmo providerId; esperado 1 (dedup via índice único)", eventCount)
	}
}

// TestHumanRail_InvalidSignature_NeverSettles attacks the HMAC gate itself:
// an attacker who doesn't know PIX_WEBHOOK_SECRET must never be able to
// forge a settlement, no matter how the payload is shaped.
func TestHumanRail_InvalidSignature_NeverSettles(t *testing.T) {
	h := newHarness(t)
	buyID := seedBuyOrder(t, h.DB)

	forged := []struct {
		name string
		sig  string
	}{
		{"empty signature", ""},
		{"garbage signature", "fake_calculated_signature_attempt"},
		{"signature for different body", signHMAC(testSecret, []byte(`{"buyId":"other"}`))},
		{"zero-value hmac", "0000000000000000000000000000000000000000000000000000000000000"},
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := h.Server.URL + "/api/pix/webhook/buy"

	for _, f := range forged {
		t.Run(f.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"buyId":      buyID,
				"status":     "CONCLUIDA",
				"providerId": fmt.Sprintf("forged-%s", f.name),
			})
			req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if f.sig != "" {
				req.Header.Set("x-efi-signature", f.sig)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("🚨 BRECHA: assinatura forjada (%s) recebeu status %d, esperado 401", f.name, resp.StatusCode)
			}
		})
	}

	var eventCount int
	if err := h.DB.SQL.QueryRow(
		`SELECT count(*) FROM buy_order_events WHERE buy_order_id = $1 AND type = 'webhook.provider'`,
		buyID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("falha ao verificar buy_order_events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("🚨 FALHA DE CUSTÓDIA: webhook sem assinatura válida chegou a gravar %d evento(s) de settlement", eventCount)
	}
}
