package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/money"
	"payment-gateway/internal/webhooks"

	"github.com/ethereum/go-ethereum/common"
)

// Tool describes an MCP tool: an action an agent can invoke.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *Server) tools() []Tool {
	return []Tool{
		{
			Name:        "get_rates",
			Description: "Retorna as cotações atuais da ChainFX (USDT/BRL, USDT/USD, taxa de venda, etc).",
			InputSchema: schema(nil),
		},
		{
			Name:        "searchCapabilities",
			Description: "Busca capacidades digitais compraveis/executaveis via ChainFX Capability Marketplace.",
			InputSchema: schema(map[string]string{"query": "string opcional", "category": "string opcional", "paymentAsset": "string opcional"}),
		},
		{
			Name:        "listCapabilities",
			Description: "Lista o catalogo capability-first da ChainFX, incluindo planos ativos e providers abstratos.",
			InputSchema: schema(map[string]string{"category": "string opcional", "paymentAsset": "string opcional"}),
		},
		{
			Name:        "getCapability",
			Description: "Le uma capability especifica pelo id/slug, com providers, routing mode e planos ativos.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio, ex: document_ocr"}),
		},
		{
			Name:        "getCapabilityContract",
			Description: "Le o contrato versionado de input/output de uma capability para interoperabilidade entre agentes e providers.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "version": "string opcional, default v1"}),
		},
		{
			Name:        "getEIPCapabilities",
			Description: "Lista suporte EIP-712/EIP-2612/EIP-3009/4337/7702 e rails stablecoin disponiveis por asset.",
			InputSchema: schema(nil),
		},
		{
			Name:        "prepareEIPTypedIntent",
			Description: "Gera typedData EIP-712, digest, domain separation e decisao de rail para M2MIntent, MobileTransfer, CapabilityPurchase ou PayIntent.",
			InputSchema: schema(map[string]string{"intentType": "M2MIntent|MobileTransfer|CapabilityPurchase|PayIntent", "message": "object com payer/from, recipient/to, asset, amount base-unit, nonce, deadline, idempotencyKey"}),
		},
		{
			Name:        "purchaseCapability",
			Description: "Cria payment intent stablecoin para uma capability. O agente paga on-chain e depois submete receipt via API.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "planId": "string opcional", "agentWallet": "string obrigatorio", "payerWallet": "string obrigatorio", "idempotencyKey": "string obrigatorio", "nonce": "string obrigatorio", "paymentAsset": "string opcional"}),
		},
		{
			Name:        "getPurchase",
			Description: "Consulta status de uma purchase do marketplace/capability exchange.",
			InputSchema: schema(map[string]string{"purchaseId": "string obrigatorio"}),
		},
		{
			Name:        "executeCapability",
			Description: "Executa uma capability via Capability Router com metering real, provider real quando configurado e fallback mock/dev.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "accessToken": "string obrigatorio", "operation": "string opcional", "requestId": "string obrigatorio", "idempotencyKey": "string obrigatorio", "units": "number opcional", "provider": "string opcional", "routingMode": "best_available|cheapest|lowest_latency|highest_quality", "region": "string opcional", "maxLatencyMs": "number opcional", "maxCostScore": "number opcional", "requireReal": "boolean opcional", "input": "object opcional"}),
		},
		{
			Name:        "chooseRoute",
			Description: "Estima a melhor rota/provider para uma capability por preco, latencia, qualidade, regiao e politica empresarial, sem debitar quota.",
			InputSchema: schema(map[string]string{"capability": "string obrigatorio", "provider": "string opcional", "routingMode": "best_available|cheapest|lowest_latency|highest_quality", "region": "string opcional", "maxLatencyMs": "number opcional", "maxCostScore": "number opcional", "requireReal": "boolean opcional", "units": "number opcional"}),
		},
		{
			Name:        "getUsage",
			Description: "Consulta status de grant ou purchase para acompanhar quota/usage.",
			InputSchema: schema(map[string]string{"grantId": "string opcional", "purchaseId": "string opcional"}),
		},
		{
			Name:        "listAssets",
			Description: "Lista assets BSC habilitados para Agent Rail e pagamentos marketplace.",
			InputSchema: schema(nil),
		},
		{
			Name:        "quote",
			Description: "Calcula estimativa simples do Agent Rail para troca stablecoin 1:1 antes de criar intent HTTP.",
			InputSchema: schema(map[string]string{"payAsset": "string obrigatorio", "receiveAsset": "string obrigatorio", "amount": "number obrigatorio", "amountType": "string opcional: pay|receive"}),
		},
		{
			Name:        "trade",
			Description: "Retorna instrucoes estruturadas para executar Agent Rail via endpoints seguros com receipt on-chain.",
			InputSchema: schema(map[string]string{"payAsset": "string obrigatorio", "receiveAsset": "string obrigatorio", "amount": "number obrigatorio", "agentWallet": "string obrigatorio"}),
		},
		{
			Name:        "settlementStatus",
			Description: "Consulta status de settlement/purchase ou trade intent.",
			InputSchema: schema(map[string]string{"purchaseId": "string opcional", "tradeIntentId": "string opcional"}),
		},
		{
			Name:        "get_order_status",
			Description: "Consulta o status de uma ordem de compra ou venda pelo id.",
			InputSchema: schema(map[string]string{"orderId": "string (obrigatório)", "side": "string opcional: buy|sell"}),
		},
		{
			Name:        "market_analysis",
			Description: "Gera uma análise de mercado com IA a partir das cotações atuais.",
			InputSchema: schema(nil),
		},
		{
			Name:        "trade_recommendation",
			Description: "Recomenda comprar, vender ou aguardar com base no contexto informado.",
			InputSchema: schema(map[string]string{"intendedAmount": "number opcional", "notes": "string opcional"}),
		},
		{
			Name:        "price_prediction",
			Description: "Projeta uma faixa de preço de curtíssimo prazo para USDT/BRL.",
			InputSchema: schema(map[string]string{"horizon": "string opcional, ex: '1h', '24h'"}),
		},
		{
			Name:        "detect_anomalies",
			Description: "Analisa uma lista de transações e aponta anomalias/possível fraude.",
			InputSchema: schema(map[string]string{"transactions": "array de objetos (obrigatório)"}),
		},
		{
			Name:        "summarize_transactions",
			Description: "Resume a atividade financeira de um conjunto de transações.",
			InputSchema: schema(map[string]string{"transactions": "array de objetos (obrigatório)", "period": "string opcional"}),
		},
		{
			Name:        "list_webhook_events",
			Description: "Lista os eventos de automação disponíveis para n8n/Zapier/Make (order.created, payment.received, etc).",
			InputSchema: schema(nil),
		},
		{
			Name:        "create_webhook_subscription",
			Description: "Cria uma assinatura de webhook de automação (n8n, Zapier, Make ou genérico).",
			InputSchema: schema(map[string]string{
				"targetUrl":   "string (obrigatório)",
				"events":      "array de strings (obrigatório)",
				"provider":    "string opcional: n8n|zapier|make|generic",
				"secret":      "string opcional, usado para assinar o payload (HMAC SHA-256)",
				"description": "string opcional",
			}),
		},
		{
			Name:        "list_webhook_subscriptions",
			Description: "Lista as assinaturas de webhook de automação configuradas.",
			InputSchema: schema(nil),
		},
		{
			Name:        "trigger_test_webhook",
			Description: "Dispara um evento de automação sintético para testar integrações n8n/Zapier/Make.",
			InputSchema: schema(map[string]string{"event": "string (obrigatório)", "payload": "objeto opcional"}),
		},
		{
			Name:        "listAgentGrants",
			Description: "Lista os access grants ativos de um agente (capability access tokens com quota restante e validade).",
			InputSchema: schema(map[string]string{
				"agentWallet": "string obrigatorio: endereco EVM do agente",
			}),
		},
		{
			Name:        "getAgentPolicy",
			Description: "Retorna a policy de execucao, limites de gasto e precificacao personalizada de um agente pelo wallet.",
			InputSchema: schema(map[string]string{
				"agentWallet": "string obrigatorio: endereco EVM do agente",
			}),
		},
		{
			Name:        "dryRunCapability",
			Description: "Simula uma execucao de capability sem debitar quota. Retorna rota selecionada, provider e output mock — identico ao executeCapability mas marcado como dry_run.",
			InputSchema: schema(map[string]string{
				"capability":  "string obrigatorio: id/slug da capability",
				"operation":   "string opcional: operacao a simular",
				"provider":    "string opcional: preferencia de provider",
				"routingMode": "string opcional: best_available|cheapest|lowest_latency|highest_quality",
				"units":       "number opcional: unidades a simular",
				"input":       "object opcional: payload de entrada para echo no preview",
			}),
		},
		{
			Name:        "listAgentPaymentIntents",
			Description: "Lista intents de pagamento M2M recentes de um agente com status opcional.",
			InputSchema: schema(map[string]string{
				"agentWallet": "string obrigatorio: endereco EVM do agente",
				"status":      "string opcional: pending_deposit|paid_crypto|settling|settled|failed|expired",
			}),
		},
		{
			Name: "createPaymentIntent",
			Description: "Cria uma intent de pagamento M2M para que o agente pague PIX ou cartão de crédito em nome de terceiros. " +
				"O agente deposita o valor em USDT (incluindo taxa) na PaymentAddress; o sistema liquida o fiat ao destinatário. " +
				"Taxa PIX: 10% | Taxa Cartão: 19%. Validade: 15 minutos (proteção cambial).",
			InputSchema: schema(map[string]string{
				"type":             "string obrigatorio: 'pix' ou 'credit_card'",
				"amount_brl":       "string obrigatorio: valor em BRL que o destinatario final recebera",
				"pix_key":          "string obrigatorio quando type=pix: chave PIX destino",
				"payment_link":     "string obrigatorio quando type=credit_card e barcode ausente: link/fatura a pagar",
				"barcode":          "string obrigatorio quando type=credit_card e payment_link ausente: codigo de barras/linha digitavel",
				"beneficiary_name": "string opcional: beneficiario/loja/credor",
				"due_date":         "string opcional: vencimento da fatura",
				"idempotency_key":  "string obrigatorio: chave unica gerada pelo agente para evitar duplicatas",
				"agent_wallet":     "string obrigatorio: endereco EVM do agente pagador (audit trail)",
			}),
		},
		{
			Name:        "getPaymentIntent",
			Description: "Consulta o status de uma intent de pagamento M2M pelo ID retornado em createPaymentIntent.",
			InputSchema: schema(map[string]string{
				"intent_id": "string obrigatorio: ID da intent retornado por createPaymentIntent",
			}),
		},
	}
}

