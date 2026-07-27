package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/money"

	"github.com/ethereum/go-ethereum/common"
)

const chainFXAgentPayDescription = "A2A payment agent that lets autonomous agents pay PIX recipients and card bills by funding intents with USDT on BSC."

type a2aRequest struct {
	ID        any            `json:"id,omitempty"`
	JSONRPC   string         `json:"jsonrpc,omitempty"`
	Method    string         `json:"method,omitempty"`
	Skill     string         `json:"skill,omitempty"`
	Action    string         `json:"action,omitempty"`
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Params    map[string]any `json:"params,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
}

type captureResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *captureResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *captureResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (s *Server) handleA2AAgentCard(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "a2a-agent-card:"+base, time.Minute, func() (any, error) {
		return s.a2aAgentCard(base), nil
	})
}

func (s *Server) handleAgentPayOnboarding(w http.ResponseWriter, r *http.Request) {
	base := publicBaseURL(r)
	s.writeCachedDiscoveryJSON(w, r, "agent-pay:"+base, time.Minute, func() (any, error) {
		return map[string]any{
			"product":         "ChainFX Agent Pay",
			"what_it_does":    "Pays PIX recipients and card bills using USDT funded by autonomous agents.",
			"positioning":     chainFXAgentPayDescription,
			"funding":         "USDT on BSC",
			"payment_methods": []string{"pix", "credit_card"},
			"create_intent":   base + "/agent/v1/pay",
			"status":          base + "/agent/v1/pay/{id}",
			"mcp":             base + "/mcp/initialize",
			"a2a":             base + "/a2a",
			"a2a_tasks":       base + "/a2a/tasks",
			"a2a_task_status": base + "/a2a/tasks/{id}",
			"a2a_task_events": base + "/a2a/tasks/{id}/events",
			"x402_execute":    base + "/x402/capabilities/{capability}/execute",
			"compositions":    base + "/.well-known/capability-compositions.json",
			"graph_registry":  base + "/.well-known/capability-graph-registry.json",
			"planner":         base + "/agent/v1/plans",
			"task_states":     []string{"submitted", "working", "input_required", "completed", "failed", "canceled", "rejected"},
			"agent_card":      base + "/.well-known/agent-card.json",
			"openapi":         base + "/openapi.json",
		}, nil
	})
}

func (s *Server) handleA2A(w http.ResponseWriter, r *http.Request) {
	var req a2aRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}

	action := normalizeA2AAction(firstNonEmpty(req.Skill, req.Action, req.Name, req.Method))
	args := firstMap(req.Arguments, req.Params, req.Input)
	started := time.Now()
	inputHash := hashAny(map[string]any{"skill": action, "arguments": args})
	episodeStatus := "completed"
	episodeStatusCode := http.StatusOK
	var episodeError map[string]any
	var episodeResult any
	defer func() {
		s.recordAgentEpisode(agentEpisode{
			AgentID:          "chainfx-agent-pay",
			Protocol:         "a2a",
			Skill:            action,
			InputHash:        inputHash,
			PaymentIntentID:  paymentIntentIDFromAny(episodeResult),
			SettlementStatus: settlementStatusFromAny(episodeResult),
			LatencyMS:        time.Since(started).Milliseconds(),
			ResultHash:       hashAny(episodeResult),
			ErrorTree:        episodeError,
			Status:           episodeStatus,
			StatusCode:       episodeStatusCode,
			CreatedAt:        started.UTC(),
		})
	}()

	var (
		result     any
		status     int
		errPayload map[string]any
	)
	result, status, errPayload = s.executeA2AAction(r, action, args)
	if errPayload != nil {
		episodeStatus = "failed"
		episodeStatusCode = status
		episodeError = errPayload
		writeJSON(w, status, a2aErrorEnvelope(req, status, errPayload))
		return
	}

	episodeStatusCode = status
	episodeResult = result
	writeJSON(w, status, a2aSuccessEnvelope(req, action, result))
}

func (s *Server) executeA2AAction(r *http.Request, action string, args map[string]any) (any, int, map[string]any) {
	switch action {
	case "list_supported_payment_methods":
		return s.a2aSupportedPaymentMethods(publicBaseURL(r)), http.StatusOK, nil
	case "capability_exchange":
		result, status := s.a2aCapabilityExchange(r, args)
		return result, status, nil
	case "stablecoin_exchange":
		return s.a2aStablecoinExchange(r, args)
	case "semantic_memory", "document_ocr", "llm_chat":
		return s.a2aCapabilityDetails(r, action)
	case "quote_required_usdt":
		return s.a2aQuoteRequiredUSDT(r, args)
	case "pay_pix_with_usdt":
		return s.a2aCreatePaymentIntent(r, "pix", args)
	case "pay_card_bill_with_usdt":
		return s.a2aCreatePaymentIntent(r, "credit_card", args)
	case "get_payment_status":
		return s.a2aGetPaymentStatus(r, args)
	default:
		return nil, http.StatusBadRequest, map[string]any{
			"error":            "unsupported A2A skill",
			"supported_skills": supportedA2ASkills(),
		}
	}
}

func (s *Server) a2aAgentCard(base string) map[string]any {
	return map[string]any{
		"name":        "ChainFX Agent Pay",
		"description": "Stablecoin-funded real-world payments for autonomous agents.",
		"url":         base + "/a2a",
		"version":     "1.0.0",
		"protocol":    "a2a",
		"status":      "available",
		"provider": map[string]any{
			"organization": "ChainFX",
			"url":          "https://www.chainfx.store",
		},
		"defaultInputModes":  []string{"application/json"},
		"defaultOutputModes": []string{"application/json"},
		"authentication": map[string]any{
			"type":        "bearer",
			"description": "Send Authorization: Bearer <ChainFX API key> for payment creation, quotes and private status.",
		},
		"agent_identity": s.agentIdentityMetadata(base),
		"registry": map[string]any{
			"index":        base + "/agent/v1/registries",
			"agntcy":       base + "/.well-known/agntcy.json",
			"oasf":         base + "/.well-known/oasf.json",
			"signedRecord": base + "/agent/v1/registry-records/agntcy-oasf",
			"graph":        base + "/.well-known/capability-graph-registry.json",
			"graph_api":    base + "/agent/v1/graph-registry",
			"protocols":    []string{"mcp", "a2a", "openapi", "x402", "oasf"},
		},
		"planning": map[string]any{
			"name":                 "ChainFX Planning Layer for Agent Commerce",
			"policy_discovery":     base + "/.well-known/agent-policy.json",
			"capability_graph":     base + "/.well-known/capability-graph.json",
			"capability_graph_api": base + "/agent/v1/capability-graph",
			"compositions":         base + "/.well-known/capability-compositions.json",
			"compositions_api":     base + "/agent/v1/capability-compositions",
			"planner_api":          base + "/agent/v1/plans",
			"graph_registry":       base + "/.well-known/capability-graph-registry.json",
			"graph_registry_api":   base + "/agent/v1/graph-registry",
			"graph_version":        "2.0.0",
			"planner_version":      "1.0.0",
			"policy_required_for": []string{
				"pay_pix_with_usdt",
				"pay_card_bill_with_usdt",
				"stablecoin_exchange",
				"capability_purchase",
				"capability_execution",
				"x402_capability_execution",
			},
		},
		"decision_metadata": map[string]any{
			"supported_networks":   []string{"BSC"},
			"supported_assets":     []string{"USDT", "USDC"},
			"supported_countries":  []string{"BR"},
			"payment_methods":      []string{"pix", "credit_card"},
			"auth_required":        "bearer",
			"sandbox_available":    true,
			"funding_asset":        "USDT",
			"funding_network":      "BSC",
			"settlement_countries": []string{"BR"},
			"fees_bps":             map[string]int{"pix": s.cfg.M2MPixFeeBps, "credit_card": s.cfg.M2MCreditFeeBps},
			"sla": map[string]any{
				"discovery_timeout_ms": 3000,
				"a2a_timeout_ms":       15000,
				"intent_ttl_minutes":   15,
				"settlement_note":      "PIX/card settlement starts after exact USDT deposit confirmation.",
			},
			"risk_notes": []string{
				"Deposit exactly required_usdt to payment_address on BSC.",
				"Expired intents must not be funded.",
				"Overpayment and underpayment may require manual review.",
				"Bearer API key is required for quote, payment creation and private status.",
			},
		},
		"skills": []map[string]any{
			{
				"id":          "pay_pix_with_usdt",
				"name":        "Pay PIX with USDT",
				"description": "Create a PIX payment intent funded by an autonomous agent with USDT on BSC.",
				"tags":        []string{"payments", "pix", "usdt", "bsc", "a2a"},
				"input_schema": map[string]any{
					"type":     "object",
					"required": []string{"amount_brl", "pix_key", "idempotency_key", "agent_wallet"},
					"properties": map[string]any{
						"amount_brl":       map[string]any{"type": "string", "description": "BRL amount the PIX recipient receives, e.g. 150.00"},
						"pix_key":          map[string]any{"type": "string", "description": "Destination PIX key"},
						"beneficiary_name": map[string]any{"type": "string", "description": "Optional beneficiary label"},
						"idempotency_key":  map[string]any{"type": "string", "description": "Caller-generated unique key"},
						"agent_wallet":     map[string]any{"type": "string", "description": "EVM wallet address of the paying agent"},
					},
				},
				"example": map[string]any{"skill": "pay_pix_with_usdt", "arguments": map[string]any{"amount_brl": "150.00", "pix_key": "+5511999999999", "beneficiary_name": "Recipient", "idempotency_key": "agent-pay-001", "agent_wallet": "0x830000000000000000000000000000000000019a"}},
			},
			{
				"id":          "pay_card_bill_with_usdt",
				"name":        "Pay card bill with USDT",
				"description": "Create a card bill or payment-link intent funded by USDT on BSC.",
				"tags":        []string{"payments", "credit_card", "usdt", "bsc", "a2a"},
				"input_schema": map[string]any{
					"type":     "object",
					"required": []string{"amount_brl", "idempotency_key", "agent_wallet"},
					"properties": map[string]any{
						"amount_brl":       map[string]any{"type": "string"},
						"payment_link":     map[string]any{"type": "string", "description": "Required when barcode is absent"},
						"barcode":          map[string]any{"type": "string", "description": "Required when payment_link is absent"},
						"beneficiary_name": map[string]any{"type": "string"},
						"due_date":         map[string]any{"type": "string"},
						"idempotency_key":  map[string]any{"type": "string"},
						"agent_wallet":     map[string]any{"type": "string"},
					},
				},
				"example": map[string]any{"skill": "pay_card_bill_with_usdt", "arguments": map[string]any{"amount_brl": "250.00", "payment_link": "https://issuer.example/pay/123", "beneficiary_name": "Card Issuer", "idempotency_key": "agent-card-001", "agent_wallet": "0x830000000000000000000000000000000000019a"}},
			},
			{
				"id":          "get_payment_status",
				"name":        "Get payment status",
				"description": "Read an agent payment intent lifecycle status, deposit data and settlement receipt.",
				"tags":        []string{"payments", "status", "settlement"},
				"input_schema": map[string]any{
					"type":       "object",
					"required":   []string{"intent_id"},
					"properties": map[string]any{"intent_id": map[string]any{"type": "string"}},
				},
			},
			{
				"id":          "quote_required_usdt",
				"name":        "Quote required USDT",
				"description": "Estimate gross USDT, fee and required USDT for a BRL payment amount.",
				"tags":        []string{"quote", "pricing", "usdt"},
				"input_schema": map[string]any{
					"type":     "object",
					"required": []string{"type", "amount_brl"},
					"properties": map[string]any{
						"type":         map[string]any{"type": "string", "enum": []string{"pix", "credit_card"}},
						"amount_brl":   map[string]any{"type": "string"},
						"agent_wallet": map[string]any{"type": "string"},
					},
				},
			},
			{
				"id":           "list_supported_payment_methods",
				"name":         "List supported payment methods",
				"description":  "List supported fiat payment rails and stablecoin funding rails.",
				"tags":         []string{"discovery", "payments"},
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
			{
				"id":          "stablecoin_exchange",
				"name":        "Stablecoin exchange",
				"description": "Quote BSC stablecoin exchanges such as USDT to USDC through ChainFX Agent Rail.",
				"tags":        []string{"stablecoin", "exchange", "usdt", "usdc", "bsc"},
				"input_schema": map[string]any{
					"type":     "object",
					"required": []string{"payAsset", "receiveAsset", "amount", "agent_wallet"},
					"properties": map[string]any{
						"payAsset":     map[string]any{"type": "string", "enum": []string{"USDT", "USDC"}},
						"receiveAsset": map[string]any{"type": "string", "enum": []string{"USDT", "USDC"}},
						"amount":       map[string]any{"type": "number"},
						"amountType":   map[string]any{"type": "string", "enum": []string{"pay", "receive"}},
						"agent_wallet": map[string]any{"type": "string"},
					},
				},
			},
			{
				"id":          "capability_exchange",
				"name":        "Capability exchange",
				"description": "Discover purchasable and executable capabilities available to autonomous agents.",
				"tags":        []string{"capabilities", "marketplace", "discovery"},
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"category":     map[string]any{"type": "string"},
						"capability":   map[string]any{"type": "string"},
						"paymentAsset": map[string]any{"type": "string", "enum": []string{"USDT", "USDC"}},
					},
				},
			},
			{
				"id":          "semantic_memory",
				"name":        "Semantic memory",
				"description": "Discover persistent memory operations for agent context and knowledge lookup.",
				"tags":        []string{"memory", "agent-state", "capability"},
			},
			{
				"id":          "document_ocr",
				"name":        "Document OCR",
				"description": "Discover OCR operations for extracting and structuring document text.",
				"tags":        []string{"ocr", "documents", "capability"},
			},
			{
				"id":          "llm_chat",
				"name":        "LLM chat",
				"description": "Discover provider-routed text generation, chat, summarization and classification.",
				"tags":        []string{"llm", "chat", "summarization", "capability"},
			},
		},
		"capabilities": map[string]any{
			"streaming":              true,
			"pushNotifications":      false,
			"stateTransitionHistory": true,
			"taskLifecycle":          true,
			"taskStates":             []string{"submitted", "working", "input_required", "completed", "failed", "canceled", "rejected"},
		},
		"endpoints": map[string]any{
			"a2a":            base + "/a2a",
			"a2a_tasks":      base + "/a2a/tasks",
			"a2a_task":       base + "/a2a/tasks/{id}",
			"a2a_events":     base + "/a2a/tasks/{id}/events",
			"x402":           base + "/.well-known/x402.json",
			"x402_execute":   base + "/x402/capabilities/{capability}/execute",
			"agent_pay":      base + "/agent/v1/pay",
			"status":         base + "/agent/v1/pay/{id}",
			"mcp":            base + "/mcp/initialize",
			"openapi":        base + "/openapi.json",
			"registries":     base + "/agent/v1/registries",
			"agntcy":         base + "/.well-known/agntcy.json",
			"oasf":           base + "/.well-known/oasf.json",
			"policy":         base + "/.well-known/agent-policy.json",
			"graph":          base + "/.well-known/capability-graph.json",
			"compositions":   base + "/.well-known/capability-compositions.json",
			"graph_registry": base + "/.well-known/capability-graph-registry.json",
			"planner":        base + "/agent/v1/plans",
			"onboarding":     base + "/agent-pay.json",
			"ai_services":    base + "/.well-known/ai-services.json",
		},
	}
}

func (s *Server) a2aCapabilityExchange(r *http.Request, args map[string]any) (map[string]any, int) {
	query := make([]string, 0, 3)
	if category := stringArg(args, "category"); category != "" {
		query = append(query, "category="+url.QueryEscape(category))
	}
	if capability := stringArg(args, "capability"); capability != "" {
		query = append(query, "capability="+url.QueryEscape(capability))
	}
	if paymentAsset := stringArg(args, "paymentAsset", "payment_asset"); paymentAsset != "" {
		query = append(query, "paymentAsset="+url.QueryEscape(paymentAsset))
	}
	path := "/marketplace/capabilities"
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}
	status, out := s.dispatchJSONToHandler(r, http.MethodGet, path, nil, s.handleMarketplaceCapabilities)
	return map[string]any{
		"skill":        "capability_exchange",
		"capabilities": out,
		"instructions": "Choose a capability, inspect its contract, purchase a plan, then execute with an access grant.",
	}, status
}

func (s *Server) a2aCapabilityDetails(r *http.Request, capability string) (map[string]any, int, map[string]any) {
	status, out := s.dispatchJSONToHandler(r, http.MethodGet, "/marketplace/capabilities/"+capability, nil, func(w http.ResponseWriter, req *http.Request) {
		req.SetPathValue("id", capability)
		s.handleMarketplaceCapability(w, req)
	})
	if status < 200 || status >= 300 {
		return nil, status, out
	}
	return map[string]any{
		"skill":        capability,
		"capability":   out,
		"contract_url": "/marketplace/capabilities/" + capability + "/contract",
		"next_step":    "Use capability_exchange to discover plans, then purchase and execute this capability.",
	}, status, nil
}

func (s *Server) a2aStablecoinExchange(r *http.Request, args map[string]any) (map[string]any, int, map[string]any) {
	payAsset := firstNonEmpty(stringArg(args, "payAsset", "pay_asset"), "USDT")
	receiveAsset := firstNonEmpty(stringArg(args, "receiveAsset", "receive_asset"), "USDC")
	amount := stringArg(args, "amount")
	if amount == "" {
		amount = "10"
	}
	amountFloat, err := strconv.ParseFloat(strings.ReplaceAll(amount, ",", "."), 64)
	if err != nil || amountFloat <= 0 {
		return nil, http.StatusBadRequest, map[string]any{"error": "amount must be a positive number"}
	}
	agentWallet := stringArg(args, "agentWallet", "agent_wallet", "wallet")
	if agentWallet == "" {
		return nil, http.StatusBadRequest, map[string]any{"error": "agent_wallet is required for stablecoin_exchange quote"}
	}
	payload := map[string]any{
		"payAsset":          payAsset,
		"receiveAsset":      receiveAsset,
		"amount":            amountFloat,
		"amountType":        firstNonEmpty(stringArg(args, "amountType", "amount_type"), "pay"),
		"agentWallet":       agentWallet,
		"destinationWallet": firstNonEmpty(stringArg(args, "destinationWallet", "destination_wallet"), agentWallet),
	}
	status, out := s.dispatchJSONToHandler(r, http.MethodPost, "/agent/v1/trade/quote", payload, s.handleAgentTradeQuote)
	if status < 200 || status >= 300 {
		return nil, status, out
	}
	return map[string]any{
		"skill":        "stablecoin_exchange",
		"quote":        out,
		"instructions": "Fund the returned payment intent on-chain, then execute settlement through Agent Rail.",
	}, status, nil
}

func (s *Server) a2aSupportedPaymentMethods(base string) map[string]any {
	return map[string]any{
		"payment_methods": []map[string]any{
			{"type": "pix", "funding_asset": "USDT", "funding_network": "BSC", "create_skill": "pay_pix_with_usdt", "fee_bps": s.cfg.M2MPixFeeBps},
			{"type": "credit_card", "funding_asset": "USDT", "funding_network": "BSC", "create_skill": "pay_card_bill_with_usdt", "fee_bps": s.cfg.M2MCreditFeeBps},
		},
		"status_skill": "get_payment_status",
		"quote_skill":  "quote_required_usdt",
		"rest": map[string]any{
			"create": base + "/agent/v1/pay",
			"status": base + "/agent/v1/pay/{id}",
		},
	}
}

func (s *Server) a2aCreatePaymentIntent(r *http.Request, paymentType string, args map[string]any) (map[string]any, int, map[string]any) {
	payload := map[string]any{
		"type":             paymentType,
		"amount_brl":       stringArg(args, "amount_brl", "amountBRL", "amount"),
		"pix_key":          stringArg(args, "pix_key", "pixKey"),
		"payment_link":     stringArg(args, "payment_link", "paymentLink"),
		"barcode":          stringArg(args, "barcode"),
		"beneficiary_name": stringArg(args, "beneficiary_name", "beneficiaryName"),
		"due_date":         stringArg(args, "due_date", "dueDate"),
		"idempotency_key":  stringArg(args, "idempotency_key", "idempotencyKey", "request_id", "requestId"),
		"agent_wallet":     stringArg(args, "agent_wallet", "agentWallet", "wallet"),
	}
	status, out := s.dispatchJSONToHandler(r, http.MethodPost, "/agent/v1/pay", payload, s.handleM2MCreateIntent)
	if status < 200 || status >= 300 {
		return nil, status, out
	}
	return map[string]any{
		"skill":        normalizeA2AAction(map[string]string{"pix": "pay_pix_with_usdt", "credit_card": "pay_card_bill_with_usdt"}[paymentType]),
		"status":       "completed",
		"payment":      out,
		"instructions": "Deposit required_usdt in USDT on BSC to payment_address, then call get_payment_status.",
	}, status, nil
}

func (s *Server) a2aGetPaymentStatus(r *http.Request, args map[string]any) (map[string]any, int, map[string]any) {
	id := stringArg(args, "intent_id", "intentId", "payment_id", "paymentId", "id")
	if id == "" {
		return nil, http.StatusBadRequest, map[string]any{"error": "intent_id is required"}
	}
	status, out := s.dispatchJSONToHandler(r, http.MethodGet, "/agent/v1/pay/"+id, nil, func(w http.ResponseWriter, req *http.Request) {
		req.SetPathValue("id", id)
		s.handleM2MGetIntent(w, req)
	})
	if status < 200 || status >= 300 {
		return nil, status, out
	}
	return map[string]any{"skill": "get_payment_status", "payment": out}, status, nil
}

func (s *Server) a2aQuoteRequiredUSDT(r *http.Request, args map[string]any) (map[string]any, int, map[string]any) {
	if _, ok := s.authorizeChainFX(noopResponseWriter{}, r); !ok {
		return nil, http.StatusUnauthorized, map[string]any{"error": "Authorization Bearer ChainFX API key is required"}
	}
	paymentType := strings.ToLower(firstNonEmpty(stringArg(args, "type", "payment_type", "paymentType"), "pix"))
	if paymentType != "pix" && paymentType != "credit_card" {
		return nil, http.StatusBadRequest, map[string]any{"error": "type must be 'pix' or 'credit_card'"}
	}
	amountRaw := stringArg(args, "amount_brl", "amountBRL", "amount")
	amountBRL, err := money.ParseMoney(amountRaw)
	if err != nil || amountBRL <= 0 {
		return nil, http.StatusBadRequest, map[string]any{"error": "amount_brl must be a positive number"}
	}
	usdtRate := s.workers.PriceWorker.GetPrice("BRL")
	if usdtRate <= 0 {
		return nil, http.StatusServiceUnavailable, map[string]any{"error": "USDT/BRL rate unavailable; retry in a few seconds"}
	}
	feeBps := s.cfg.M2MPixFeeBps
	if paymentType == "credit_card" {
		feeBps = s.cfg.M2MCreditFeeBps
	}
	agentWallet := strings.ToLower(strings.TrimSpace(stringArg(args, "agent_wallet", "agentWallet", "wallet")))
	if agentWallet != "" {
		if !common.IsHexAddress(agentWallet) {
			return nil, http.StatusBadRequest, map[string]any{"error": "agent_wallet must be a valid EVM address"}
		}
		env := "sandbox"
		if strings.EqualFold(s.cfg.Environment, "production") {
			env = "production"
		}
		if resolved, err := s.db.ResolveM2MFeeBps(r.Context(), agentWallet, paymentType, env, s.cfg.M2MPixFeeBps, s.cfg.M2MCreditFeeBps); err == nil {
			feeBps = resolved
		}
	}
	rateDecimal := money.RateFromFloat(usdtRate)
	gross := money.TokensFromFiat(amountBRL, rateDecimal)
	fee := money.TokenFeeBps(gross, feeBps)
	required := gross + fee
	return map[string]any{
		"payment_type":    paymentType,
		"amount_brl":      fmt.Sprintf("%.2f", amountBRL.Float64()),
		"gross_usdt":      fmt.Sprintf("%.6f", gross.Float64()),
		"fee_usdt":        fmt.Sprintf("%.6f", fee.Float64()),
		"required_usdt":   fmt.Sprintf("%.6f", required.Float64()),
		"fee_bps":         feeBps,
		"usdt_rate":       fmt.Sprintf("%.4f", usdtRate),
		"funding_asset":   "USDT",
		"funding_network": "BSC",
	}, http.StatusOK, nil
}

func (s *Server) dispatchJSONToHandler(r *http.Request, method, path string, payload any, handler http.HandlerFunc) (int, map[string]any) {
	var body bytes.Buffer
	if payload != nil {
		_ = json.NewEncoder(&body).Encode(payload)
	}
	req, _ := http.NewRequestWithContext(r.Context(), method, path, &body)
	req.Host = r.Host
	req.RemoteAddr = r.RemoteAddr
	req.Header = r.Header.Clone()
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &captureResponseWriter{}
	handler(rec, req)
	status := rec.status
	if status == 0 {
		status = http.StatusOK
	}
	var out map[string]any
	if err := json.Unmarshal(rec.body.Bytes(), &out); err != nil {
		out = map[string]any{"raw": strings.TrimSpace(rec.body.String())}
	}
	return status, out
}

type noopResponseWriter struct{}

func (noopResponseWriter) Header() http.Header       { return make(http.Header) }
func (noopResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (noopResponseWriter) WriteHeader(int)           {}

func a2aSuccessEnvelope(req a2aRequest, action string, result any) map[string]any {
	out := map[string]any{
		"status": "completed",
		"skill":  action,
		"result": result,
	}
	if req.ID != nil {
		out["id"] = req.ID
	}
	if req.JSONRPC != "" {
		out["jsonrpc"] = req.JSONRPC
	}
	return out
}

func a2aErrorEnvelope(req a2aRequest, status int, payload map[string]any) map[string]any {
	payload = enrichA2AErrorPayload(payload)
	out := map[string]any{
		"status":      "failed",
		"status_code": status,
		"error":       payload,
	}
	if req.ID != nil {
		out["id"] = req.ID
	}
	if req.JSONRPC != "" {
		out["jsonrpc"] = req.JSONRPC
	}
	return out
}

func enrichA2AErrorPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return payload
	}
	code := strings.TrimSpace(fmt.Sprint(payload["code"]))
	if code == "" && payload["error"] != nil {
		if nested, ok := payload["error"].(map[string]any); ok {
			code = strings.TrimSpace(fmt.Sprint(nested["code"]))
		}
	}
	switch code {
	case "AGENT_POLICY_REQUIRED", "AGENT_POLICY_INACTIVE", "MAX_TRANSACTION_EXCEEDED", "DAILY_LIMIT_EXCEEDED", "MONTHLY_LIMIT_EXCEEDED", "AGENT_PERMISSION_DENIED":
		payload["policy_discovery"] = "/.well-known/agent-policy.json"
		payload["capability_graph"] = "/.well-known/capability-graph.json"
		payload["next_action"] = "Fetch policy_discovery, ensure the agent wallet has an active policy, then retry the same A2A skill."
	}
	return payload
}

func normalizeA2AAction(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "chainfx.")
	value = strings.TrimPrefix(value, "agent_pay.")
	switch value {
	case "paypix", "pay_pix", "pix", "pix_payment":
		return "pay_pix_with_usdt"
	case "paycard", "pay_card", "credit_card", "card_bill", "card_bill_payment":
		return "pay_card_bill_with_usdt"
	case "status", "payment_status", "get_status":
		return "get_payment_status"
	case "quote", "quote_usdt", "quote_payment":
		return "quote_required_usdt"
	case "methods", "payment_methods", "supported_methods":
		return "list_supported_payment_methods"
	case "stablecoin", "exchange", "stablecoin_quote", "stablecoin_trade":
		return "stablecoin_exchange"
	case "capabilities", "capability", "capability_marketplace", "capability_discovery":
		return "capability_exchange"
	default:
		return value
	}
}

func firstMap(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return map[string]any{}
}

func stringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := args[key]; ok {
			switch typed := value.(type) {
			case string:
				return strings.TrimSpace(typed)
			case float64:
				return fmt.Sprintf("%.2f", typed)
			case int:
				return fmt.Sprintf("%d", typed)
			case json.Number:
				return typed.String()
			default:
				if typed != nil {
					return strings.TrimSpace(fmt.Sprint(typed))
				}
			}
		}
	}
	return ""
}
