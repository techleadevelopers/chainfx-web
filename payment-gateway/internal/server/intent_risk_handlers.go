package server

// intent_risk_handlers.go — handlers for:
//   GET /app/intent/{id}        — M2M payment intent detail
//   GET /app/risk               — M2M risk / settlement operational dashboard
//   GET /mcp/capabilities.json  — public MCP capability registry (machine-readable)
//   GET /agent/v1/pricing/{wallet} — per-agent pricing policy lookup

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
)

// ─── Payment Intent Detail ─────────────────────────────────────────────────

func (s *Server) handleAppIntentDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	intent, err := s.db.GetAgentPaymentIntent(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if intent == nil {
		writeAPIError(w, r, http.StatusNotFound, "INTENT_NOT_FOUND", "Payment intent not found.")
		return
	}

	rate := s.workers.PriceWorker.GetPrice("BRL")
	feeLabel := fmt.Sprintf("%d bps (%.0f%%)", intent.FeeBps, float64(intent.FeeBps)/100)

	writeJSON(w, http.StatusOK, map[string]any{
		"intent": intent,
		"feeLayer": map[string]any{
			"feeBps":       intent.FeeBps,
			"feeLabel":     feeLabel,
			"grossUsdt":    fmt.Sprintf("%.6f", intent.GrossUSDT),
			"feeUsdt":      fmt.Sprintf("%.6f", intent.FeeUSDT),
			"requiredUsdt": fmt.Sprintf("%.6f", intent.RequiredUSDT),
			"usdtBrlRate":  fmt.Sprintf("%.4f", intent.USDTRate),
			"currentRate":  fmt.Sprintf("%.4f", rate),
		},
		"lifecycle": m2mIntentLifecycle(),
		"statusDoc": m2mStatusDocs(),
	})
}

// ─── Risk / Settlement Dashboard ──────────────────────────────────────────────

func (s *Server) handleAppRisk(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}

	stats, err := s.db.GetRiskDashboardStats(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}

	rate := s.workers.PriceWorker.GetPrice("BRL")
	dailyCap := s.cfg.M2MMaxDailyOutflowBRL

	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt": time.Now().UTC(),
		"usdtBrlRate": rate,
		"stats":       stats,
		"limits": map[string]any{
			"m2mDailyCapBrl":   dailyCap,
			"m2mPixFeeBps":     s.cfg.M2MPixFeeBps,
			"m2mCreditFeeBps":  s.cfg.M2MCreditFeeBps,
			"m2mDepositTolPct": s.cfg.M2MDepositTolerancePct,
		},
		"health": riskHealthSignal(stats, dailyCap),
		"note":   "Pending intents waiting for on-chain deposit. Expired = deposit window missed. Efi pending = PIX sent, awaiting bank confirmation.",
	})
}

// riskHealthSignal evaluates key signals and returns "ok" | "warning" | "degraded".
func riskHealthSignal(stats *database.RiskDashboardStats, dailyCap float64) string {
	if stats.FailedToday >= 5 {
		return "degraded"
	}
	if dailyCap > 0 && stats.DailyOutflowBRL/dailyCap >= 0.9 {
		return "warning"
	}
	if stats.PendingIntents > 50 {
		return "warning"
	}
	return "ok"
}

// ─── MCP Public Capability Registry ──────────────────────────────────────────

