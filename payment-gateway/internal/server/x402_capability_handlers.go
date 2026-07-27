package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"

	"github.com/ethereum/go-ethereum/common"
)

type x402CapabilityExecuteRequest struct {
	PlanID         string          `json:"planId"`
	AgentWallet    string          `json:"agentWallet"`
	PayerWallet    string          `json:"payerWallet"`
	PaymentAsset   string          `json:"paymentAsset"`
	Network        string          `json:"network"`
	Nonce          string          `json:"nonce"`
	Operation      string          `json:"operation"`
	Input          json.RawMessage `json:"input"`
	RequestID      string          `json:"requestId"`
	Units          int             `json:"units"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Provider       string          `json:"provider"`
	RoutingMode    string          `json:"routingMode"`
	Region         string          `json:"region"`
	MaxLatencyMS   int             `json:"maxLatencyMs"`
	MaxCostScore   int             `json:"maxCostScore"`
	RequireReal    bool            `json:"requireReal"`
}

type x402PaymentHeader struct {
	PurchaseID string `json:"purchaseId"`
	TxHash     string `json:"txHash"`
	LogIndex   int    `json:"logIndex"`
}

func (s *Server) handleX402CapabilityExecute(w http.ResponseWriter, r *http.Request) {
	capabilityID := strings.TrimSpace(r.PathValue("capability"))
	var req x402CapabilityExecuteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	req.AgentWallet = strings.ToLower(strings.TrimSpace(req.AgentWallet))
	req.PayerWallet = strings.ToLower(strings.TrimSpace(firstNonEmpty(req.PayerWallet, req.AgentWallet)))
	req.PaymentAsset = strings.ToUpper(strings.TrimSpace(firstNonEmpty(req.PaymentAsset, "USDT")))
	req.Network = normalizeStablecoinNetwork(req.Network)
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	req.Nonce = strings.TrimSpace(firstNonEmpty(req.Nonce, "x402_"+strings.ReplaceAll(database.NewID(), "-", "")))
	req.RequestID = strings.TrimSpace(firstNonEmpty(req.RequestID, "x402_req_"+strings.ReplaceAll(database.NewID(), "-", "")))
	if req.Units <= 0 {
		req.Units = 1
	}
	if len(req.Input) == 0 || !json.Valid(req.Input) {
		req.Input = json.RawMessage(`{}`)
	}
	if !common.IsHexAddress(req.AgentWallet) || !common.IsHexAddress(req.PayerWallet) {
		s.writeX402PaymentRequired(w, r, capabilityID, req, nil, map[string]any{
			"code":    "wallet_required",
			"message": "agentWallet and payerWallet must be valid EVM addresses to quote x402 payment.",
		})
		return
	}
	if req.IdempotencyKey == "" {
		s.writeX402PaymentRequired(w, r, capabilityID, req, nil, map[string]any{
			"code":    "idempotency_required",
			"message": "idempotencyKey or X-Idempotency-Key is required.",
		})
		return
	}

	payment, paymentOK := parseX402PaymentHeader(r)
	if !paymentOK {
		purchase, product, plan, err := s.createX402CapabilityPurchase(r, capabilityID, req)
		if err != nil {
			if paymentErr, ok := err.(*database.AgentCreditPaymentRequiredError); ok {
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"error":                paymentErr.Challenge(),
					"payment_requirements": paymentErr.Challenge(),
				})
				return
			}
			if payload, ok := x402PolicyPaymentRequired(err); ok {
				writeJSON(w, http.StatusPaymentRequired, map[string]any{
					"error":                payload,
					"payment_requirements": payload,
				})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.writeX402PaymentRequired(w, r, capabilityID, req, x402ChallengeFromPurchase(r, purchase, product, plan), nil)
		return
	}

	result, status, errPayload := s.activateX402Payment(r, payment)
	if errPayload != nil {
		writeJSON(w, status, map[string]any{
			"error":                errPayload,
			"payment_requirements": map[string]any{"purchase_id": payment.PurchaseID, "status": "payment_not_verified"},
		})
		return
	}
	accessToken := result.AccessToken
	if accessToken == "" {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": "payment activated but access token was not returned"})
		return
	}

	execReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/agent/v1/capabilities/"+capabilityID+"/execute", nil)
	execReq.Header = r.Header.Clone()
	execReq.Header.Set("Authorization", "Bearer "+accessToken)
	execReq.Host = r.Host
	execReq.RemoteAddr = r.RemoteAddr
	body := map[string]any{
		"operation":      req.Operation,
		"input":          jsonRawToMap(req.Input),
		"requestId":      req.RequestID,
		"units":          req.Units,
		"idempotencyKey": req.IdempotencyKey + ":exec",
		"provider":       req.Provider,
		"routingMode":    req.RoutingMode,
		"region":         req.Region,
		"maxLatencyMs":   req.MaxLatencyMS,
		"maxCostScore":   req.MaxCostScore,
		"requireReal":    req.RequireReal,
	}
	statusCode, out := s.dispatchJSONToHandler(execReq, http.MethodPost, "/agent/v1/capabilities/"+capabilityID+"/execute", body, func(w http.ResponseWriter, req *http.Request) {
		req.SetPathValue("capability", capabilityID)
		s.handleAgentCapabilityExecute(w, req)
	})
	responsePayload := map[string]any{
		"ok":              statusCode >= 200 && statusCode < 300,
		"x402":            "paid",
		"payment":         marketplaceActivationResponse(result),
		"capability":      capabilityID,
		"execution":       out,
		"payment_receipt": x402PaymentResponse(result, payment),
	}
	rawReceipt, _ := json.Marshal(responsePayload["payment_receipt"])
	w.Header().Set("PAYMENT-RESPONSE", base64.RawURLEncoding.EncodeToString(rawReceipt))
	writeJSON(w, statusCode, responsePayload)
}

func x402PolicyPaymentRequired(err error) (map[string]any, bool) {
	if err == nil {
		return nil, false
	}
	raw := strings.TrimSpace(err.Error())
	code, message, found := strings.Cut(raw, ":")
	if !found {
		code = raw
		message = raw
	}
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	switch code {
	case "AGENT_POLICY_REQUIRED",
		"AGENT_POLICY_INACTIVE",
		"AGENT_PERMISSION_DENIED",
		"MAX_TRANSACTION_EXCEEDED",
		"DAILY_LIMIT_EXCEEDED",
		"MONTHLY_LIMIT_EXCEEDED",
		"ASSET_NOT_ALLOWED",
		"CAPABILITY_NOT_ALLOWED":
		return map[string]any{
			"code":             code,
			"message":          message,
			"policy_discovery": "/.well-known/agent-policy.json",
			"capability_graph": "/.well-known/capability-graph.json",
			"next_action":      "Fetch policy discovery, connect or activate the agent wallet policy, then retry the same x402 capability request.",
		}, true
	default:
		return nil, false
	}
}

func (s *Server) createX402CapabilityPurchase(r *http.Request, capabilityID string, req x402CapabilityExecuteRequest) (*database.MarketplacePurchase, *database.MarketplaceProduct, *database.MarketplacePlan, error) {
	capability, err := s.db.GetMarketplaceCapability(r.Context(), capabilityID)
	if err != nil {
		return nil, nil, nil, err
	}
	if capability == nil {
		return nil, nil, nil, fmt.Errorf("capability not found")
	}
	_, plan, err := s.db.ResolveMarketplaceCapabilityPlan(r.Context(), capability.ID, req.PlanID, req.PaymentAsset, req.Network)
	if err != nil {
		return nil, nil, nil, err
	}
	paymentAddress := s.accessPaymentAddress()
	if !common.IsHexAddress(paymentAddress) {
		return nil, nil, nil, fmt.Errorf("payment address is not configured")
	}
	paymentContract, err := s.marketplacePaymentContract(r, plan.PaymentAsset, plan.Network)
	if err != nil {
		return nil, nil, nil, err
	}
	return s.db.CreateMarketplacePurchase(r.Context(), database.MarketplacePurchaseInput{
		PlanID:          plan.ID,
		AgentWallet:     req.AgentWallet,
		PayerWallet:     req.PayerWallet,
		PaymentAddress:  paymentAddress,
		PaymentContract: paymentContract,
		Nonce:           req.Nonce,
		IdempotencyKey:  req.IdempotencyKey + ":x402_purchase",
		ExpiresAt:       time.Now().UTC().Add(marketplacePurchaseTTL),
	})
}

func (s *Server) activateX402Payment(r *http.Request, payment x402PaymentHeader) (*database.MarketplaceActivationResult, int, map[string]any) {
	payment.PurchaseID = strings.TrimSpace(payment.PurchaseID)
	payment.TxHash = strings.ToLower(strings.TrimSpace(payment.TxHash))
	if payment.PurchaseID == "" || payment.TxHash == "" || !strings.HasPrefix(payment.TxHash, "0x") || payment.LogIndex < 0 {
		return nil, http.StatusBadRequest, map[string]any{"code": "invalid_payment_header", "message": "PAYMENT must include purchaseId, txHash and logIndex."}
	}
	purchase, err := s.db.GetMarketplacePurchase(r.Context(), payment.PurchaseID)
	if err != nil {
		return nil, http.StatusInternalServerError, map[string]any{"code": "purchase_lookup_failed", "message": err.Error()}
	}
	if purchase == nil {
		return nil, http.StatusNotFound, map[string]any{"code": "purchase_not_found", "message": "purchase not found"}
	}
	if time.Now().UTC().After(purchase.ExpiresAt) {
		return nil, http.StatusPaymentRequired, map[string]any{"code": "purchase_expired", "message": "purchase expired"}
	}
	asset, err := s.db.GetAgentSupportedAsset(r.Context(), purchase.PaymentAsset, purchase.Network)
	if err != nil {
		return nil, http.StatusInternalServerError, map[string]any{"code": "asset_lookup_failed", "message": err.Error()}
	}
	if asset == nil || !asset.Enabled || asset.Status != "active" || !strings.EqualFold(asset.ContractAddress, purchase.PaymentContract) {
		return nil, http.StatusPaymentRequired, map[string]any{"code": "payment_asset_not_allowed", "message": "payment contract is not allowlisted"}
	}
	expectedAmount, err := decimalStringToBaseUnits(purchase.GrossAmount, asset.Decimals)
	if err != nil {
		return nil, http.StatusInternalServerError, map[string]any{"code": "invalid_internal_amount", "message": err.Error()}
	}
	expectedLogIndex := payment.LogIndex
	receipt, err := s.verifyERC20TransferTxRaw(r.Context(), purchase.Network, payment.TxHash, purchase.PaymentContract, purchase.PayerWallet, purchase.PaymentAddress, expectedAmount, purchase.PaymentAsset, asset.Decimals, &expectedLogIndex)
	if err != nil {
		return nil, http.StatusPaymentRequired, map[string]any{"code": "payment_not_verified", "message": err.Error()}
	}
	result, err := s.db.ActivateMarketplacePurchase(r.Context(), purchase.ID, receipt)
	if err != nil {
		return nil, http.StatusConflict, map[string]any{"code": "activation_failed", "message": err.Error()}
	}
	return result, http.StatusOK, nil
}

func (s *Server) writeX402PaymentRequired(w http.ResponseWriter, r *http.Request, capabilityID string, req x402CapabilityExecuteRequest, challenge map[string]any, validation map[string]any) {
	if challenge == nil {
		challenge = map[string]any{
			"pricing_scheme": "exact",
			"asset":          firstNonEmpty(req.PaymentAsset, "USDT"),
			"network":        normalizeStablecoinNetwork(req.Network),
			"chain_id":       stablecoinNetworkChainID(req.Network),
			"capability":     capabilityID,
			"quote_url":      publicBaseURL(r) + "/x402/capabilities/" + capabilityID + "/execute",
			"required_fields": []string{
				"agentWallet",
				"payerWallet",
				"idempotencyKey",
				"operation",
				"requestId",
			},
		}
	}
	payload := map[string]any{
		"error":                "Payment required to execute ChainFX capability.",
		"code":                 "HTTP_402_PAYMENT_REQUIRED",
		"x402_version":         "chainfx-x402-capability-0.1",
		"payment_requirements": challenge,
		"replay": map[string]any{
			"method":  "POST",
			"url":     publicBaseURL(r) + "/x402/capabilities/" + capabilityID + "/execute",
			"headers": []string{"PAYMENT: base64url({\"purchaseId\":\"...\",\"txHash\":\"0x...\",\"logIndex\":0})"},
		},
	}
	if validation != nil {
		payload["validation"] = validation
	}
	w.Header().Set("X-Payment-Required", "x402")
	writeJSON(w, http.StatusPaymentRequired, payload)
}

func x402ChallengeFromPurchase(r *http.Request, purchase *database.MarketplacePurchase, product *database.MarketplaceProduct, plan *database.MarketplacePlan) map[string]any {
	out := marketplacePurchaseIntentResponse(purchase, product, plan)
	out["pricing_scheme"] = "exact"
	out["currency"] = purchase.PaymentAsset
	out["blockchain"] = fmt.Sprintf("eip155:%d", purchase.ChainID)
	out["destination_address"] = purchase.PaymentAddress
	out["amount"] = purchase.GrossAmount
	out["purchase_id"] = purchase.ID
	out["expires_at"] = purchase.ExpiresAt.Unix()
	out["payment_header"] = map[string]any{
		"encoding": "base64url-json",
		"schema":   map[string]string{"purchaseId": purchase.ID, "txHash": "0x...", "logIndex": "0"},
	}
	return out
}

func parseX402PaymentHeader(r *http.Request) (x402PaymentHeader, bool) {
	raw := strings.TrimSpace(firstNonEmpty(r.Header.Get("PAYMENT"), r.Header.Get("Payment"), r.Header.Get("PAYMENT-SIGNATURE"), r.Header.Get("X-Payment")))
	if raw == "" {
		return x402PaymentHeader{}, false
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		raw = string(decoded)
	} else if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		raw = string(decoded)
	}
	var payment x402PaymentHeader
	if err := json.Unmarshal([]byte(raw), &payment); err != nil {
		return x402PaymentHeader{}, false
	}
	return payment, true
}

func x402PaymentResponse(result *database.MarketplaceActivationResult, payment x402PaymentHeader) map[string]any {
	return map[string]any{
		"status":      "paid",
		"purchaseId":  payment.PurchaseID,
		"txHash":      payment.TxHash,
		"logIndex":    payment.LogIndex,
		"accessGrant": result.Grant,
	}
}