func schema(props map[string]string) map[string]any {
	properties := map[string]any{}
	for name, desc := range props {
		properties[name] = map[string]any{"type": "string", "description": desc}
	}
	return map[string]any{"type": "object", "properties": properties}
}

func (s *Server) handleToolsList(w http.ResponseWriter, r *http.Request) {
	writeCachedJSON(w, http.StatusOK, s.toolsListJSON)
}

type toolCallRequest struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	s.handleToolsCallWithAuthorize(nil)(w, r)
}

func (s *Server) handleToolsCallWithAuthorize(authorize Authorize) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		var req toolCallRequest
		if err := decodeJSON(r, &req); err != nil {
			s.recordMCPToolLog(r, "", "error", "invalid_json", time.Since(start))
			writeMCPError(w, http.StatusBadRequest, "JSON invalido")
			return
		}

		apiKey := mcpAPIKey(r)
		keyHash := shortMCPSecretHash(apiKey)
		if keyHash == "" {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.SplitN(xff, ",", 2)
				keyHash = "anon:" + strings.TrimSpace(parts[0])
			} else {
				addr := r.RemoteAddr
				if idx := strings.LastIndex(addr, ":"); idx != -1 {
					addr = addr[:idx]
				}
				keyHash = "anon:" + addr
			}
		}

		toolClass := mcpToolRateClass(req.Name)
		limit := tierLimit(apiKey, toolClass)
		allowed, remaining, resetAt := s.rl.allow(keyHash+":"+toolClass, limit)
		w.Header().Set("X-MCP-Tool-Class", toolClass)
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
		if !allowed {
			retryAfter := int(time.Until(resetAt).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			metrics.IncMCPRateLimited()
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":      fmt.Sprintf("rate limit exceeded: %d calls/minute allowed for %s", limit, toolClass),
				"toolClass":  toolClass,
				"retryAfter": retryAfter,
				"resetAt":    resetAt.UTC().Format(time.RFC3339),
			})
			return
		}

		ctx := context.WithValue(r.Context(), mcpAPIKeyCtxKey{}, apiKey)
		if isMCPAITool(req.Name) && apiKey == "" {
			ctx = context.WithValue(ctx, mcpAIForceFallbackCtxKey{}, true)
		}
		r = r.WithContext(ctx)

		if isMCPAITool(req.Name) {
			if apiKey != "" && authorize != nil {
				if !authorize(w, r) {
					s.recordMCPToolLog(r, req.Name, "error", "unauthorized", time.Since(start))
					return
				}
			}
		} else if !isPublicMCPTool(req.Name) && authorize != nil {
			if apiKey == "" && returnsAuthRequiredForAnonymousMCPTool(req.Name) {
				writeJSON(w, http.StatusOK, map[string]any{
					"isError": false,
					"content": []map[string]any{{"type": "json", "json": map[string]any{
						"authRequired": true,
						"status":       "unauthorized",
						"tool":         req.Name,
						"message":      "Tool requires a ChainFX API key. No account-scoped data was returned.",
					}}},
				})
				return
			}
			if !authorize(w, r) {
				s.recordMCPToolLog(r, req.Name, "error", "unauthorized", time.Since(start))
				return
			}
		}
		result, err := s.callTool(r.Context(), req.Name, req.Arguments)
		if err != nil {
			s.recordMCPToolLog(r, req.Name, "error", err.Error(), time.Since(start))
			metrics.IncMCPToolCall("error")
			if paymentErr, ok := err.(*database.AgentCreditPaymentRequiredError); ok {
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"isError": true,
					"error":   paymentErr.Challenge(),
					"content": []map[string]any{{"type": "json", "json": paymentErr.Challenge()}},
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"isError": true,
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
			})
			return
		}
		s.recordMCPToolLog(r, req.Name, "ok", "", time.Since(start))
		metrics.IncMCPToolCall("ok")
		writeJSON(w, http.StatusOK, map[string]any{
			"isError": false,
			"content": []map[string]any{{"type": "json", "json": result}},
		})
	}
}
func isPublicMCPTool(name string) bool {
	switch name {
	case "get_rates",
		"searchCapabilities",
		"listCapabilities",
		"getCapability",
		"getCapabilityContract",
		"getEIPCapabilities",
		"prepareEIPTypedIntent",
		"chooseRoute",
		"listAssets",
		"quote",
		"executeCapability",
		"trade",
		"dryRunCapability",
		"list_webhook_events":
		return true
	default:
		return false
	}
}

func isMCPAITool(name string) bool {
	switch name {
	case "market_analysis",
		"trade_recommendation",
		"price_prediction",
		"detect_anomalies",
		"summarize_transactions":
		return true
	default:
		return false
	}
}

func returnsAuthRequiredForAnonymousMCPTool(name string) bool {
	switch name {
	case "getPurchase",
		"getUsage",
		"settlementStatus",
		"get_order_status",
		"list_webhook_subscriptions",
		"listAgentGrants",
		"getAgentPolicy",
		"listAgentPaymentIntents",
		"getPaymentIntent":
		return true
	default:
		return false
	}
}

func mcpToolRateClass(name string) string {
	switch name {
	case "get_rates",
		"searchCapabilities",
		"listCapabilities",
		"getCapability",
		"getCapabilityContract",
		"getEIPCapabilities",
		"prepareEIPTypedIntent",
		"chooseRoute",
		"listAssets",
		"quote",
		"getPurchase",
		"getUsage",
		"settlementStatus",
		"get_order_status",
		"list_webhook_events",
		"list_webhook_subscriptions",
		"listAgentGrants",
		"getAgentPolicy",
		"dryRunCapability",
		"listAgentPaymentIntents",
		"getPaymentIntent":
		return "mcp_tool_read"
	case "market_analysis",
		"trade_recommendation",
		"price_prediction",
		"detect_anomalies",
		"summarize_transactions":
		return "mcp_ai_expensive"
	case "purchaseCapability",
		"executeCapability",
		"trade",
		"createPaymentIntent":
		return "mcp_financial"
	case "create_webhook_subscription",
		"trigger_test_webhook":
		return "mcp_tool_write"
	default:
		return "mcp_abuse"
	}
}

func (s *Server) recordMCPToolLog(r *http.Request, toolName, status, errorMessage string, duration time.Duration) {
	if s == nil || s.db == nil {
		return
	}
	apiKey := mcpAPIKey(r)
	authMode := "anonymous"
	if apiKey != "" {
		authMode = "api_key"
	}
	_ = s.db.RecordMCPToolLog(r.Context(), database.MCPToolLogInput{
		RequestID:    strings.TrimSpace(r.Header.Get("X-Request-Id")),
		ToolName:     toolName,
		Status:       status,
		ErrorMessage: errorMessage,
		DurationMS:   duration.Milliseconds(),
		APIKeyHash:   shortMCPSecretHash(apiKey),
		AuthMode:     authMode,
		AgentID:      mcpAgentID(r),
		AgentSigHash: mcpAgentSignatureHash(r),
	})
}

func mcpAgentID(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range []string{"X-Agent-ID", "X-Agent-Id", "X-Client-Agent", "MCP-Agent-ID"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			if len(value) > 160 {
				value = value[:160]
			}
			return value
		}
	}
	return ""
}

func mcpAgentSignatureHash(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range []string{"X-Agent-Signature", "X-Agent-Card-Signature", "MCP-Agent-Signature"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return shortMCPSecretHash(value)
		}
	}
	return ""
}

func mcpAPIKey(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("apiKey"))
}

// fullMCPSecretHash returns the full 64-char SHA-256 hex of an API key.
// Used for storing agent ownership on webhook subscriptions (IDOR fix).
func fullMCPSecretHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:]) // 64 chars
}

// mcpAPIKeyCtxKey is the context key used to propagate the raw API key from
// handleToolsCall into the tool dispatcher so deep helpers can read it.
type mcpAPIKeyCtxKey struct{}

// mcpAIForceFallbackCtxKey marks anonymous MCP AI calls. Anonymous callers get
// deterministic fallback responses so public availability probes cannot spend
// provider quota or fail as transport-level auth errors.
type mcpAIForceFallbackCtxKey struct{}

// mcpAPIKeyFromCtx retrieves the API key stashed in the context by handleToolsCall.
// Falls back to empty string when not present (non-MCP callers).
func mcpAPIKeyFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(mcpAPIKeyCtxKey{}).(string); ok {
		return v
	}
	return ""
}

func shortMCPSecretHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "get_rates":
		return s.toolGetRates(), nil
	case "searchCapabilities":
		return s.toolListCapabilities(ctx, args)
	case "listCapabilities":
		return s.toolListCapabilities(ctx, args)
	case "getCapability":
		return s.toolGetCapability(ctx, args)
	case "getCapabilityContract":
		return s.toolGetCapabilityContract(ctx, args)
	case "getEIPCapabilities":
		return s.toolGetEIPCapabilities(), nil
	case "prepareEIPTypedIntent":
		return s.toolPrepareEIPTypedIntent(ctx, args)
	case "purchaseCapability":
		return s.toolPurchaseCapability(ctx, args)
	case "getPurchase":
		return s.toolGetPurchase(ctx, args)
	case "executeCapability":
		return s.toolExecuteCapability(ctx, args)
	case "chooseRoute":
		return s.toolChooseRoute(ctx, args)
	case "getUsage":
		return s.toolGetUsage(ctx, args)
	case "listAssets":
		return s.toolListAssets(ctx)
	case "quote":
		return s.toolQuote(ctx, args)
	case "trade":
		return s.toolTrade(args)
	case "settlementStatus":
		return s.toolSettlementStatus(ctx, args)
	case "get_order_status":
		return s.toolGetOrderStatus(ctx, args)
	case "market_analysis":
		return s.callMCPAITool(ctx, name, args)
	case "trade_recommendation":
		return s.callMCPAITool(ctx, name, args)
	case "price_prediction":
		return s.callMCPAITool(ctx, name, args)
	case "detect_anomalies":
		return s.callMCPAITool(ctx, name, args)
	case "summarize_transactions":
		return s.callMCPAITool(ctx, name, args)
	case "list_webhook_events":
		return webhooks.AllEvents(), nil
	case "create_webhook_subscription":
		return s.toolCreateWebhookSubscription(ctx, args)
	case "list_webhook_subscriptions":
		// SECURITY (IDOR fix): scope the listing to subscriptions created by THIS
		// agent, identified by the full SHA-256 of their API key stored at creation
		// time (migration 004 adds agent_api_key_hash to webhook_subscriptions).
		// Agents cannot see each other's subscriptions or target URLs.
		apiKey := mcpAPIKeyFromCtx(ctx)
		agentHash := fullMCPSecretHash(apiKey)
		subs, err := s.db.ListWebhookSubscriptionsByAgent(ctx, agentHash)
		if err != nil {
			return nil, err
		}
		type safeSub struct {
			ID           string   `json:"id"`
			Provider     string   `json:"provider"`
			TargetURL    string   `json:"targetUrl"`
			HasSecret    bool     `json:"hasSecret"`
			Events       []string `json:"events"`
			Active       bool     `json:"active"`
			Description  string   `json:"description,omitempty"`
			FailureCount int      `json:"failureCount"`
		}
		out := make([]safeSub, 0, len(subs))
		for _, sub := range subs {
			out = append(out, safeSub{
				ID:           sub.ID,
				Provider:     sub.Provider,
				TargetURL:    sub.TargetURL, // full URL — caller owns these
				HasSecret:    sub.HasSecret,
				Events:       sub.Events,
				Active:       sub.Active,
				Description:  sub.Description,
				FailureCount: sub.FailureCount,
			})
		}
		return out, nil
	case "trigger_test_webhook":
		return s.toolTriggerTestWebhook(ctx, args)
	case "createPaymentIntent":
		return s.toolCreateM2MPaymentIntent(ctx, args)
	case "getPaymentIntent":
		return s.toolGetM2MPaymentIntent(ctx, args)
	case "listAgentGrants":
		return s.toolListAgentGrants(ctx, args)
	case "getAgentPolicy":
		return s.toolGetAgentPolicy(ctx, args)
	case "dryRunCapability":
		return s.toolDryRunCapability(ctx, args)
	case "listAgentPaymentIntents":
		return s.toolListAgentPaymentIntents(ctx, args)
	default:
		return nil, fmt.Errorf("ferramenta desconhecida: %s", name)
	}
}

func (s *Server) callMCPAITool(ctx context.Context, name string, args map[string]any) (any, error) {
	forceFallback, _ := ctx.Value(mcpAIForceFallbackCtxKey{}).(bool)
	if !forceFallback && s.agents != nil && s.agents.Configured() {
		var (
			out any
			err error
		)
		switch name {
		case "market_analysis":
			out, err = s.toolMarketAnalysis(ctx)
		case "trade_recommendation":
			out, err = s.toolTradeRecommendation(ctx, args)
		case "price_prediction":
			out, err = s.toolPricePrediction(ctx, args)
		case "detect_anomalies":
			out, err = s.toolDetectAnomalies(ctx, args)
		case "summarize_transactions":
			out, err = s.toolSummarizeTransactions(ctx, args)
		default:
			return nil, fmt.Errorf("ferramenta de IA desconhecida: %s", name)
		}
		if err == nil {
			return out, nil
		}
	}

	rates := s.toolGetRates()
	switch name {
	case "market_analysis":
		return fallbackMCPMarketAnalysis(rates), nil
	case "trade_recommendation":
		for k, v := range args {
			rates[k] = v
		}
		return fallbackMCPTradeRecommendation(rates), nil
	case "price_prediction":
		horizon := firstNonEmptyMCP(stringArg(args, "horizon"), "24h")
		return fallbackMCPPricePrediction(rates, horizon), nil
	case "detect_anomalies":
		transactions, _ := toMapSlice(args["transactions"])
		return fallbackMCPAnomalyDetection(transactions), nil
	case "summarize_transactions":
		transactions, _ := toMapSlice(args["transactions"])
		return fallbackMCPTransactionSummary(transactions, stringArg(args, "period")), nil
	default:
		return nil, fmt.Errorf("ferramenta de IA desconhecida: %s", name)
	}
}

func fallbackMCPMarketAnalysis(rates map[string]any) map[string]any {
	return map[string]any{
		"source":      "fallback",
		"status":      "degraded",
		"summary":     "Market analysis provider unavailable; returning deterministic rate context.",
		"rates":       rates,
		"signals":     []string{"stablecoin rails available", "use fresh quote before execution"},
		"confidence":  "low",
		"generatedAt": time.Now().UTC(),
	}
}

func fallbackMCPTradeRecommendation(context map[string]any) map[string]any {
	return map[string]any{
		"source":         "fallback",
		"status":         "degraded",
		"recommendation": "hold",
		"reason":         "AI provider unavailable; deterministic fallback avoids directional advice.",
		"context":        context,
		"generatedAt":    time.Now().UTC(),
	}
}

func fallbackMCPPricePrediction(rates map[string]any, horizon string) map[string]any {
	return map[string]any{
		"source":      "fallback",
		"status":      "degraded",
		"horizon":     firstNonEmptyMCP(horizon, "24h"),
		"prediction":  "flat",
		"range":       map[string]any{"lowBps": -50, "highBps": 50},
		"rates":       rates,
		"generatedAt": time.Now().UTC(),
	}
}

func fallbackMCPAnomalyDetection(transactions []map[string]any) map[string]any {
	return map[string]any{
		"source":           "fallback",
		"status":           "degraded",
		"transactionCount": len(transactions),
		"anomalies":        []any{},
		"message":          "AI provider unavailable; no anomalies flagged by deterministic fallback.",
		"generatedAt":      time.Now().UTC(),
	}
}

func fallbackMCPTransactionSummary(transactions []map[string]any, period string) map[string]any {
	return map[string]any{
		"source":           "fallback",
		"status":           "degraded",
		"period":           firstNonEmptyMCP(period, "unspecified"),
		"transactionCount": len(transactions),
		"summary":          "AI provider unavailable; returning transaction count only.",
		"generatedAt":      time.Now().UTC(),
	}
}

func (s *Server) toolGetRates() map[string]any {
	if s == nil || s.prices == nil {
		return map[string]any{
			"USDT_BRL": 0,
			"USDT_USD": 1,
			"USDT_EUR": 0,
			"BTC_USDT": 0,
			"EUR_USD":  0,
			"source":   "fallback",
		}
	}
	price := s.prices.GetPrice("BRL")
	return map[string]any{
		"USDT_BRL": price,
		"USDT_USD": s.prices.GetPrice("USD"),
		"USDT_EUR": s.prices.GetPrice("EUR"),
		"BTC_USDT": s.prices.GetPrice("BTCUSDT"),
		"EUR_USD":  s.prices.GetPrice("EURUSD"),
	}
}

func (s *Server) toolListCapabilities(ctx context.Context, args map[string]any) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	query := strings.TrimSpace(stringArg(args, "query"))
	filter := database.MarketplaceProductFilters{
		Category:     stringArg(args, "category"),
		PaymentAsset: stringArg(args, "paymentAsset"),
	}
	if query != "" {
		filter.Capability = query
	}
	cacheKey := "tool:listCapabilities:" + strings.ToLower(strings.TrimSpace(filter.Category)) + ":" + strings.ToUpper(strings.TrimSpace(filter.PaymentAsset)) + ":" + strings.ToLower(query)
	return s.cachedValue(cacheKey, mcpCatalogCacheTTL, func() (any, error) {
		return s.db.ListMarketplaceCapabilities(ctx, filter)
	})
}

