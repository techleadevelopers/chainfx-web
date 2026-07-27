package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	agentGatewayFeeBps = 600
	accessQuoteTTL     = 15 * time.Minute
)

var erc20TransferTopic = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

func (s *Server) handleAIServicesWellKnown(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "well-known-ai-services:"+base, time.Minute, func() (any, error) {
		return map[string]any{
			"name":        "ChainFX Agent Liquidity Rail",
			"version":     "1.0",
			"description": "AI agents can acquire API access, execute BSC/Polygon stablecoin liquidity trades, and create M2M PIX or credit-card payment intents funded with USDT.",
			"capabilities": []string{
				"crypto_purchase",
				"crypto_sale",
				"stablecoin_exchange",
				"agent_payments",
				"api_access_purchase",
				"marketplace_api_purchase",
				"mcp_tools",
				"agent_capability_graph",
				"capability_compositions",
				"agent_planner",
			},
			"networks": s.stablecoinPaymentNetworks(),
			"api": map[string]string{
				"baseUrl":      base + "/agent/v1",
				"capabilities": base + "/agent/v1/capabilities",
				"openapi":      base + "/openapi.json",
			},
			"authentication": map[string]any{
				"current": "bearer_api_key_for_mcp_and_developer_routes",
				"agentTrade": []string{
					"wallet_address",
					"request_hash",
					"nonce",
					"idempotency_key",
					"onchain_payment_receipt",
				},
				"planned": "wallet_signature_headers: X-Agent-Wallet, X-Agent-Timestamp, X-Agent-Nonce, X-Agent-Signature",
			},
			"payment": map[string]any{
				"asset":          "USDT",
				"defaultNetwork": "BSC",
				"networks":       s.stablecoinPaymentNetworks(),
				"gatewayFeeBps":  agentGatewayFeeBps,
				"gatewayFeeNote": "ChainFX keeps 6%; providers receive 94%. Example: 10 USDT -> ChainFX 0.60, provider 9.40.",
			},
			"discovery": map[string]string{
				"openapi":       base + "/openapi.json",
				"mcp":           base + "/mcp/initialize",
				"x402":          base + "/.well-known/x402.json",
				"marketplace":   base + "/marketplace/apis",
				"capabilities":  base + "/marketplace/capabilities",
				"products":      base + "/marketplace/products",
				"agentProfile":  base + "/agent/v1/capabilities",
				"agentCard":     base + "/.well-known/agent-card.json",
				"policy":        base + "/.well-known/agent-policy.json",
				"graph":         base + "/.well-known/capability-graph.json",
				"compositions":  base + "/.well-known/capability-compositions.json",
				"graphRegistry": base + "/.well-known/capability-graph-registry.json",
				"planner":       base + "/agent/v1/plans",
				"llms":          base + "/llms.txt",
			},
			"agentLiquidityRail": map[string]any{
				"quote":         base + "/agent/v1/trade/quote",
				"execute":       base + "/agent/v1/trade/execute",
				"assets":        base + "/agent/v1/assets",
				"supportedFlow": "enabled BSC/Polygon stablecoin pairs with different symbols",
				"feeBps":        agentGatewayFeeBps,
			},
			"agentPayments": map[string]any{
				"create":                base + "/agent/v1/pay",
				"status":                base + "/agent/v1/pay/{id}",
				"types":                 []string{"pix", "credit_card"},
				"fundingAsset":          "USDT",
				"defaultFundingNetwork": "BSC",
				"fundingNetworks":       s.stablecoinPaymentNetworks(),
				"feesBps":               map[string]int{"pix": s.cfg.M2MPixFeeBps, "credit_card": s.cfg.M2MCreditFeeBps},
				"flow":                  "agent creates intent -> deposits required_usdt to payment_address -> ChainFX settles PIX/card recipient",
			},
		}, nil
	})
}

