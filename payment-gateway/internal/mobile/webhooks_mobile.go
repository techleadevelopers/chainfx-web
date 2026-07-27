package mobile

// webhooks_mobile.go — Phase 5: User-facing webhook subscription management
// Compatible with n8n, Zapier, Make and any HTTP webhook consumer.
//
//	POST   /api/mobile/webhooks/subscribe     — create a subscription
//	GET    /api/mobile/webhooks               — list my subscriptions
//	DELETE /api/mobile/webhooks/{id}          — delete a subscription
//	PUT    /api/mobile/webhooks/{id}/toggle   — enable / disable
//	GET    /api/mobile/webhooks/events        — available event types (public)

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"payment-gateway/internal/models"
)

// handleWebhookSubscribe — POST /api/mobile/webhooks/subscribe
func (s *Server) handleWebhookSubscribe(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		TargetURL string   `json:"target_url"`
		Events    []string `json:"events"`
	}
	if err := decodeJSON(r, &req); err != nil || req.TargetURL == "" || len(req.Events) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "target_url e events obrigatórios"})
		return
	}

	// Validate URL scheme (only https in production, allow http in dev)
	if !isAllowedWebhookURL(req.TargetURL) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "target_url deve usar HTTPS e não pode apontar para endereços internos",
		})
		return
	}

	// Validate event names
	validEvents := make(map[string]bool)
	for _, e := range models.Phase5WebhookEvents {
		validEvents[e] = true
	}
	for _, ev := range req.Events {
		if !validEvents[ev] {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "evento inválido: " + ev,
				"valid": models.Phase5WebhookEvents,
			})
			return
		}
	}

	secret := generateWebhookSecret()
	sub, err := mobileDB(s.db).CreateWebhookSubscription(r.Context(), uid, req.TargetURL, secret, req.Events)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"subscription": sub,
		"secret":       secret, // shown ONCE at creation only
		"hint":         "Guarde o secret — ele não será exibido novamente. Use-o para validar HMAC-SHA256 nos webhooks recebidos.",
	})
}

// handleListWebhooks — GET /api/mobile/webhooks
func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	subs, err := mobileDB(s.db).ListWebhooksByUser(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Never return the secret in list responses
	type safeWebhook struct {
		ID        string   `json:"id"`
		TargetURL string   `json:"target_url"`
		Events    []string `json:"events"`
		Active    bool     `json:"active"`
		CreatedAt any      `json:"created_at"`
	}
	out := make([]safeWebhook, 0, len(subs))
	for _, sub := range subs {
		out = append(out, safeWebhook{
			ID:        sub.ID,
			TargetURL: sub.TargetURL,
			Events:    sub.Events,
			Active:    sub.Active,
			CreatedAt: sub.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out, "count": len(out)})
}

// handleDeleteWebhook — DELETE /api/mobile/webhooks/{id}
func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	if err := mobileDB(s.db).DeleteWebhookSubscription(r.Context(), id, uid); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleToggleWebhook — PUT /api/mobile/webhooks/{id}/toggle
func (s *Server) handleToggleWebhook(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := r.PathValue("id")
	var req struct {
		Active bool `json:"active"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	if err := mobileDB(s.db).ToggleWebhookSubscription(r.Context(), id, uid, req.Active); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": req.Active})
}

// handleListWebhookEvents — GET /api/mobile/webhooks/events (public)
func (s *Server) handleListWebhookEvents(w http.ResponseWriter, _ *http.Request) {
	type eventDoc struct {
		Event       string `json:"event"`
		Description string `json:"description"`
	}
	docs := []eventDoc{
		{"order.created", "Nova ordem de compra ou venda criada"},
		{"order.completed", "Ordem concluída com sucesso"},
		{"order.failed", "Ordem falhou"},
		{"payment.received", "Pagamento PIX confirmado pelo banco"},
		{"payout.sent", "PIX de saída enviado ao usuário"},
		{"price.change", "Variação significativa de preço"},
		{"dca.executed", "Compra automática DCA executada"},
		{"swap.completed", "Swap entre criptos concluído"},
		{"swap.failed", "Swap falhou"},
		{"kyc.approved", "KYC aprovado — limites aumentados"},
		{"kyc.rejected", "KYC rejeitado"},
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": docs})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func generateWebhookSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "whsec_" + hex.EncodeToString(b)
}

// isAllowedWebhookURL enforces SSRF policy: target must be HTTPS and not
// point to private/loopback addresses. See chainfx-webhooks-ssrf.md.
func isAllowedWebhookURL(rawURL string) bool {
	return validateWebhookTargetURL(rawURL) == nil
}