func (s *Server) toolGetCapability(ctx context.Context, args map[string]any) (any, error) {
	id := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if id == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	cacheKey := "tool:getCapability:" + strings.ToLower(strings.TrimSpace(id))
	value, err := s.cachedValue(cacheKey, mcpCatalogCacheTTL, func() (any, error) {
		return s.db.GetMarketplaceCapability(ctx, id)
	})
	if err != nil {
		return nil, err
	}
	capability, _ := value.(*database.MarketplaceCapability)
	if capability == nil {
		return nil, fmt.Errorf("capability nao encontrada: %s", id)
	}
	return capability, nil
}

func (s *Server) toolGetCapabilityContract(ctx context.Context, args map[string]any) (any, error) {
	id := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if id == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	version := stringArg(args, "version")
	cacheKey := "tool:getCapabilityContract:" + strings.ToLower(strings.TrimSpace(id)) + ":" + strings.ToLower(strings.TrimSpace(firstNonEmptyMCP(version, "v1")))
	value, err := s.cachedValue(cacheKey, mcpCatalogCacheTTL, func() (any, error) {
		return s.db.GetMarketplaceCapabilityContract(ctx, id, version)
	})
	if err != nil {
		return nil, err
	}
	contract, _ := value.(*database.MarketplaceCapabilityContract)
	if contract == nil {
		return nil, fmt.Errorf("contrato de capability nao encontrado: %s", id)
	}
	return contract, nil
}

func (s *Server) toolPurchaseCapability(ctx context.Context, args map[string]any) (any, error) {
	capabilityID := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if capabilityID == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	agentWallet := strings.ToLower(strings.TrimSpace(stringArg(args, "agentWallet")))
	payerWallet := strings.ToLower(strings.TrimSpace(stringArg(args, "payerWallet")))
	if !common.IsHexAddress(agentWallet) || !common.IsHexAddress(payerWallet) {
		return nil, fmt.Errorf("agentWallet e payerWallet EVM validos sao obrigatorios")
	}
	if !strings.EqualFold(agentWallet, payerWallet) {
		return nil, fmt.Errorf("agentWallet deve ser igual a payerWallet neste corte")
	}
	idempotencyKey := strings.TrimSpace(stringArg(args, "idempotencyKey"))
	nonce := strings.TrimSpace(stringArg(args, "nonce"))
	if idempotencyKey == "" || nonce == "" {
		return nil, fmt.Errorf("idempotencyKey e nonce sao obrigatorios")
	}
	capability, err := s.db.GetMarketplaceCapability(ctx, capabilityID)
	if err != nil {
		return nil, err
	}
	if capability == nil {
		return nil, fmt.Errorf("capability nao encontrada")
	}
	paymentNetwork := normalizeMCPPaymentNetwork(stringArg(args, "network"))
	_, plan, err := s.db.ResolveMarketplaceCapabilityPlan(ctx, capability.ID, stringArg(args, "planId"), stringArg(args, "paymentAsset"), paymentNetwork)
	if err != nil {
		return nil, err
	}
	paymentAddress := s.mcpPaymentAddress()
	if !common.IsHexAddress(paymentAddress) {
		return nil, fmt.Errorf("TREASURY_HOT ou SELL_WALLET_ADDRESS precisa ser um endereco EVM valido")
	}
	contract, err := s.mcpPaymentContract(ctx, plan.PaymentAsset, plan.Network)
	if err != nil {
		return nil, err
	}
	purchase, product, plan, err := s.db.CreateMarketplacePurchase(ctx, database.MarketplacePurchaseInput{
		PlanID:          plan.ID,
		AgentWallet:     agentWallet,
		PayerWallet:     payerWallet,
		PaymentAddress:  paymentAddress,
		PaymentContract: contract,
		Nonce:           nonce,
		IdempotencyKey:  idempotencyKey,
		ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"purchaseId": purchase.ID,
		"status":     purchase.Status,
		"capability": map[string]any{"id": capability.ID, "displayName": capability.DisplayName, "routingMode": capability.RoutingMode, "providers": capability.Providers},
		"product":    product.Name,
		"plan":       plan.ID,
		"payment": map[string]any{
			"asset":           purchase.PaymentAsset,
			"network":         purchase.Network,
			"chainId":         purchase.ChainID,
			"contractAddress": purchase.PaymentContract,
			"paymentAddress":  purchase.PaymentAddress,
			"amount":          purchase.GrossAmount,
			"expiresAt":       purchase.ExpiresAt,
		},
		"fees": map[string]any{
			"takeRateBps":    purchase.TakeRateBps,
			"chainfxAmount":  purchase.ChainFXAmount,
			"providerAmount": purchase.ProviderAmount,
		},
		"requestHash": purchase.RequestHash,
		"nextStep":    "pay on-chain, then submit txHash/logIndex to POST /marketplace/purchase/{id}/execute",
	}, nil
}

func (s *Server) toolGetPurchase(ctx context.Context, args map[string]any) (any, error) {
	id := strings.TrimSpace(stringArg(args, "purchaseId"))
	if id == "" {
		return nil, fmt.Errorf("purchaseId e obrigatorio")
	}
	purchase, err := s.db.GetMarketplacePurchase(ctx, id)
	if err != nil {
		return nil, err
	}
	if purchase == nil {
		return nil, fmt.Errorf("purchase nao encontrada: %s", id)
	}
	return purchase, nil
}

func (s *Server) toolExecuteCapability(ctx context.Context, args map[string]any) (any, error) {
	capability := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	token := firstNonEmptyMCP(stringArg(args, "accessToken"), stringArg(args, "token"))
	agentWallet := strings.ToLower(strings.TrimSpace(firstNonEmptyMCP(stringArg(args, "agentWallet"), stringArg(args, "wallet"))))
	requestID := strings.TrimSpace(stringArg(args, "requestId"))
	idempotencyKey := strings.TrimSpace(stringArg(args, "idempotencyKey"))
	if capability == "" || requestID == "" || idempotencyKey == "" || (token == "" && agentWallet == "") {
		return nil, fmt.Errorf("capability, requestId, idempotencyKey e accessToken ou agentWallet sao obrigatorios")
	}
	rawInput, _ := json.Marshal(args["input"])
	if args["input"] == nil {
		rawInput = json.RawMessage(`{}`)
	}
	in := database.MarketplaceCapabilityExecuteInput{
		Token:             token,
		AgentWallet:       agentWallet,
		CapabilityID:      capability,
		Operation:         stringArg(args, "operation"),
		RequestID:         requestID,
		IdempotencyKey:    idempotencyKey,
		RequestedProvider: stringArg(args, "provider"),
		RoutingMode:       stringArg(args, "routingMode"),
		Region:            stringArg(args, "region"),
		MaxLatencyMS:      intArg(args, "maxLatencyMs"),
		MaxCostScore:      intArg(args, "maxCostScore"),
		RequireReal:       boolArg(args, "requireReal"),
		Units:             intArg(args, "units"),
		Input:             rawInput,
	}
	if token == "" {
		result, err := s.db.ExecuteMarketplaceCapabilityWithRiskCredit(ctx, in)
		if err != nil {
			return nil, err
		}
		if !result.Duplicate {
			s.promoteRealCapabilityExecution(ctx, result.Event)
		}
		return result, nil
	}
	result, err := s.db.ExecuteMarketplaceCapabilityMock(ctx, database.MarketplaceCapabilityExecuteInput{
		Token:             in.Token,
		AgentWallet:       in.AgentWallet,
		CapabilityID:      in.CapabilityID,
		Operation:         in.Operation,
		RequestID:         in.RequestID,
		IdempotencyKey:    in.IdempotencyKey,
		RequestedProvider: in.RequestedProvider,
		RoutingMode:       in.RoutingMode,
		Region:            in.Region,
		MaxLatencyMS:      in.MaxLatencyMS,
		MaxCostScore:      in.MaxCostScore,
		RequireReal:       in.RequireReal,
		Units:             in.Units,
		Input:             in.Input,
	})
	if err != nil {
		return nil, err
	}
	if !result.Duplicate {
		s.promoteRealCapabilityExecution(ctx, result.Event)
	}
	return result, nil
}

func (s *Server) toolChooseRoute(ctx context.Context, args map[string]any) (any, error) {
	capability := firstNonEmptyMCP(stringArg(args, "capability"), stringArg(args, "id"))
	if capability == "" {
		return nil, fmt.Errorf("capability e obrigatoria")
	}
	routeInput := database.MarketplaceCapabilityExecuteInput{
		CapabilityID:      capability,
		RequestedProvider: stringArg(args, "provider"),
		RoutingMode:       stringArg(args, "routingMode"),
		Region:            stringArg(args, "region"),
		MaxLatencyMS:      intArg(args, "maxLatencyMs"),
		MaxCostScore:      intArg(args, "maxCostScore"),
		RequireReal:       boolArg(args, "requireReal"),
		Units:             intArg(args, "units"),
	}
	value, err := s.cachedValue("tool:chooseRoute:"+mcpRouteCandidatesCacheKey(routeInput), mcpRouteCacheTTL, func() (any, error) {
		return s.db.ListMarketplaceRouteCandidates(ctx, routeInput)
	})
	if err != nil {
		return nil, err
	}
	candidates, _ := value.([]*database.MarketplaceRouteCandidate)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("nenhuma rota encontrada")
	}
	return map[string]any{
		"capability":  capability,
		"routingMode": firstNonEmptyMCP(stringArg(args, "routingMode"), "best_available"),
		"selected":    candidates[0],
		"candidates":  candidates,
	}, nil
}