func (s *Server) handleAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	assets, err := s.agentTradeAssets(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":        "ChainFX Agent Payment Gateway",
		"description": "Machine-readable capabilities for autonomous agents, bots and API-to-API systems.",
		"version":     "1.0",
		"capabilities": []map[string]any{{
			"id":          "stablecoin_exchange",
			"description": "Swap enabled BSC/Polygon stablecoin symbols using ChainFX liquidity and treasury settlement.",
			"quote":       base + "/agent/v1/trade/quote",
			"execute":     base + "/agent/v1/trade/execute",
			"status":      base + "/agent/v1/trade/{id}",
			"assets":      base + "/agent/v1/assets",
		}, {
			"id":          "agent_payments",
			"name":        "agent_payments",
			"status":      "available",
			"version":     "v1",
			"description": "Create M2M payment intents for PIX or credit-card bills. The agent funds the intent with BSC or Polygon USDT and ChainFX settles the fiat recipient.",
			"create":      base + "/agent/v1/pay",
			"statusUrl":   base + "/agent/v1/pay/{id}",
			"types":       []string{"pix", "credit_card"},
			"feesBps":     map[string]int{"pix": s.cfg.M2MPixFeeBps, "credit_card": s.cfg.M2MCreditFeeBps},
		}, {
			"id":          "marketplace_api_purchase",
			"name":        "marketplace_api_purchase",
			"status":      "available",
			"version":     "v1",
			"description": "Buy premium API products and digital capabilities with BSC or Polygon USDT/USDC payment intents.",
			"products":    base + "/marketplace/products",
			"purchase":    base + "/marketplace/purchase",
			"execute":     base + "/marketplace/purchase/{id}/execute",
			"usage":       base + "/marketplace/usage",
		}, {
			"id":          "capability_exchange",
			"name":        "capability_exchange",
			"status":      "available",
			"version":     "v1",
			"description": "Discover, buy and meter digital capabilities instead of binding agents to individual providers.",
			"catalog":     base + "/marketplace/capabilities",
			"purchase":    base + "/marketplace/capabilities/{id}/purchase",
			"usage":       base + "/marketplace/capabilities/{id}/usage",
			"execute":     base + "/agent/v1/capabilities/{capability}/execute",
		}, {
			"id":          "agent_connect",
			"name":        "agent_connect",
			"status":      "available",
			"version":     "v1",
			"description": "Create a ChainFX agent identity and API credential without buying a separate passport product.",
			"connect":     base + "/agent/connect",
		}, {
			"id":          "api_access_purchase",
			"status":      "available",
			"description": "Buy temporary API/MCP access with BSC USDT and receive metered quota.",
			"catalog":     base + "/marketplace/apis",
			"quote":       base + "/v1/access/quote",
			"purchase":    base + "/v1/access/purchase",
			"meter":       base + "/v1/meter/usage",
		}, {
			"id":          "mcp_tools",
			"description": "Use ChainFX tools through MCP after API key or access-token authorization.",
			"initialize":  base + "/mcp/initialize",
		}},
		"tradeLifecycle": []string{
			"discover_capabilities",
			"list_assets",
			"create_trade_intent",
			"pay_onchain_to_payment_address",
			"submit_payment_tx",
			"chainfx_verifies_receipt",
			"chainfx_settles_receive_asset",
			"check_trade_status",
		},
		"marketplaceLifecycle": []string{
			"discover_marketplace",
			"list_capabilities",
			"list_products",
			"select_capability",
			"route_provider",
			"create_purchase",
			"pay_onchain",
			"submit_transaction",
			"verify_receipt",
			"receive_access_grant",
			"execute_capability",
			"mock_provider_execution",
			"consume_api",
			"report_usage",
		},
		"agentPaymentLifecycle": []string{
			"create_payment_intent",
			"quote_required_usdt",
			"deposit_usdt_to_unique_payment_address",
			"chainfx_matches_deposit_by_address",
			"settle_pix_or_credit_card_recipient",
			"poll_payment_intent_status",
		},
		"capabilityExchange": map[string]any{
			"networkName":  "ChainFX Capability Network",
			"positioning":  "economic infrastructure for autonomous agents to discover, execute, meter, bill and settle digital capabilities",
			"mcpRegistry":  map[string]any{"status": "ready_for_public_listing", "serverName": "chainfx-mcp", "publicTitle": "ChainFX Capability Network MCP"},
			"contracts":    map[string]any{"status": "available", "version": "v1", "endpoint": "/marketplace/capabilities/{id}/contract"},
			"routePreview": map[string]any{"status": "available", "endpoint": "/agent/v1/capabilities/{capability}/route"},
			"dailyCapabilities": []string{
				"semantic_memory",
				"llm_chat",
				"document_ocr",
				"payments_fx",
				"capability_discovery",
			},
			"providerAbstraction": true,
			"capabilityRouter": map[string]any{
				"status":       "available",
				"mode":         "policy_routed_hybrid",
				"metering":     "real",
				"routingModes": []string{"best_available", "cheapest", "lowest_latency", "highest_quality"},
				"policies":     []string{"provider_priority", "cost_score", "latency_ms", "quality_score", "success_rate_bps", "region", "fallback_order"},
				"fallback":     "provider_fallback_then_mock_dev",
			},
			"providerExecution": map[string]any{
				"status":  "partial_available",
				"enabled": true,
				"productionReadiness": map[string]any{
					"metering_billing_settlement": true,
					"mock_dev_fallback":           false,
					"seed_fixtures":               false,
					"provider_credentials_required_for_real_execution": []string{
						"OPENAI_API_KEY",
						"CAPABILITY_OCR_URL",
					},
				},
				"realProviders": map[string]any{
					"semantic_memory": "native_postgres",
					"llm_chat":        "openai_compatible_when_configured",
					"document_ocr":    "http_adapter_when_configured",
				},
				"fallback": "mock_dev",
			},
		},
		"features": map[string]any{
			"wallet_signature_auth": map[string]any{"status": "planned", "enabled": false},
			"provider_auto_payout":  map[string]any{"status": "planned", "enabled": false},
		},
		"security": map[string]any{
			"required": []string{
				"request_hash_bound_to_wallet_assets_contracts_amounts_nonce_expiry",
				"idempotency_key",
				"unique_tx_hash",
				"bsc_erc20_receipt_verification",
				"database_lock_before_settlement",
				"isolated_signer_for_treasury_transfers",
			},
			"plannedAgentIdentity": []string{
				"wallet_signature_headers",
				"used_nonce_registry",
				"agent_limits",
				"wallet_reputation_tiers",
			},
		},
		"fees": map[string]any{
			"defaultFeeBps": agentGatewayFeeBps,
			"model":         "fee is charged on operation value and may be higher per asset in agent_supported_assets",
		},
		"assets": assets,
		"docs": map[string]string{
			"wellKnown": base + "/.well-known/ai-services.json",
			"x402":      base + "/.well-known/x402.json",
			"openapi":   base + "/openapi.json",
			"llms":      base + "/llms.txt",
		},
	})
}

