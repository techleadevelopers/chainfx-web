package server

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleChainFXOrder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order id is required"})
		return
	}
	token := customerAccessToken(r)
	if buy, ok := s.readChainFXBuy(r.Context(), id, token); ok {
		writeJSON(w, http.StatusOK, buy)
		return
	}
	if order, ok := s.readChainFXSell(r.Context(), id, token); ok {
		writeJSON(w, http.StatusOK, order)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "order not found"})
}

func (s *Server) handleChainFXWebhookTest(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	var req struct {
		Event     string `json:"event"`
		OrderID   string `json:"orderId"`
		Asset     string `json:"asset"`
		Amount    string `json:"amount"`
		TargetURL string `json:"targetUrl"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	event := defaultString(req.Event, "payment.completed")
	if !validChainFXEvent(event) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported event", "events": chainFXWebhookEvents()})
		return
	}
	payload := map[string]any{
		"event":     event,
		"orderId":   defaultString(req.OrderID, "ord_test_123"),
		"status":    chainFXEventStatus(event),
		"asset":     defaultString(req.Asset, "USDT"),
		"amount":    defaultString(req.Amount, "96.52"),
		"timestamp": time.Now().UTC(),
		"sandbox":   auth.Sandbox,
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"delivered":   false,
		"simulated":   true,
		"targetUrl":   req.TargetURL,
		"payload":     payload,
		"retryPolicy": "Phase 2: dashboard logs and webhook retry",
	})
}

func (s *Server) handleChainFXWebhookRetry(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.authorizeChainFX(w, r)
	if !ok {
		return
	}
	var req struct {
		Event          string `json:"event"`
		OrderID        string `json:"orderId"`
		Side           string `json:"side"`
		SubscriptionID string `json:"subscriptionId"`
		TargetURL      string `json:"targetUrl"`
		Asset          string `json:"asset"`
		Amount         string `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	req.OrderID = strings.TrimSpace(req.OrderID)
	if req.OrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "orderId is required"})
		return
	}
	event := defaultString(req.Event, "payment.completed")
	if !validChainFXEvent(event) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported event", "events": chainFXWebhookEvents()})
		return
	}
	source, payload, found := s.chainFXWebhookPayloadFromOrder(r.Context(), req.OrderID, req.Side, event, req.Asset, req.Amount, auth.Sandbox)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "order not found"})
		return
	}
	delivery := map[string]any{"attempted": false}
	targetURL := strings.TrimSpace(req.TargetURL)
	if req.SubscriptionID != "" {
		sub, err := s.webhookRegistry.Get(r.Context(), req.SubscriptionID)
		if err != nil || sub == nil {
			writeAPIError(w, r, http.StatusNotFound, "WEBHOOK_SUBSCRIPTION_NOT_FOUND", "Webhook subscription not found.")
			return
		}
		if !webhookSubscriptionAllowsEvent(sub.Events, event) {
			writeAPIError(w, r, http.StatusBadRequest, "WEBHOOK_EVENT_NOT_ALLOWED", "Webhook subscription is not configured for this event.")
			return
		}
		targetURL = sub.TargetURL
		delivery = s.deliverChainFXWebhook(r.Context(), sub.TargetURL, event, payload)
	} else if targetURL != "" {
		if s.cfg.IsProduction() {
			writeAPIError(w, r, http.StatusBadRequest, "WEBHOOK_SUBSCRIPTION_REQUIRED", "Use a registered webhook subscription for retry in production.")
			return
		}
		if err := validateManualWebhookTarget(targetURL); err != nil {
			writeAPIError(w, r, http.StatusBadRequest, "WEBHOOK_TARGET_REJECTED", "Webhook target URL is not allowed.")
			return
		}
		delivery = s.deliverChainFXWebhook(r.Context(), targetURL, event, payload)
	}
	logPayload := map[string]any{
		"requestId": requestID(r),
		"event":     event,
		"targetUrl": targetURL,
		"delivery":  delivery,
		"sandbox":   auth.Sandbox,
	}
	if source == "buy" {
		_ = s.db.AddBuyEvent(r.Context(), req.OrderID, "developer.webhook_retry", logPayload)
	} else {
		_ = s.db.AddEvent(r.Context(), req.OrderID, "developer.webhook_retry", logPayload)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"source":   source,
		"payload":  payload,
		"delivery": delivery,
	})
}