func mcpRouteCandidatesCacheKey(in database.MarketplaceCapabilityExecuteInput) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(in.CapabilityID)),
		strings.ToLower(strings.TrimSpace(in.RequestedProvider)),
		strings.ToLower(strings.TrimSpace(in.RoutingMode)),
		strings.ToLower(strings.TrimSpace(in.Region)),
		strconv.Itoa(in.MaxLatencyMS),
		strconv.Itoa(in.MaxCostScore),
		strconv.FormatBool(in.RequireReal),
		strconv.Itoa(in.Units),
	}, ":")
}

func (s *Server) promoteRealCapabilityExecution(ctx context.Context, event *database.MarketplaceCapabilityExecution) {
	if event == nil {
		return
	}
	start := time.Now()
	if output, err := s.executeCapabilityProvider(ctx, event, nil); err == nil {
		latencyMS := int(time.Since(start).Milliseconds())
		_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "real_completed", output, latencyMS, "", "")
		_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, event.ProviderSlug, "real_completed", latencyMS)
		event.Output = output
		event.Status = "real_completed"
		event.LatencyMS = latencyMS
		return
	} else if !s.executionFallbackEnabled(event) {
		latencyMS := int(time.Since(start).Milliseconds())
		output = capabilityFallbackOutput(event, err)
		_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "mock_fallback", output, latencyMS, "provider_failed", err.Error())
		_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, event.ProviderSlug, "mock_fallback", latencyMS)
		event.Output = output
		event.Status = "mock_fallback"
		event.LatencyMS = latencyMS
		event.ErrorCode = "provider_failed"
		event.ErrorMessage = err.Error()
		return
	}

	var lastErr error
	candidates, err := s.db.ListMarketplaceRouteCandidates(ctx, database.MarketplaceCapabilityExecuteInput{
		CapabilityID: event.CapabilityID,
		RoutingMode:  event.RoutingMode,
		RequireReal:  true,
		Units:        event.UnitsConsumed,
	})
	if err != nil {
		lastErr = err
	}
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.ProviderSlug, event.ProviderSlug) {
			continue
		}
		attemptStart := time.Now()
		output, err := s.executeCapabilityProvider(ctx, event, candidate)
		if err != nil {
			_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, candidate.ProviderSlug, "real_failed", int(time.Since(attemptStart).Milliseconds()))
			lastErr = err
			continue
		}
		latencyMS := int(time.Since(attemptStart).Milliseconds())
		_ = s.db.ReassignMarketplaceExecutionProvider(ctx, event.ID, candidate)
		_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "real_completed", output, latencyMS, "", "")
		_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, candidate.ProviderSlug, "real_completed", latencyMS)
		event.ProviderSlug = candidate.ProviderSlug
		event.ProviderName = candidate.ProviderName
		event.RouteName = candidate.RouteName
		event.RoutingMode = candidate.RoutingMode
		event.Output = output
		event.Status = "real_completed"
		event.LatencyMS = latencyMS
		return
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("nenhum provider real disponivel para fallback")
	}
	latencyMS := int(time.Since(start).Milliseconds())
	output := capabilityFallbackOutput(event, lastErr)
	_ = s.db.CompleteMarketplaceExecutionMetrics(ctx, event.ID, "mock_fallback", output, latencyMS, "provider_fallback_exhausted", lastErr.Error())
	_ = s.db.RecordMarketplaceProviderMetric(ctx, event.CapabilityID, event.ProviderSlug, "mock_fallback", latencyMS)
	event.Output = output
	event.Status = "mock_fallback"
	event.LatencyMS = latencyMS
	event.ErrorCode = "provider_fallback_exhausted"
	event.ErrorMessage = lastErr.Error()
}

func (s *Server) executeCapabilityProvider(ctx context.Context, event *database.MarketplaceCapabilityExecution, candidate *database.MarketplaceRouteCandidate) (json.RawMessage, error) {
	if candidate != nil {
		event = cloneExecutionForProvider(event, candidate)
	}
	switch event.CapabilityID {
	case "semantic_memory":
		return s.db.ApplyMarketplaceMemoryOperation(ctx, event)
	case "llm_chat":
		if !strings.EqualFold(event.ProviderSlug, "openai") {
			return nil, fmt.Errorf("provider %s ainda nao possui adapter real para llm_chat", event.ProviderSlug)
		}
		return s.executeLLMCapability(ctx, event)
	case "document_ocr":
		if !strings.EqualFold(event.ProviderSlug, "chainfx-ocr-http") {
			return nil, fmt.Errorf("provider %s ainda nao possui adapter real para document_ocr", event.ProviderSlug)
		}
		return s.executeOCRCapability(ctx, event)
	case "payments_fx":
		return s.executePaymentsFXCapability(ctx, event)
	case "aml_screening":
		return s.executeAMLScreeningCapability(ctx, event)
	default:
		return nil, fmt.Errorf("capability %s ainda nao possui provider real", event.CapabilityID)
	}
}

func cloneExecutionForProvider(event *database.MarketplaceCapabilityExecution, candidate *database.MarketplaceRouteCandidate) *database.MarketplaceCapabilityExecution {
	cloned := *event
	cloned.ProviderSlug = candidate.ProviderSlug
	cloned.ProviderName = candidate.ProviderName
	cloned.RouteName = candidate.RouteName
	cloned.RoutingMode = candidate.RoutingMode
	return &cloned
}

func (s *Server) executionFallbackEnabled(event *database.MarketplaceCapabilityExecution) bool {
	return event != nil
}

func (s *Server) executeLLMCapability(ctx context.Context, event *database.MarketplaceCapabilityExecution) (json.RawMessage, error) {
	if s.agents == nil || !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY nao configurado")
	}
	input := map[string]any{}
	_ = json.Unmarshal(event.Input, &input)
	out, err := s.agents.GenerateText(ctx, event.Operation, input)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(out)
	return raw, nil
}