func (s *Server) handleX402WellKnown(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	accepts := []map[string]any{{
		"asset":   "USDT",
		"network": "BSC",
		"chainId": 56,
		"address": s.accessPaymentAddress(),
	}}
	if s.cfg != nil && strings.TrimSpace(s.cfg.PolygonRpcUrls) != "" && common.IsHexAddress(s.cfg.PolygonUsdtContract) {
		accepts = append(accepts, map[string]any{
			"asset":    "USDT",
			"network":  "POLYGON",
			"chainId":  137,
			"address":  s.accessPaymentAddress(),
			"contract": strings.ToLower(s.cfg.PolygonUsdtContract),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"x402Version": "chainfx-m2m-0.1",
		"seller":      "ChainFX",
		"accepts":     accepts,
		"resources": []map[string]any{{
			"path":         "/x402/capabilities/{capability}/execute",
			"description":  "Execute digital capabilities with HTTP 402 payment challenge. First call returns payment_requirements; replay with PAYMENT header after BSC or Polygon stablecoin transfer.",
			"capabilities": base + "/marketplace/capabilities",
			"pricing":      "exact",
			"replayHeader": "PAYMENT",
		}, {
			"path":        "/v1/access/purchase",
			"description": "Buy temporary API access with BSC USDT after receiving a quote.",
			"quote":       base + "/v1/access/quote",
		}, {
			"path":        "/agent/v1/trade/execute",
			"description": "Machine-to-machine liquidity rail: agent pays one enabled BSC/Polygon stablecoin and receives another enabled stablecoin on the same network.",
			"quote":       base + "/agent/v1/trade/quote",
			"assets":      base + "/agent/v1/assets",
		}},
		"security": []string{"request_hash_bound_to_product_wallet_price_quota_nonce", "idempotency_required", "quota_debited_before_execution", "bsc_receipt_verification_required"},
	})
}

func (s *Server) handleLLMSTxt(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `# ChainFX Agent API Marketplace

ChainFX provides machine-to-machine API payments for AI agents.
Agents can buy temporary API credits with USDT on BSC and execute stablecoin liquidity trades without PIX.

Core endpoints:
- %s/.well-known/ai-services.json
- %s/.well-known/x402.json
- %s/openapi.json
- %s/mcp/initialize
- %s/marketplace/apis
- %s/marketplace/capabilities
- %s/marketplace/products
- %s/marketplace/purchase
- %s/agent/connect
- %s/agent/v1/capabilities/{capability}/execute
- %s/v1/access/quote
- %s/v1/access/purchase
- %s/agent/v1/capabilities
- %s/agent/v1/assets
- %s/agent/v1/trade/quote
- %s/agent/v1/trade/execute
- %s/agent/v1/pay

Positioning:
AI agents can acquire liquidity and API access with stablecoins on BSC.
Machine-to-machine payment rail for stablecoin trades and agent-funded PIX/card payment intents.
No manual onboarding.
`, base, base, base, base, base, base, base, base, base, base, base, base, base, base, base, base, base)
}