func (s *Server) handleMCPCapabilityRegistry(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "mcp-capabilities:"+base, time.Minute, func() (any, error) {
		caps, err := s.db.ListMarketplaceCapabilities(r.Context(), s.db.EmptyMarketplaceFilters())
		if err != nil {
			return nil, err
		}

		capDocs := make([]map[string]any, 0, len(caps))
		for _, c := range caps {
			doc := map[string]any{
				"id":          c.ID,
				"slug":        c.Slug,
				"displayName": c.DisplayName,
				"description": c.Description,
				"category":    c.Category,
				"routingMode": c.RoutingMode,
				"providers":   c.Providers,
				"contractUrl": base + "/marketplace/capabilities/" + c.ID + "/contract",
				"purchaseUrl": base + "/marketplace/capabilities/" + c.ID + "/purchase",
				"executeUrl":  base + "/agent/v1/capabilities/" + c.ID + "/execute",
			}
			capDocs = append(capDocs, doc)
		}

		return map[string]any{
			"version":      "1.0",
			"generatedAt":  time.Now().UTC(),
			"network":      "ChainFX Capability Network",
			"serverName":   "chainfx-mcp",
			"mcpEndpoint":  base + "/mcp/initialize",
			"capabilities": capDocs,
			"tools": []map[string]any{
				{
					"name":        "createPaymentIntent",
					"description": "Create an M2M PIX or credit_card payment intent funded by BSC USDT.",
					"endpoint":    base + "/mcp/tools/call",
					"rest":        base + "/agent/v1/pay",
					"inputs":      []string{"type", "amount_brl", "pix_key", "idempotency_key", "agent_wallet"},
				},
				{
					"name":        "getPaymentIntent",
					"description": "Read status for an M2M payment intent.",
					"endpoint":    base + "/mcp/tools/call",
					"rest":        base + "/agent/v1/pay/{id}",
					"inputs":      []string{"intent_id"},
				},
				{
					"name":        "listAgentPaymentIntents",
					"description": "List recent M2M payment intents for one agent wallet.",
					"endpoint":    base + "/mcp/tools/call",
					"inputs":      []string{"agentWallet", "status"},
				},
			},
			"agentPayments": map[string]any{
				"createUrl":      base + "/agent/v1/pay",
				"statusUrl":      base + "/agent/v1/pay/{id}",
				"types":          []string{"pix", "credit_card"},
				"fundingAsset":   "USDT",
				"fundingNetwork": "BSC",
				"lifecycle":      []string{"create_intent", "deposit_required_usdt", "match_deposit_by_address", "settle_recipient", "poll_status"},
			},
			"payment": map[string]any{
				"assets":   []string{"USDT", "USDC"},
				"networks": []string{"BSC"},
				"note":     "Pay on-chain → receive access grant → execute with quota metering",
			},
			"discovery": map[string]string{
				"openapi":   base + "/openapi.json",
				"llmsTxt":   base + "/llms.txt",
				"wellKnown": base + "/.well-known/ai-services.json",
			},
		}, nil
	})
}

// ─── Agent Pricing Policy endpoints ──────────────────────────────────────────

func (s *Server) handleGetAgentPricingPolicy(w http.ResponseWriter, r *http.Request) {
	wallet := strings.ToLower(strings.TrimSpace(r.PathValue("wallet")))
	env := strings.TrimSpace(r.URL.Query().Get("env"))
	if env == "" {
		env = "sandbox"
	}
	policy, err := s.db.GetAgentPricingPolicy(r.Context(), wallet, env)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"wallet": wallet,
		"env":    env,
		"policy": policy,
		"globals": map[string]any{
			"pixFeeBps":        s.cfg.M2MPixFeeBps,
			"creditCardFeeBps": s.cfg.M2MCreditFeeBps,
			"dailyCapBrl":      s.cfg.M2MMaxDailyOutflowBRL,
		},
	})
}

// ─── doc helpers ─────────────────────────────────────────────────────────────

func m2mIntentLifecycle() []map[string]string {
	return []map[string]string{
		{"status": "pending_deposit", "label": "Aguardando depósito USDT on-chain"},
		{"status": "paid_crypto", "label": "Depósito confirmado on-chain"},
		{"status": "settling", "label": "PIX sendo enviado via Efí"},
		{"status": "settled", "label": "PIX liquidado com sucesso"},
		{"status": "failed", "label": "Falha no settlement (Efí ou política)"},
		{"status": "expired", "label": "Janela de depósito expirada (15min)"},
	}
}

func m2mStatusDocs() map[string]string {
	return map[string]string{
		"pending_deposit": "Agent deve depositar required_usdt em USDT (BEP-20) para payment_address antes de expires_at.",
		"paid_crypto":     "Depósito detectado on-chain. Settlement PIX enfileirado.",
		"settling":        "Worker de M2M enviando PIX via Efí Bank. Aguardar confirmação.",
		"settled":         "PIX liquidado. efi_end_to_end_id disponível para rastreio.",
		"failed":          "Falha permanente ou temporária. Ver error_message.",
		"expired":         "Janela de 15min expirada sem depósito detectado. Criar nova intent.",
	}
}