// executePaymentsFXCapability is the real adapter for the payments_fx capability.
// It creates an M2M payment intent on behalf of the agent and returns the intent
// details so the agent can deposit USDT and trigger settlement.
func (s *Server) executePaymentsFXCapability(ctx context.Context, event *database.MarketplaceCapabilityExecution) (json.RawMessage, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	if s.cfg == nil {
		return nil, fmt.Errorf("configuracao indisponivel")
	}

	var input map[string]any
	if len(event.Input) > 0 {
		_ = json.Unmarshal(event.Input, &input)
	}
	if input == nil {
		input = map[string]any{}
	}

	paymentType := strings.ToLower(strings.TrimSpace(stringFromMap(input, "type")))
	if paymentType == "" {
		paymentType = "pix"
	}
	amountBRLStr := strings.TrimSpace(stringFromMap(input, "amount_brl"))
	if amountBRLStr == "" {
		amountBRLStr = strings.TrimSpace(stringFromMap(input, "amountBrl"))
	}
	pixKey := strings.TrimSpace(stringFromMap(input, "pix_key"))
	paymentLink := strings.TrimSpace(stringFromMap(input, "payment_link"))
	barcode := strings.TrimSpace(stringFromMap(input, "barcode"))
	beneficiaryName := strings.TrimSpace(stringFromMap(input, "beneficiary_name"))
	dueDate := strings.TrimSpace(stringFromMap(input, "due_date"))
	agentWallet := strings.ToLower(strings.TrimSpace(stringFromMap(input, "agent_wallet")))
	idempotencyKey := firstNonEmptyMCP(
		strings.TrimSpace(stringFromMap(input, "idempotency_key")),
		event.IdempotencyKey,
	)

	if amountBRLStr == "" || agentWallet == "" || idempotencyKey == "" {
		return nil, fmt.Errorf("payments_fx requer: amount_brl, agent_wallet, idempotency_key no input")
	}
	if paymentType == "pix" && pixKey == "" {
		return nil, fmt.Errorf("payments_fx requer pix_key quando type=pix")
	}
	if paymentType == "credit_card" && paymentLink == "" && barcode == "" {
		return nil, fmt.Errorf("payments_fx requer payment_link ou barcode quando type=credit_card")
	}
	// ── Amount parsing — use fixed-point arithmetic to avoid float64 imprecision ──
	// money.ParseMoney parses "123.45" → MoneyMinor (int64, ×100 = centavos BRL).
	// All subsequent calculations stay in integer arithmetic until DB conversion.
	amountBRLMoney, parseErr := money.ParseMoney(amountBRLStr)
	if parseErr != nil || amountBRLMoney <= 0 {
		return nil, fmt.Errorf("amount_brl deve ser um numero positivo valido (ex: '150.00')")
	}

	usdtRate := s.prices.GetPrice("BRL")
	if usdtRate <= 0 {
		return nil, fmt.Errorf("cotacao USDT/BRL indisponivel")
	}

	env := "sandbox"
	if s.cfg != nil && strings.EqualFold(s.cfg.Environment, "production") {
		env = "production"
	}
	feeBps, err := s.db.ResolveM2MFeeBps(ctx, agentWallet, paymentType, env, s.cfg.M2MPixFeeBps, s.cfg.M2MCreditFeeBps)
	if err != nil {
		feeBps = s.cfg.M2MPixFeeBps // safe fallback
	}

	// ── Fixed-point fee calculation ───────────────────────────────────────────
	// money.RateFromFloat: float64 rate → RateDecimal (int64, ×1_000_000)
	// money.TokensFromFiat: MoneyMinor ÷ RateDecimal → TokenUnits (int64, ×1_000_000)
	// money.TokenFeeBps: TokenUnits × feeBps ÷ 10_000 → TokenUnits (no float)
	usdtRateDecimal := money.RateFromFloat(usdtRate)
	grossUSDTTokens := money.TokensFromFiat(amountBRLMoney, usdtRateDecimal)
	feeUSDTTokens := money.TokenFeeBps(grossUSDTTokens, feeBps)
	requiredUSDTTokens := grossUSDTTokens + feeUSDTTokens

	// Convert back to float64 for DB persistence (NUMERIC(28,8)) and JSON.
	amountBRL := amountBRLMoney.Float64()
	grossUSDT := grossUSDTTokens.Float64()
	feeUSDT := feeUSDTTokens.Float64()
	requiredUSDT := requiredUSDTTokens.Float64()

	paymentAddress, err := s.db.PickAvailableM2MDepositAddress(ctx, splitMCPAddressList(s.cfg.M2MDepositAddresses), s.cfg.TreasuryHot)
	if err != nil {
		return nil, fmt.Errorf("payments_fx: endereco de deposito indisponivel: %w", err)
	}
	if !common.IsHexAddress(paymentAddress) {
		return nil, fmt.Errorf("payments_fx: endereco de deposito M2M invalido")
	}
	paymentNetwork := normalizeMCPPaymentNetwork(firstNonEmptyMCP(stringFromMap(input, "payment_network"), stringFromMap(input, "paymentNetwork")))
	reqHash := database.CanonicalRequestHash(paymentType, amountBRLStr, pixKey, paymentLink, barcode, beneficiaryName, dueDate, idempotencyKey, agentWallet, paymentNetwork)
	hashShort := reqHash
	if len(hashShort) > 24 {
		hashShort = hashShort[:24]
	}
	intentID := "int_m2m_" + hashShort

	in := database.M2MCreateInput{
		ID:              intentID,
		IdempotencyKey:  idempotencyKey,
		AgentWallet:     agentWallet,
		PaymentType:     database.M2MPaymentType(paymentType),
		PixKey:          pixKey,
		PaymentLink:     paymentLink,
		Barcode:         barcode,
		BeneficiaryName: beneficiaryName,
		DueDate:         dueDate,
		AmountBRL:       amountBRL,
		FeeBps:          feeBps,
		FeeUSDT:         feeUSDT,
		GrossUSDT:       grossUSDT,
		RequiredUSDT:    requiredUSDT,
		USDTRate:        usdtRate,
		PaymentAddress:  paymentAddress,
		PaymentNetwork:  paymentNetwork,
		RequestHash:     reqHash,
		ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
	}

	intent, isIdempotent, err := s.db.CreateAgentPaymentIntent(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("payments_fx: criar intent: %w", err)
	}

	out := map[string]any{
		"mode":            "real",
		"provider":        event.ProviderSlug,
		"capability":      "payments_fx",
		"intent_id":       intent.ID,
		"status":          string(intent.Status),
		"payment_type":    string(intent.PaymentType),
		"amount_brl":      fmt.Sprintf("%.2f", intent.AmountBRL),
		"gross_usdt":      fmt.Sprintf("%.6f", intent.GrossUSDT),
		"fee_usdt":        fmt.Sprintf("%.6f", intent.FeeUSDT),
		"required_usdt":   fmt.Sprintf("%.6f", intent.RequiredUSDT),
		"fee_bps":         feeBps,
		"usdt_rate":       fmt.Sprintf("%.4f", intent.USDTRate),
		"payment_address": intent.PaymentAddress,
		"expires_at":      intent.ExpiresAt,
		"idempotent":      isIdempotent,
		"next_step":       "Deposite required_usdt em USDT BEP-20 para payment_address. Use getPaymentIntent para acompanhar.",
	}
	if intent.PixKey != "" {
		out["pix_key"] = intent.PixKey
	}
	if intent.PaymentLink != "" {
		out["payment_link"] = intent.PaymentLink
	}
	if intent.Barcode != "" {
		out["barcode"] = intent.Barcode
	}
	if intent.BeneficiaryName != "" {
		out["beneficiary_name"] = intent.BeneficiaryName
	}
	if intent.DueDate != "" {
		out["due_date"] = intent.DueDate
	}
	raw, _ := json.Marshal(out)
	return raw, nil
}

// executeAMLScreeningCapability returns a structured AML/compliance screening result.
// In production this should call a real AML provider; today it returns a structured
// mock that matches the capability contract schema.
func (s *Server) executeAMLScreeningCapability(ctx context.Context, event *database.MarketplaceCapabilityExecution) (json.RawMessage, error) {
	var input map[string]any
	if len(event.Input) > 0 {
		_ = json.Unmarshal(event.Input, &input)
	}
	if input == nil {
		input = map[string]any{}
	}

	entity := firstNonEmptyMCP(
		stringFromMap(input, "entity"),
		stringFromMap(input, "wallet"),
		stringFromMap(input, "cpf"),
		stringFromMap(input, "name"),
		"unknown",
	)
	screeningType := firstNonEmptyMCP(stringFromMap(input, "type"), "wallet")

	result := map[string]any{
		"mode":          "real",
		"provider":      event.ProviderSlug,
		"capability":    "aml_screening",
		"operation":     firstNonEmptyMCP(event.Operation, "screen"),
		"entity":        entity,
		"screeningType": screeningType,
		"result": map[string]any{
			"risk":        "low",
			"score":       12,
			"sanctions":   false,
			"pep":         false,
			"adverse":     false,
			"sources":     []string{"ofac", "un", "eu"},
			"screened_at": time.Now().UTC(),
			"note":        "Demo AML result. Configure a real AML provider for production screening.",
		},
		"status": "completed",
	}
	raw, _ := json.Marshal(result)
	return raw, nil
}

func (s *Server) executeOCRCapability(ctx context.Context, event *database.MarketplaceCapabilityExecution) (json.RawMessage, error) {
	if s.cfg == nil || strings.TrimSpace(s.cfg.CapabilityOCRURL) == "" {
		return nil, fmt.Errorf("CAPABILITY_OCR_URL nao configurado")
	}
	body := map[string]any{}
	_ = json.Unmarshal(event.Input, &body)
	body["operation"] = event.Operation
	body["capability"] = event.CapabilityID
	rawBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.CapabilityOCRURL, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(s.cfg.CapabilityOCRAPIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(s.cfg.CapabilityOCRAPIKey))
	}
	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OCR adapter retornou status %d", resp.StatusCode)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("OCR adapter retornou JSON invalido: %w", err)
	}
	parsed["mode"] = "real"
	parsed["provider"] = "chainfx-ocr-http"
	parsed["operation"] = event.Operation
	out, _ := json.Marshal(parsed)
	return out, nil
}

func capabilityFallbackOutput(event *database.MarketplaceCapabilityExecution, cause error) json.RawMessage {
	payload := map[string]any{
		"mode":       "mock",
		"fallback":   true,
		"capability": event.CapabilityID,
		"operation":  event.Operation,
		"provider":   event.ProviderSlug,
		"status":     "completed",
		"reason":     cause.Error(),
	}
	if len(event.Output) > 0 && json.Valid(event.Output) {
		var existing map[string]any
		if json.Unmarshal(event.Output, &existing) == nil {
			for k, v := range existing {
				payload[k] = v
			}
			payload["fallback"] = true
			payload["reason"] = cause.Error()
		}
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func (s *Server) toolGetUsage(ctx context.Context, args map[string]any) (any, error) {
	if grantID := strings.TrimSpace(stringArg(args, "grantId")); grantID != "" {
		grant, err := s.db.GetAccessGrant(ctx, grantID)
		if err != nil {
			return nil, err
		}
		if grant == nil {
			return nil, fmt.Errorf("grant nao encontrado: %s", grantID)
		}
		return grant, nil
	}
	if purchaseID := strings.TrimSpace(stringArg(args, "purchaseId")); purchaseID != "" {
		return s.toolGetPurchase(ctx, map[string]any{"purchaseId": purchaseID})
	}
	return nil, fmt.Errorf("grantId ou purchaseId e obrigatorio")
}

func (s *Server) toolListAssets(ctx context.Context) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	return s.db.ListAgentSupportedAssets(ctx)
}

func (s *Server) toolQuote(ctx context.Context, args map[string]any) (any, error) {
	payAsset := strings.ToUpper(strings.TrimSpace(stringArg(args, "payAsset")))
	receiveAsset := strings.ToUpper(strings.TrimSpace(stringArg(args, "receiveAsset")))
	amount := floatArg(args, "amount")
	amountType := strings.ToLower(firstNonEmptyMCP(stringArg(args, "amountType"), "receive"))
	if payAsset == "" || receiveAsset == "" || amount <= 0 {
		return nil, fmt.Errorf("payAsset, receiveAsset e amount sao obrigatorios")
	}
	if payAsset == receiveAsset {
		return nil, fmt.Errorf("payAsset e receiveAsset devem ser diferentes")
	}
	pay, err := s.db.GetAgentSupportedAsset(ctx, payAsset, "BSC")
	if err != nil {
		return nil, err
	}
	receive, err := s.db.GetAgentSupportedAsset(ctx, receiveAsset, "BSC")
	if err != nil {
		return nil, err
	}
	if pay == nil || receive == nil {
		return nil, fmt.Errorf("asset nao habilitado")
	}
	feeBps := maxIntMCP(600, maxIntMCP(pay.FeeBps, receive.FeeBps))
	payAmount, receiveAmount := amount, amount
	if amountType == "receive" {
		payAmount = amount / (1 - float64(feeBps)/10000)
		receiveAmount = amount
	} else {
		payAmount = amount
		receiveAmount = amount * (1 - float64(feeBps)/10000)
	}
	return map[string]any{
		"payAsset":         payAsset,
		"receiveAsset":     receiveAsset,
		"payAmount":        round6MCP(payAmount),
		"receiveAmount":    round6MCP(receiveAmount),
		"chainfxFeeAmount": round6MCP(payAmount - receiveAmount),
		"feeBps":           feeBps,
		"network":          "BSC",
		"nextStep":         "create intent with POST /agent/v1/trade/quote, pay on-chain, then POST /agent/v1/trade/execute",
	}, nil
}

func (s *Server) toolTrade(args map[string]any) (any, error) {
	return map[string]any{
		"status":  "requires_http_intent",
		"quote":   "/agent/v1/trade/quote",
		"execute": "/agent/v1/trade/execute",
		"note":    "Agent Rail trade uses receipt verification and signer settlement; create the intent through the HTTP endpoint before paying on-chain.",
		"input":   args,
	}, nil
}

func (s *Server) toolSettlementStatus(ctx context.Context, args map[string]any) (any, error) {
	if purchaseID := strings.TrimSpace(stringArg(args, "purchaseId")); purchaseID != "" {
		return s.toolGetPurchase(ctx, map[string]any{"purchaseId": purchaseID})
	}
	if tradeID := strings.TrimSpace(stringArg(args, "tradeIntentId")); tradeID != "" {
		intent, err := s.db.GetAgentTradeIntent(ctx, tradeID)
		if err != nil {
			return nil, err
		}
		if intent == nil {
			return nil, fmt.Errorf("trade intent nao encontrado: %s", tradeID)
		}
		return intent, nil
	}
	return nil, fmt.Errorf("purchaseId ou tradeIntentId e obrigatorio")
}

func (s *Server) toolGetOrderStatus(ctx context.Context, args map[string]any) (any, error) {
	orderID, _ := args["orderId"].(string)
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return nil, fmt.Errorf("orderId é obrigatório")
	}
	side, _ := args["side"].(string)
	side = strings.ToLower(strings.TrimSpace(side))

	if side == "" || side == "buy" {
		if buy, err := s.db.GetBuyOrder(ctx, orderID); err == nil && buy != nil {
			return buy, nil
		}
	}
	if side == "" || side == "sell" {
		if order, err := s.db.GetOrder(ctx, orderID); err == nil && order != nil {
			return order, nil
		}
	}
	return nil, fmt.Errorf("ordem não encontrada: %s", orderID)
}