func (s *Server) handleRobotsTxt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("User-agent: *\nAllow: /\nSitemap: /sitemap.xml\n"))
}

func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/developers</loc></url>
  <url><loc>%s/openapi.json</loc></url>
  <url><loc>%s/.well-known/ai-services.json</loc></url>
  <url><loc>%s/.well-known/x402.json</loc></url>
  <url><loc>%s/marketplace/apis</loc></url>
  <url><loc>%s/marketplace/capabilities</loc></url>
  <url><loc>%s/marketplace/products</loc></url>
  <url><loc>%s/agent/connect</loc></url>
  <url><loc>%s/agent/v1/capabilities/document_ocr/execute</loc></url>
  <url><loc>%s/agent/v1/capabilities</loc></url>
  <url><loc>%s/agent/v1/assets</loc></url>
  <url><loc>%s/agent/v1/trade/quote</loc></url>
</urlset>`, base, base, base, base, base, base, base, base, base, base, base, base)
}

func (s *Server) handleMarketplaceAPIs(w http.ResponseWriter, r *http.Request) {
	products, err := s.db.ListAPIProducts(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"products":       products,
		"gatewayFeeBps":  agentGatewayFeeBps,
		"gatewayFeeText": "6% ChainFX fee. Example: 10 USDT purchase -> ChainFX 0.60 USDT, provider 9.40 USDT.",
	})
}

func (s *Server) handleMarketplaceAPI(w http.ResponseWriter, r *http.Request) {
	product, err := s.db.GetAPIProduct(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if product == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "produto nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, product)
}

func (s *Server) handleAccessQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProductID   string `json:"productId"`
		BuyerWallet string `json:"buyerWallet"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	req.ProductID = strings.TrimSpace(req.ProductID)
	req.BuyerWallet = strings.ToLower(strings.TrimSpace(req.BuyerWallet))
	if req.ProductID == "" || !common.IsHexAddress(req.BuyerWallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "productId e buyerWallet BSC validos sao obrigatorios"})
		return
	}
	nonce := "aq_" + strings.ReplaceAll(database.NewID(), "-", "")
	paymentAddress := s.accessPaymentAddress()
	requestHash := accessRequestHash(req.ProductID, req.BuyerWallet, paymentAddress, nonce)
	payment, product, err := s.db.CreateAccessQuote(r.Context(), database.AccessQuoteInput{
		ProductID:      req.ProductID,
		BuyerWallet:    req.BuyerWallet,
		PaymentAddress: paymentAddress,
		Nonce:          nonce,
		RequestHash:    requestHash,
		ChainFXFeeBps:  agentGatewayFeeBps,
		QuoteTTL:       accessQuoteTTL,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("X-Idempotency-Key")),
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, accessQuoteResponse(payment, product, publicBaseURL(r)))
}

