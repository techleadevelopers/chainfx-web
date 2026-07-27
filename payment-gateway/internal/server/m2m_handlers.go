package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/money"
	"payment-gateway/internal/workers"

	"github.com/ethereum/go-ethereum/common"
)

// ─── Request / Response types ────────────────────────────────────────────────

type m2mCreateRequest struct {
	Type            string `json:"type"`             // "pix" | "credit_card"
	AmountBRL       string `json:"amount_brl"`       // BRL amount the recipient will receive
	PixKey          string `json:"pix_key"`          // required when type == "pix"
	PaymentLink     string `json:"payment_link"`     // required for credit_card when barcode is absent
	Barcode         string `json:"barcode"`          // required for credit_card when payment_link is absent
	BeneficiaryName string `json:"beneficiary_name"` // optional merchant/beneficiary hint
	DueDate         string `json:"due_date"`         // optional due date hint
	IdempotencyKey  string `json:"idempotency_key"`  // caller-generated, immutable
	AgentWallet     string `json:"agent_wallet"`     // EVM address of paying agent (audit)
	PaymentNetwork  string `json:"payment_network"`  // "BSC" | "POLYGON"
}

type m2mCreateResponse struct {
	IntentID        string    `json:"intent_id"`
	Status          string    `json:"status"`
	PaymentType     string    `json:"payment_type"`
	AmountBRL       string    `json:"amount_brl"`
	GrossUSDT       string    `json:"gross_usdt"`
	FeeUSDT         string    `json:"fee_usdt"`
	RequiredUSDT    string    `json:"required_usdt"`
	FeeBps          int       `json:"fee_bps"`
	USDTRate        string    `json:"usdt_rate"`
	PaymentAddress  string    `json:"payment_address"`
	PaymentNetwork  string    `json:"payment_network"`
	PaymentLink     string    `json:"payment_link,omitempty"`
	Barcode         string    `json:"barcode,omitempty"`
	BeneficiaryName string    `json:"beneficiary_name,omitempty"`
	DueDate         string    `json:"due_date,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	Idempotent      bool      `json:"idempotent,omitempty"` // true when returning cached response
	NextStep        string    `json:"next_step"`
}

// ─── POST /agent/v1/pay ──────────────────────────────────────────────────────

// handleM2MCreateIntent creates a new agent-initiated payment intent.
// The agent deposits RequiredUSDT on-chain; our system then pays the fiat recipient.
func (s *Server) handleM2MCreateIntent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req m2mCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}

	// ── Validation ────────────────────────────────────────────────────────────
	req.Type = strings.TrimSpace(strings.ToLower(req.Type))
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.AgentWallet = strings.ToLower(strings.TrimSpace(req.AgentWallet))
	req.PaymentNetwork = normalizeStablecoinNetwork(req.PaymentNetwork)
	req.PixKey = strings.TrimSpace(req.PixKey)
	req.PaymentLink = strings.TrimSpace(req.PaymentLink)
	req.Barcode = strings.TrimSpace(req.Barcode)
	req.BeneficiaryName = strings.TrimSpace(req.BeneficiaryName)
	req.DueDate = strings.TrimSpace(req.DueDate)

	if req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "idempotency_key is required"})
		return
	}
	if req.AgentWallet == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_wallet is required"})
		return
	}
	if !common.IsHexAddress(req.AgentWallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent_wallet must be a valid EVM address"})
		return
	}
	if req.PaymentNetwork != "BSC" && req.PaymentNetwork != "POLYGON" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payment_network deve ser BSC ou POLYGON"})
		return
	}

	switch req.Type {
	case "pix":
		if req.PixKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pix_key is required for type=pix"})
			return
		}
	case "credit_card":
		if req.PaymentLink == "" && req.Barcode == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payment_link or barcode is required for type=credit_card"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "type must be 'pix' or 'credit_card'"})
		return
	}

	// ── Per-agent pricing (falls back to global env vars) ─────────────────────
	env := "sandbox"
	if strings.EqualFold(s.cfg.Environment, "production") {
		env = "production"
	}
	feeBps, feeErr := s.db.ResolveM2MFeeBps(r.Context(), req.AgentWallet, req.Type, env, s.cfg.M2MPixFeeBps, s.cfg.M2MCreditFeeBps)
	if feeErr != nil {
		if req.Type == "pix" {
			feeBps = s.cfg.M2MPixFeeBps
		} else {
			feeBps = s.cfg.M2MCreditFeeBps
		}
	}

	amountBRLMoney, err := money.ParseMoney(req.AmountBRL)
	if err != nil || amountBRLMoney <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl must be a positive number"})
		return
	}

	// ── Rate ──────────────────────────────────────────────────────────────────
	usdtRate := s.workers.PriceWorker.GetPrice("BRL")
	if usdtRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "USDT/BRL rate unavailable; retry in a few seconds",
		})
		return
	}

	// ── Fee calculation ───────────────────────────────────────────────────────
	// grossUSDT = amountBRL / usdtRate   (base: how much the pix recipient costs in USDT)
	// feeUSDT   = grossUSDT * (feeBps / 10_000)
	// required  = grossUSDT + feeUSDT
	usdtRateDecimal := money.RateFromFloat(usdtRate)
	grossUSDTTokens := money.TokensFromFiat(amountBRLMoney, usdtRateDecimal)
	feeUSDTTokens := money.TokenFeeBps(grossUSDTTokens, feeBps)
	requiredUSDTTokens := grossUSDTTokens + feeUSDTTokens

	amountBRL := amountBRLMoney.Float64()
	grossUSDT := grossUSDTTokens.Float64()
	feeUSDT := feeUSDTTokens.Float64()
	requiredUSDT := requiredUSDTTokens.Float64()

	_, decision, policyErr := s.db.ValidateAgentPaymentPolicy(r.Context(), req.AgentWallet, "USDT", fmt.Sprintf("%.6f", requiredUSDT))
	if policyErr != nil {
		writeError(w, policyErr)
		return
	}
	if !decision.Allowed {
		writeAPIError(w, r, http.StatusForbidden, decision.Code, decision.Message)
		return
	}

	// ── Intent ID (deterministic from idempotency key + wallet) ──────────────
	intentID := m2mIntentID(req.IdempotencyKey, req.AgentWallet)

	// ── Audit hash (canonical request fingerprint) ────────────────────────────
	requestHash := database.CanonicalRequestHash(
		req.Type, req.AmountBRL, req.PixKey,
		req.PaymentLink, req.Barcode, req.BeneficiaryName, req.DueDate,
		req.IdempotencyKey, req.AgentWallet, req.PaymentNetwork,
	)

	paymentAddress, err := s.db.PickAvailableM2MDepositAddress(
		r.Context(),
		splitAddressList(s.cfg.M2MDepositAddresses),
		s.cfg.TreasuryHot,
	)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if !common.IsHexAddress(paymentAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "M2M payment address must be a valid EVM address"})
		return
	}

	in := database.M2MCreateInput{
		ID:              intentID,
		IdempotencyKey:  req.IdempotencyKey,
		AgentWallet:     req.AgentWallet,
		PaymentType:     database.M2MPaymentType(req.Type),
		PixKey:          req.PixKey,
		PaymentLink:     req.PaymentLink,
		Barcode:         req.Barcode,
		BeneficiaryName: req.BeneficiaryName,
		DueDate:         req.DueDate,
		AmountBRL:       amountBRL,
		FeeBps:          feeBps,
		FeeUSDT:         feeUSDT,
		GrossUSDT:       grossUSDT,
		RequiredUSDT:    requiredUSDT,
		USDTRate:        usdtRate,
		PaymentAddress:  paymentAddress,
		PaymentNetwork:  req.PaymentNetwork,
		RequestHash:     requestHash,
		ExpiresAt:       time.Now().UTC().Add(15 * time.Minute),
	}

	intent, isIdempotent, err := s.db.CreateAgentPaymentIntent(r.Context(), in)
	if err != nil {
		slog.Error("failed to create M2M agent payment intent",
			"error", err,
			"agent_wallet", req.AgentWallet,
			"payment_type", req.Type,
			"payment_address", paymentAddress,
			"idempotency_key", req.IdempotencyKey,
		)
		if strings.Contains(err.Error(), "uq_m2m_pending_payment_address") {
			writeAPIError(w, r, http.StatusConflict, "M2M_DEPOSIT_ADDRESS_BUSY", "A legacy unique pending-address index is blocking this shared deposit address. Apply migration 015_m2m_shared_deposit_addresses and retry.")
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "M2M_INTENT_CREATE_FAILED", "Failed to create payment intent.")
		return
	}

	// ── Publish lifecycle event (new intent only, not idempotent replays) ─────
	if !isIdempotent && s.workers != nil {
		s.workers.Bus.Publish(workers.Event{
			Type:    "m2m.intent.created",
			OrderID: intent.ID,
			Payload: map[string]any{
				"intent_id":       intent.ID,
				"agent_wallet":    intent.AgentWallet,
				"payment_type":    string(intent.PaymentType),
				"payment_network": intent.PaymentNetwork,
				"amount_brl":      intent.AmountBRL,
				"fee_bps":         intent.FeeBps,
				"expires_at":      intent.ExpiresAt,
			},
		})
	}

	statusCode := http.StatusCreated
	if isIdempotent {
		statusCode = http.StatusOK
	}

	writeJSON(w, statusCode, m2mIntentToResponse(intent, isIdempotent))
}

// ─── GET /agent/v1/pay/{id} ──────────────────────────────────────────────────

func (s *Server) handleM2MGetIntent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "intent id is required"})
		return
	}

	intent, err := s.db.GetAgentPaymentIntent(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to fetch intent"})
		return
	}
	if intent == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "intent not found"})
		return
	}

	writeJSON(w, http.StatusOK, intentFullView(intent))
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func m2mIntentToResponse(i *database.AgentPaymentIntent, idempotent bool) m2mCreateResponse {
	nextStep := fmt.Sprintf("deposit RequiredUSDT USDT on %s to PaymentAddress, then poll GET /agent/v1/pay/{intent_id}", i.PaymentNetwork)
	if i.Status != "pending_deposit" {
		nextStep = fmt.Sprintf("intent is %s — no deposit required", i.Status)
	}
	return m2mCreateResponse{
		IntentID:        i.ID,
		Status:          string(i.Status),
		PaymentType:     string(i.PaymentType),
		AmountBRL:       fmt.Sprintf("%.2f", i.AmountBRL),
		GrossUSDT:       fmt.Sprintf("%.6f", i.GrossUSDT),
		FeeUSDT:         fmt.Sprintf("%.6f", i.FeeUSDT),
		RequiredUSDT:    fmt.Sprintf("%.6f", i.RequiredUSDT),
		FeeBps:          i.FeeBps,
		USDTRate:        fmt.Sprintf("%.4f", i.USDTRate),
		PaymentAddress:  i.PaymentAddress,
		PaymentNetwork:  i.PaymentNetwork,
		PaymentLink:     i.PaymentLink,
		Barcode:         i.Barcode,
		BeneficiaryName: i.BeneficiaryName,
		DueDate:         i.DueDate,
		ExpiresAt:       i.ExpiresAt,
		Idempotent:      idempotent,
		NextStep:        nextStep,
	}
}

func intentFullView(i *database.AgentPaymentIntent) map[string]any {
	out := map[string]any{
		"id":              i.ID,
		"status":          string(i.Status),
		"payment_type":    string(i.PaymentType),
		"amount_brl":      fmt.Sprintf("%.2f", i.AmountBRL),
		"gross_usdt":      fmt.Sprintf("%.6f", i.GrossUSDT),
		"fee_usdt":        fmt.Sprintf("%.6f", i.FeeUSDT),
		"required_usdt":   fmt.Sprintf("%.6f", i.RequiredUSDT),
		"fee_bps":         i.FeeBps,
		"usdt_rate":       fmt.Sprintf("%.4f", i.USDTRate),
		"payment_address": i.PaymentAddress,
		"payment_network": i.PaymentNetwork,
		"expires_at":      i.ExpiresAt,
		"attempts":        i.Attempts,
		"created_at":      i.CreatedAt,
		"updated_at":      i.UpdatedAt,
	}
	if i.PixKey != "" {
		out["pix_key"] = i.PixKey
	}
	if i.PaymentLink != "" {
		out["payment_link"] = i.PaymentLink
	}
	if i.Barcode != "" {
		out["barcode"] = i.Barcode
	}
	if i.BeneficiaryName != "" {
		out["beneficiary_name"] = i.BeneficiaryName
	}
	if i.DueDate != "" {
		out["due_date"] = i.DueDate
	}
	if i.DepositTx != nil {
		out["deposit_tx"] = *i.DepositTx
	}
	if i.DepositAmountUSDT != nil {
		out["deposit_amount_usdt"] = fmt.Sprintf("%.6f", *i.DepositAmountUSDT)
	}
	if i.EfiEndToEndID != nil {
		out["efi_end_to_end_id"] = *i.EfiEndToEndID
	}
	if i.EfiStatus != nil {
		out["efi_status"] = *i.EfiStatus
	}
	if i.SettlementReceiptURL != "" {
		out["settlement_receipt_url"] = i.SettlementReceiptURL
	}
	if i.SettlementReceiptNote != "" {
		out["settlement_receipt_note"] = i.SettlementReceiptNote
	}
	if i.ErrorMessage != nil {
		out["error_message"] = *i.ErrorMessage
	}
	if i.SettledAt != nil {
		out["settled_at"] = *i.SettledAt
	}
	return out
}

func m2mIntentID(idempotencyKey, agentWallet string) string {
	hash := database.CanonicalRequestHash(idempotencyKey, strings.ToLower(agentWallet))
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return "int_m2m_" + hash
}

func splitAddressList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}