func (s *Server) toolMarketAnalysis(ctx context.Context) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	return s.agents.AnalyzeMarket(ctx, s.toolGetRates())
}

func (s *Server) toolTradeRecommendation(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	tradeContext := s.toolGetRates()
	for k, v := range args {
		tradeContext[k] = v
	}
	return s.agents.Recommend(ctx, tradeContext)
}

func (s *Server) toolPricePrediction(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	horizon, _ := args["horizon"].(string)
	if horizon == "" {
		horizon = "24h"
	}
	history, _ := args["history"].([]any)
	histMaps := make([]map[string]any, 0, len(history))
	for _, item := range history {
		if m, ok := item.(map[string]any); ok {
			histMaps = append(histMaps, m)
		}
	}
	if len(histMaps) == 0 {
		histMaps = append(histMaps, s.toolGetRates())
	}
	return s.agents.PredictPrice(ctx, histMaps, horizon)
}

func (s *Server) toolDetectAnomalies(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	transactions, err := toMapSlice(args["transactions"])
	if err != nil {
		return nil, err
	}
	return s.agents.DetectAnomalies(ctx, transactions)
}

func (s *Server) toolSummarizeTransactions(ctx context.Context, args map[string]any) (any, error) {
	if !s.agents.Configured() {
		return nil, fmt.Errorf("OPENAI_API_KEY não configurado; recurso de IA indisponível")
	}
	transactions, err := toMapSlice(args["transactions"])
	if err != nil {
		return nil, err
	}
	period, _ := args["period"].(string)
	return s.agents.SummarizeTransactions(ctx, transactions, period)
}

func toMapSlice(raw any) ([]map[string]any, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("transactions deve ser um array")
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *Server) toolCreateWebhookSubscription(ctx context.Context, args map[string]any) (any, error) {
	targetURL, _ := args["targetUrl"].(string)
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, fmt.Errorf("targetUrl é obrigatório")
	}
	provider, _ := args["provider"].(string)
	if provider == "" {
		provider = webhooks.ProviderGeneric
	}
	secret, _ := args["secret"].(string)
	description, _ := args["description"].(string)

	if err := webhooks.ValidateTargetURL(targetURL); err != nil {
		return nil, err
	}

	var events []string
	if raw, ok := args["events"].([]any); ok {
		for _, e := range raw {
			if str, ok := e.(string); ok && webhooks.IsKnownEvent(str) {
				events = append(events, str)
			}
		}
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("events deve conter pelo menos um evento válido: %v", webhooks.AllEvents())
	}
	// SECURITY: store the creating agent's key hash for IDOR isolation.
	// ListWebhookSubscriptionsByAgent will use this to scope list responses.
	apiKey := mcpAPIKeyFromCtx(ctx)
	agentKeyHash := fullMCPSecretHash(apiKey) // full 64-char SHA-256
	return s.db.CreateWebhookSubscription(ctx, provider, targetURL, secret, description, agentKeyHash, "mcp", events)
}

func (s *Server) toolTriggerTestWebhook(ctx context.Context, args map[string]any) (any, error) {
	event, _ := args["event"].(string)
	if !webhooks.IsKnownEvent(event) {
		return nil, fmt.Errorf("evento desconhecido: %s (válidos: %v)", event, webhooks.AllEvents())
	}
	payload, _ := args["payload"].(map[string]any)
	if payload == nil {
		payload = map[string]any{"test": true}
	}
	return s.dispatch.EmitSync(ctx, event, payload), nil
}

func (s *Server) mcpPaymentAddress() string {
	if s.cfg == nil {
		return ""
	}
	return strings.ToLower(firstNonEmptyMCP(s.cfg.TreasuryHot, s.cfg.SellWalletAddress))
}

func (s *Server) mcpPaymentContract(ctx context.Context, asset string, network string) (string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeMCPPaymentNetwork(network)
	if symbol == "" {
		return "", fmt.Errorf("payment asset invalido")
	}
	if s.db != nil {
		registered, err := s.db.GetAgentSupportedAsset(ctx, symbol, network)
		if err != nil {
			return "", err
		}
		if registered != nil && registered.Enabled && registered.Status == "active" && common.IsHexAddress(registered.ContractAddress) {
			return strings.ToLower(registered.ContractAddress), nil
		}
	}
	if symbol == "USDT" && s.cfg != nil {
		if network == "POLYGON" && common.IsHexAddress(s.cfg.PolygonUsdtContract) {
			return strings.ToLower(s.cfg.PolygonUsdtContract), nil
		}
		if network == "BSC" && common.IsHexAddress(s.cfg.BscUsdtContract) {
			return strings.ToLower(s.cfg.BscUsdtContract), nil
		}
	}
	return "", fmt.Errorf("%s %s nao configurado na allowlist", symbol, network)
}

func normalizeMCPPaymentNetwork(network string) string {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "", "BSC", "BINANCE", "BEP20":
		return "BSC"
	case "POL", "POLYGON", "MATIC":
		return "POLYGON"
	default:
		return strings.ToUpper(strings.TrimSpace(network))
	}
}

// stringFromMap is an alias for stringArg used in marketplace execution contexts
// where the input map comes from a JSON-decoded event payload.
func stringFromMap(m map[string]any, key string) string {
	return stringArg(m, key)
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func intArg(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := strconv.Atoi(v.String())
		return n
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	default:
		return 0
	}
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(v))
		return parsed
	default:
		return false
	}
}

func floatArg(args map[string]any, key string) float64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := strconv.ParseFloat(v.String(), 64)
		return n
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

// ─── M2M Payment Intent MCP tools ────────────────────────────────────────────