func (s *Server) handleAccessPurchase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuoteID        string `json:"quoteId"`
		TxHash         string `json:"txHash"`
		BuyerWallet    string `json:"buyerWallet"`
		RequestHash    string `json:"requestHash"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	req.QuoteID = strings.TrimSpace(req.QuoteID)
	req.TxHash = strings.ToLower(strings.TrimSpace(req.TxHash))
	req.BuyerWallet = strings.ToLower(strings.TrimSpace(req.BuyerWallet))
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	if req.QuoteID == "" || req.TxHash == "" || req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "quoteId, txHash e idempotencyKey sao obrigatorios"})
		return
	}
	payment, err := s.db.GetAccessPayment(r.Context(), req.QuoteID)
	if err != nil {
		writeError(w, err)
		return
	}
	if payment == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "quote nao encontrado"})
		return
	}
	if req.BuyerWallet != "" && req.BuyerWallet != payment.BuyerWallet {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "buyerWallet nao confere com quote"})
		return
	}
	if req.RequestHash != "" && !strings.EqualFold(req.RequestHash, payment.RequestHash) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "requestHash nao confere com quote"})
		return
	}
	if err := s.verifyAccessPaymentTx(r.Context(), payment, req.TxHash); err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": err.Error(), "status": "payment_not_verified"})
		return
	}
	result, err := s.db.ConfirmAccessPaymentAndGrant(r.Context(), payment.ID, req.TxHash, req.IdempotencyKey)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, accessGrantResponse(result, publicBaseURL(r)))
}

func (s *Server) handleAccessGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	payment, err := s.db.GetAccessPayment(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if payment != nil {
		writeJSON(w, http.StatusOK, map[string]any{"payment": payment})
		return
	}
	grant, err := s.db.GetAccessGrant(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if grant == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "access nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": grant})
}

func (s *Server) handleMeterUsage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Units          int            `json:"units"`
		RequestHash    string         `json:"requestHash"`
		IdempotencyKey string         `json:"idempotencyKey"`
		Metadata       map[string]any `json:"metadata"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	token := accessBearerToken(r)
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	if token == "" || req.IdempotencyKey == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Authorization Bearer ak_live_agent_... e idempotencyKey sao obrigatorios"})
		return
	}
	grant, duplicate, err := s.db.ConsumeAccessUsage(r.Context(), token, req.Units, req.RequestHash, req.IdempotencyKey, req.Metadata)
	if err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": duplicate, "grant": grant})
}

func (s *Server) verifyAccessPaymentTx(ctx context.Context, payment *database.APIPayment, txHash string) error {
	rpcURL := firstCSV(s.cfg.BscRpcUrls)
	if rpcURL == "" {
		return fmt.Errorf("BSC_RPC_URLS nao configurado para verificar pagamento")
	}
	if !common.IsHexAddress(payment.BuyerWallet) || !common.IsHexAddress(payment.PaymentAddress) {
		return fmt.Errorf("wallet ou paymentAddress invalido")
	}
	tokenAddr := strings.TrimSpace(s.cfg.BscUsdtContract)
	if tokenAddr == "" || !common.IsHexAddress(tokenAddr) {
		return fmt.Errorf("BSC_USDT_CONTRACT nao configurado")
	}
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return fmt.Errorf("falha ao conectar RPC BSC: %w", err)
	}
	defer client.Close()
	receipt, err := client.TransactionReceipt(ctx, common.HexToHash(txHash))
	if err != nil {
		if err == ethereum.NotFound {
			return fmt.Errorf("tx nao encontrada na BSC")
		}
		return fmt.Errorf("falha ao buscar receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("tx BSC sem sucesso")
	}
	expected := usdtAmountToWei(payment.AmountUSDT)
	if expected.Sign() <= 0 {
		return fmt.Errorf("valor esperado invalido")
	}
	from := common.HexToAddress(payment.BuyerWallet)
	to := common.HexToAddress(payment.PaymentAddress)
	token := common.HexToAddress(tokenAddr)
	for _, lg := range receipt.Logs {
		if lg.Address != token || len(lg.Topics) < 3 || lg.Topics[0] != erc20TransferTopic {
			continue
		}
		logFrom := topicAddress(lg.Topics[1])
		logTo := topicAddress(lg.Topics[2])
		if logFrom != from || logTo != to {
			continue
		}
		amount := new(big.Int).SetBytes(lg.Data)
		if amount.Cmp(expected) >= 0 {
			return nil
		}
	}
	return fmt.Errorf("tx nao contem Transfer USDT suficiente para o quote")
}

