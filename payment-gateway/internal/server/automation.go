package server

import (
	"net/http"
	"strings"

	"payment-gateway/internal/webhooks"
)

// ── Fase 4.2: OpenAI Agents Integration ─────────────────────────────────

// requireAgentsConfigured gates every /api/agents/* handler: it requires an
// authenticated admin/API-key caller (these calls cost real OpenAI spend and
// must not be publicly abusable), applies a per-client rate limit, and
// checks that OPENAI_API_KEY is actually set.
func (s *Server) requireAgentsConfigured(w http.ResponseWriter, r *http.Request) bool {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return false
	}
	if !s.limiter.Allow("agents:" + clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "limite de chamadas de IA excedido"})
		return false
	}
	if !s.agents.Configured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "OPENAI_API_KEY não configurado"})
		return false
	}
	return true
}

func (s *Server) currentMarketData() map[string]any {
	price := s.workers.PriceWorker.GetPrice("BRL")
	return map[string]any{
		"USDT_BRL":      price,
		"SELL_USDT_BRL": s.sellRate(price),
		"USDT_USD":      s.workers.PriceWorker.GetPrice("USD"),
		"USDT_EUR":      s.workers.PriceWorker.GetPrice("EUR"),
		"BTC_USDT":      s.workers.PriceWorker.GetPrice("BTCUSDT"),
		"EUR_USD":       s.workers.PriceWorker.GetPrice("EURUSD"),
	}
}

func (s *Server) handleAgentMarketAnalysis(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentsConfigured(w, r) {
		return
	}
	analysis, err := s.agents.AnalyzeMarket(r.Context(), s.currentMarketData())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, analysis)
}

func (s *Server) handleAgentRecommend(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentsConfigured(w, r) {
		return
	}
	var req map[string]any
	_ = decodeJSON(r, &req)
	tradeContext := s.currentMarketData()
	for k, v := range req {
		tradeContext[k] = v
	}
	rec, err := s.agents.Recommend(r.Context(), tradeContext)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleAgentAnomalies(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentsConfigured(w, r) {
		return
	}
	var req struct {
		Transactions []map[string]any `json:"transactions"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Transactions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "transactions é obrigatório"})
		return
	}
	report, err := s.agents.DetectAnomalies(r.Context(), req.Transactions)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleAgentPredict(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentsConfigured(w, r) {
		return
	}
	var req struct {
		History []map[string]any `json:"history"`
		Horizon string           `json:"horizon"`
	}
	_ = decodeJSON(r, &req)
	if req.Horizon == "" {
		req.Horizon = "24h"
	}
	if len(req.History) == 0 {
		req.History = []map[string]any{s.currentMarketData()}
	}
	prediction, err := s.agents.PredictPrice(r.Context(), req.History, req.Horizon)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, prediction)
}

func (s *Server) handleAgentSummary(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgentsConfigured(w, r) {
		return
	}
	var req struct {
		Transactions []map[string]any `json:"transactions"`
		Period       string           `json:"period"`
	}
	if err := decodeJSON(r, &req); err != nil || len(req.Transactions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "transactions é obrigatório"})
		return
	}
	summary, err := s.agents.SummarizeTransactions(r.Context(), req.Transactions, req.Period)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// ── Fase 4.3: n8n / Zapier / Make webhook automation ────────────────────

func (s *Server) handleListWebhookEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"events": webhooks.AllEvents()})
}

type createWebhookSubscriptionRequest struct {
	Provider    string   `json:"provider"`
	TargetURL   string   `json:"targetUrl"`
	Secret      string   `json:"secret"`
	Description string   `json:"description"`
	Events      []string `json:"events"`
}

func (s *Server) handleCreateWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	var req createWebhookSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido"})
		return
	}
	req.TargetURL = strings.TrimSpace(req.TargetURL)
	sub, err := s.webhookRegistry.Create(r.Context(), req.Provider, req.TargetURL, req.Secret, req.Description, req.Events)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "validEvents": webhooks.AllEvents()})
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (s *Server) handleListWebhookSubscriptions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	subs, err := s.webhookRegistry.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "falha ao listar assinaturas"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func (s *Server) handleDeleteWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	if err := s.webhookRegistry.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "falha ao remover assinatura"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleSetWebhookSubscriptionActive(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	var req struct {
		Active bool `json:"active"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido"})
		return
	}
	id := r.PathValue("id")
	if err := s.webhookRegistry.SetActive(r.Context(), id, req.Active); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "falha ao atualizar assinatura"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": req.Active})
}

func (s *Server) handleTestWebhookSubscription(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	sub, err := s.webhookRegistry.Get(r.Context(), id)
	if err != nil || sub == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "assinatura não encontrada"})
		return
	}
	event := "order.created"
	if len(sub.Events) > 0 {
		event = sub.Events[0]
	}
	results := s.webhooks.EmitSync(r.Context(), event, map[string]any{"test": true, "subscriptionId": sub.ID})
	writeJSON(w, http.StatusOK, map[string]any{"event": event, "results": results})
}

// ── Fase 4.4: Webhook Retry System — logs & dashboard ───────────────────

func (s *Server) handleWebhookSubscriptionLogs(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	deliveries, err := s.webhookLogs.ForSubscription(r.Context(), id, 100)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "falha ao listar entregas"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": deliveries})
}

func (s *Server) handleWebhookDashboard(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authorizeAdmin(w, r); !ok {
		return
	}
	summary, err := s.webhookDashboard.Summary(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "falha ao montar dashboard"})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