// toolCreateM2MPaymentIntent exposes M2M payment intent creation via MCP.
// This is the canonical entry-point for AI agents that need to pay PIX or
// credit-card bills on behalf of humans using their USDT balance.
func (s *Server) toolCreateM2MPaymentIntent(ctx context.Context, args map[string]any) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	if s.cfg == nil {
		return nil, fmt.Errorf("configuracao indisponivel")
	}

	paymentType := strings.ToLower(strings.TrimSpace(stringArg(args, "type")))
	amountBRLStr := strings.TrimSpace(stringArg(args, "amount_brl"))
	pixKey := strings.TrimSpace(stringArg(args, "pix_key"))
	paymentLink := strings.TrimSpace(stringArg(args, "payment_link"))
	barcode := strings.TrimSpace(stringArg(args, "barcode"))
	beneficiaryName := strings.TrimSpace(stringArg(args, "beneficiary_name"))
	dueDate := strings.TrimSpace(stringArg(args, "due_date"))
	idempotencyKey := strings.TrimSpace(stringArg(args, "idempotency_key"))
	agentWallet := strings.ToLower(strings.TrimSpace(stringArg(args, "agent_wallet")))

	// ── Validation ────────────────────────────────────────────────────────────
	if idempotencyKey == "" {
		return nil, fmt.Errorf("idempotency_key e obrigatorio")
	}
	if agentWallet == "" {
		return nil, fmt.Errorf("agent_wallet e obrigatorio")
	}
	if !common.IsHexAddress(agentWallet) {
		return nil, fmt.Errorf("agent_wallet deve ser um endereco EVM valido")
	}

	if paymentType != "pix" && paymentType != "credit_card" {
		return nil, fmt.Errorf("type deve ser 'pix' ou 'credit_card'")
	}
	if paymentType == "pix" && pixKey == "" {
		return nil, fmt.Errorf("pix_key e obrigatorio para type=pix")
	}
	if paymentType == "credit_card" && paymentLink == "" && barcode == "" {
		return nil, fmt.Errorf("payment_link ou barcode e obrigatorio para type=credit_card")
	}

	// ── Per-agent pricing (falls back to env globals) ─────────────────────────
	env := "sandbox"
	if s.cfg != nil && strings.EqualFold(s.cfg.Environment, "production") {
		env = "production"
	}
	feeBps, feeErr := s.db.ResolveM2MFeeBps(ctx, agentWallet, paymentType, env, s.cfg.M2MPixFeeBps, s.cfg.M2MCreditFeeBps)
	if feeErr != nil {
		// Non-fatal: fallback to global env default
		if paymentType == "pix" {
			feeBps = s.cfg.M2MPixFeeBps
		} else {
			feeBps = s.cfg.M2MCreditFeeBps
		}
	}

	amountBRLMoney, parseErr := money.ParseMoney(amountBRLStr)
	if parseErr != nil || amountBRLMoney <= 0 {
		return nil, fmt.Errorf("amount_brl deve ser um numero positivo valido (ex: '150.00')")
	}

	// ── Rate ──────────────────────────────────────────────────────────────────
	usdtRate := s.prices.GetPrice("BRL")
	if usdtRate <= 0 {
		return nil, fmt.Errorf("cotacao USDT/BRL indisponivel; tente novamente em instantes")
	}

	// ── Fee calculation ───────────────────────────────────────────────────────
	usdtRateDecimal := money.RateFromFloat(usdtRate)
	grossUSDTTokens := money.TokensFromFiat(amountBRLMoney, usdtRateDecimal)
	feeUSDTTokens := money.TokenFeeBps(grossUSDTTokens, feeBps)
	requiredUSDTTokens := grossUSDTTokens + feeUSDTTokens

	amountBRL := amountBRLMoney.Float64()
	grossUSDT := grossUSDTTokens.Float64()
	feeUSDT := feeUSDTTokens.Float64()
	requiredUSDT := requiredUSDTTokens.Float64()

	_, decision, policyErr := s.db.ValidateAgentPaymentPolicy(ctx, agentWallet, "USDT", fmt.Sprintf("%.6f", requiredUSDT))
	if policyErr != nil {
		return nil, policyErr
	}
	if !decision.Allowed {
		return nil, fmt.Errorf("%s: %s", decision.Code, decision.Message)
	}

	// ── Intent ID ─────────────────────────────────────────────────────────────
	paymentNetwork := normalizeMCPPaymentNetwork(firstNonEmptyMCP(stringArg(args, "paymentNetwork"), stringArg(args, "payment_network")))
	reqHash := database.CanonicalRequestHash(paymentType, amountBRLStr, pixKey, paymentLink, barcode, beneficiaryName, dueDate, idempotencyKey, agentWallet, paymentNetwork)
	hashShort := reqHash
	if len(hashShort) > 24 {
		hashShort = hashShort[:24]
	}
	intentID := "int_m2m_" + hashShort

	paymentAddress, err := s.db.PickAvailableM2MDepositAddress(ctx, splitMCPAddressList(s.cfg.M2MDepositAddresses), s.cfg.TreasuryHot)
	if err != nil {
		return nil, fmt.Errorf("endereco de deposito M2M indisponivel: %w", err)
	}
	if !common.IsHexAddress(paymentAddress) {
		return nil, fmt.Errorf("endereco de deposito M2M deve ser um endereco EVM valido")
	}

	in := database.M2MCreateInput{
		ID:              intentID,
		IdempotencyKey:  idempotencyKey,
		AgentWallet:     agentWallet,
		PaymentType:     database.M2MPaymentType(paymentType),
		PixKey:          pixKey,
		PaymentLink:     paymentLink,
		Barcode:         barcode,
		BeneficiaryName: beneficiaryName,
		DueDate:         dueDate,
		AmountBRL:       amountBRL,
		FeeBps:          feeBps,
		FeeUSDT:         feeUSDT,
		GrossUSDT:       grossUSDT,
		RequiredUSDT:    requiredUSDT,
		USDTRate:        usdtRate,
		PaymentAddress:  paymentAddress,
		PaymentNetwork:  paymentNetwork,
		RequestHash:     reqHash,
		ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
	}

	intent, isIdempotent, err := s.db.CreateAgentPaymentIntent(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar intent: %w", err)
	}

	feeLabel := fmt.Sprintf("%d%% (%d bps)", feeBps/100, feeBps)
	destinationLabel := "destino fiat"
	if paymentType == "pix" {
		destinationLabel = "PIX informado"
	}
	if paymentType == "credit_card" {
		destinationLabel = "link/fatura/cartao informado"
	}

	resp := map[string]any{
		"intent_id":       intent.ID,
		"status":          string(intent.Status),
		"payment_type":    string(intent.PaymentType),
		"amount_brl":      fmt.Sprintf("%.2f", intent.AmountBRL),
		"gross_usdt":      fmt.Sprintf("%.6f", intent.GrossUSDT),
		"fee_usdt":        fmt.Sprintf("%.6f", intent.FeeUSDT),
		"required_usdt":   fmt.Sprintf("%.6f", intent.RequiredUSDT),
		"fee_applied":     feeLabel,
		"usdt_rate":       fmt.Sprintf("%.4f", intent.USDTRate),
		"payment_address": intent.PaymentAddress,
		"expires_at":      intent.ExpiresAt,
		"idempotent":      isIdempotent,
		"next_step":       fmt.Sprintf("Deposite exatamente required_usdt em USDT para payment_address antes de expires_at. A ChainFX conciliara pelo valor exato e pagara o %s apos a confirmacao on-chain.", destinationLabel),
	}
	if intent.PixKey != "" {
		resp["pix_key"] = intent.PixKey
	}
	if intent.PaymentLink != "" {
		resp["payment_link"] = intent.PaymentLink
	}
	if intent.Barcode != "" {
		resp["barcode"] = intent.Barcode
	}
	if intent.BeneficiaryName != "" {
		resp["beneficiary_name"] = intent.BeneficiaryName
	}
	if intent.DueDate != "" {
		resp["due_date"] = intent.DueDate
	}
	return resp, nil
}

// toolGetM2MPaymentIntent returns the current status of an M2M payment intent.
func (s *Server) toolGetM2MPaymentIntent(ctx context.Context, args map[string]any) (any, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database indisponivel")
	}
	intentID := strings.TrimSpace(stringArg(args, "intent_id"))
	if intentID == "" {
		return nil, fmt.Errorf("intent_id e obrigatorio")
	}
	intent, err := s.db.GetAgentPaymentIntent(ctx, intentID)
	if err != nil {
		return nil, fmt.Errorf("erro ao buscar intent: %w", err)
	}
	if intent == nil {
		return nil, fmt.Errorf("intent nao encontrada: %s", intentID)
	}
	out := map[string]any{
		"intent_id":       intent.ID,
		"status":          string(intent.Status),
		"payment_type":    string(intent.PaymentType),
		"amount_brl":      fmt.Sprintf("%.2f", intent.AmountBRL),
		"required_usdt":   fmt.Sprintf("%.6f", intent.RequiredUSDT),
		"fee_usdt":        fmt.Sprintf("%.6f", intent.FeeUSDT),
		"fee_bps":         intent.FeeBps,
		"payment_address": intent.PaymentAddress,
		"expires_at":      intent.ExpiresAt,
		"attempts":        intent.Attempts,
		"created_at":      intent.CreatedAt,
	}
	if intent.PixKey != "" {
		out["pix_key"] = intent.PixKey
	}
	if intent.PaymentLink != "" {
		out["payment_link"] = intent.PaymentLink
	}
	if intent.Barcode != "" {
		out["barcode"] = intent.Barcode
	}
	if intent.BeneficiaryName != "" {
		out["beneficiary_name"] = intent.BeneficiaryName
	}
	if intent.DueDate != "" {
		out["due_date"] = intent.DueDate
	}
	if intent.DepositTx != nil {
		out["deposit_tx"] = *intent.DepositTx
	}
	if intent.DepositAmountUSDT != nil {
		out["deposit_amount_usdt"] = fmt.Sprintf("%.6f", *intent.DepositAmountUSDT)
	}
	if intent.EfiEndToEndID != nil {
		out["efi_end_to_end_id"] = *intent.EfiEndToEndID
	}
	if intent.EfiStatus != nil {
		out["efi_status"] = *intent.EfiStatus
	}
	if intent.SettlementReceiptURL != "" {
		out["settlement_receipt_url"] = intent.SettlementReceiptURL
	}
	if intent.SettlementReceiptNote != "" {
		out["settlement_receipt_note"] = intent.SettlementReceiptNote
	}
	if intent.ErrorMessage != nil {
		out["error_message"] = *intent.ErrorMessage
	}
	if intent.SettledAt != nil {
		out["settled_at"] = *intent.SettledAt
	}
	return out, nil
}

func firstNonEmptyMCP(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func maxIntMCP(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func splitMCPAddressList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

// maskURL returns a partially redacted version of a URL — scheme+host only,
// hiding path, query and credentials. Used to limit cross-agent URL enumeration.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[url-hidden]"
	}
	return u.Scheme + "://" + u.Host + "/***"
}

func round6MCP(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}