func accessQuoteResponse(payment *database.APIPayment, product *database.APIProduct, base string) map[string]any {
	return map[string]any{
		"quoteId":            payment.ID,
		"productId":          payment.ProductID,
		"product":            product,
		"price":              fmt.Sprintf("%.2f", payment.AmountUSDT),
		"asset":              payment.Asset,
		"network":            payment.Network,
		"quota":              product.QuotaUnits,
		"expiresIn":          fmt.Sprintf("%ds", product.DurationSeconds),
		"paymentAddress":     payment.PaymentAddress,
		"memo":               payment.Memo,
		"nonce":              payment.Nonce,
		"requestHash":        payment.RequestHash,
		"quoteExpiresAt":     payment.QuoteExpiresAt,
		"gatewayFeeBps":      agentGatewayFeeBps,
		"chainfxFeeUsdt":     payment.ChainFXFeeUSDT,
		"providerAmountUsdt": payment.ProviderAmountUSDT,
		"purchaseUrl":        base + "/v1/access/purchase",
		"security":           []string{"pay exact quote before expiry", "tx must be BSC USDT Transfer from buyerWallet to paymentAddress", "requestHash binds product/wallet/address/nonce"},
	}
}

func accessGrantResponse(result *database.AccessGrantResult, base string) map[string]any {
	return map[string]any{
		"payment":     result.Payment,
		"grant":       result.Grant,
		"accessToken": result.AccessToken,
		"quota":       result.Grant.QuotaTotal,
		"remaining":   result.Grant.QuotaRemaining,
		"expiresAt":   result.Grant.ExpiresAt,
		"mcpUrl":      base + "/mcp",
		"openapiUrl":  base + "/openapi.json",
		"meterUrl":    base + "/v1/meter/usage",
	}
}

func accessRequestHash(productID, buyerWallet, paymentAddress, nonce string) string {
	raw := strings.Join([]string{
		strings.TrimSpace(productID),
		strings.ToLower(strings.TrimSpace(buyerWallet)),
		strings.ToLower(strings.TrimSpace(paymentAddress)),
		strings.TrimSpace(nonce),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *Server) accessPaymentAddress() string {
	return strings.ToLower(firstNonEmpty(s.cfg.TreasuryHot, s.cfg.SellWalletAddress))
}

func publicBaseURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil && strings.HasPrefix(r.Host, "localhost") {
		scheme = "http"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	}
	return scheme + "://" + r.Host
}

func accessBearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return strings.TrimSpace(r.URL.Query().Get("accessToken"))
}

func firstCSV(value string) string {
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func topicAddress(topic common.Hash) common.Address {
	raw := topic.Bytes()
	if len(raw) >= 20 {
		return common.BytesToAddress(raw[len(raw)-20:])
	}
	return common.Address{}
}

func usdtAmountToWei(amount float64) *big.Int {
	text := fmt.Sprintf("%.8f", amount)
	parts := strings.SplitN(text, ".", 2)
	whole := new(big.Int)
	whole.SetString(parts[0], 10)
	whole.Mul(whole, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	frac := big.NewInt(0)
	if len(parts) == 2 {
		f := parts[1]
		if len(f) > 18 {
			f = f[:18]
		}
		f += strings.Repeat("0", 18-len(f))
		frac.SetString(strings.TrimLeft(firstNonEmpty(f, "0"), "0"), 10)
	}
	return whole.Add(whole, frac)
}

func durationDays(seconds int) string {
	days := seconds / 86400
	if days <= 0 {
		return strconv.Itoa(seconds) + "s"
	}
	return strconv.Itoa(days) + "d"
}
