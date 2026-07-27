package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/money"
	"payment-gateway/internal/nfc"
	"payment-gateway/internal/workers"

	"github.com/ethereum/go-ethereum/common"
)

type nfcProvisionRequest struct {
	WalletAddress string `json:"wallet_address"`
	DeviceID      string `json:"device_id"`
	Network       string `json:"network"`
	TTLSeconds    int    `json:"ttl_seconds"`
}

type nfcAuthorizeRequest struct {
	Token          string `json:"token"`
	AmountBRL      string `json:"amount_brl"`
	Currency       string `json:"currency"`
	MerchantID     string `json:"merchant_id"`
	TerminalID     string `json:"terminal_id"`
	ExternalRef    string `json:"external_ref"`
	IdempotencyKey string `json:"idempotency_key"`
}

type nfcSandboxFundRequest struct {
	WalletAddress string `json:"wallet_address"`
	Network       string `json:"network"`
	AmountUSDT    string `json:"amount_usdt"`
}

func (s *Server) handleNFCProvision(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req nfcProvisionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	req.WalletAddress = strings.ToLower(strings.TrimSpace(req.WalletAddress))
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Network = normalizeStablecoinNetwork(req.Network)
	if req.Network == "" {
		req.Network = "BSC"
	}
	if !common.IsHexAddress(req.WalletAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet_address must be a valid EVM address"})
		return
	}
	if req.DeviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "device_id is required"})
		return
	}
	if req.Network != "BSC" && req.Network != "POLYGON" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network must be BSC or POLYGON"})
		return
	}
	ttl := time.Duration(s.cfg.NFCTokenTTLSeconds) * time.Second
	if req.TTLSeconds > 0 && req.TTLSeconds <= 300 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	now := time.Now().UTC()
	token, claims, err := nfc.IssueToken(s.cfg.NFCTokenSecret, req.WalletAddress, req.DeviceID, req.Network, ttl, now)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	if err := s.db.StoreNFCToken(r.Context(), database.NFCTokenInput{
		TokenID:   claims.TokenID,
		TokenHash: nfc.TokenHash(token),
		Wallet:    claims.Wallet,
		DeviceID:  claims.DeviceID,
		Network:   claims.Network,
		ExpiresAt: time.Unix(claims.ExpiresAtUnix, 0),
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"token_id":   claims.TokenID,
		"expires_at": time.Unix(claims.ExpiresAtUnix, 0).UTC(),
		"network":    claims.Network,
		"next_step":  "Send this opaque token through the Android HCE APDU response; the ChainFX terminal submits it to POST /api/nfc/authorize.",
	})
}

func (s *Server) handleNFCAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	var req nfcAuthorizeRequest
	stage := time.Now()
	if err := decodeJSON(r, &req); err != nil {
		addServerTiming(r.Context(), "json_decode", time.Since(stage))
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	addServerTiming(r.Context(), "json_decode", time.Since(stage))
	req.Token = strings.TrimSpace(req.Token)
	req.Currency = strings.ToUpper(strings.TrimSpace(defaultString(req.Currency, "BRL")))
	req.MerchantID = strings.TrimSpace(req.MerchantID)
	req.TerminalID = strings.TrimSpace(req.TerminalID)
	req.ExternalRef = strings.TrimSpace(req.ExternalRef)
	req.IdempotencyKey = firstNonEmpty(strings.TrimSpace(req.IdempotencyKey), strings.TrimSpace(r.Header.Get("Idempotency-Key")), strings.TrimSpace(r.Header.Get("X-Idempotency-Key")))
	if req.Token == "" || req.MerchantID == "" || req.TerminalID == "" || req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token, merchant_id, terminal_id and idempotency_key are required"})
		return
	}
	stage = time.Now()
	terminal, ok := s.authorizeNFCTerminal(w, r, req.MerchantID, req.TerminalID)
	addServerTiming(r.Context(), "terminal_auth", time.Since(stage))
	if !ok {
		return
	}
	if req.Currency != "BRL" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "only BRL NFC authorizations are currently supported"})
		return
	}
	stage = time.Now()
	amount, err := money.ParseMoney(req.AmountBRL)
	addServerTiming(r.Context(), "amount_parse", time.Since(stage))
	if err != nil || amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl must be a positive decimal amount"})
		return
	}
	stage = time.Now()
	if s.cfg.NFCMaxAmountBRL > 0 && amount.Float64() > s.cfg.NFCMaxAmountBRL {
		addServerTiming(r.Context(), "risk_validation", time.Since(stage))
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "amount exceeds NFC_MAX_AMOUNT_BRL", "code": "NFC_AMOUNT_LIMIT"})
		return
	}
	if terminal.MaxAmountBRLMinor > 0 && int64(amount) > terminal.MaxAmountBRLMinor {
		addServerTiming(r.Context(), "risk_validation", time.Since(stage))
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "amount exceeds terminal max amount", "code": "NFC_TERMINAL_AMOUNT_LIMIT", "response_code": "61"})
		return
	}
	addServerTiming(r.Context(), "risk_validation", time.Since(stage))

	stage = time.Now()
	claims, err := nfc.VerifyToken(s.cfg.NFCTokenSecret, req.Token, time.Now().UTC())
	addServerTiming(r.Context(), "token_validation", time.Since(stage))
	if err != nil {
		status := http.StatusUnauthorized
		code := "NFC_INVALID_TOKEN"
		if errors.Is(err, nfc.ErrExpiredToken) {
			code = "NFC_TOKEN_EXPIRED"
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "code": code, "response_code": "05"})
		return
	}
	stage = time.Now()
	price := s.workers.PriceWorker.GetSnapshot("BRL")
	addServerTiming(r.Context(), "price_lookup", time.Since(stage))
	if price.Price <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "USDT/BRL rate unavailable"})
		return
	}
	if price.UpdatedAt.IsZero() || time.Since(price.UpdatedAt) > time.Duration(s.cfg.NFCPriceMaxAgeSec)*time.Second {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "USDT/BRL rate is stale", "code": "NFC_PRICE_STALE", "response_code": "91"})
		return
	}
	feeBps := maxInt(0, s.cfg.NFCFeeBps)
	feeBRL := money.FeeBps(amount, feeBps)
	totalBRL := amount + feeBRL
	required := money.TokensFromFiat(totalBRL, money.RateFromFloat(price.Price))
	if required <= 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "invalid required USDT amount"})
		return
	}

	stage = time.Now()
	auth, idempotent, err := s.db.AuthorizeNFCPayment(r.Context(), database.NFCAuthorizeInput{
		ID:                     "nfc_auth_" + database.NewAccessToken()[:24],
		IdempotencyKey:         req.IdempotencyKey,
		TokenID:                claims.TokenID,
		TokenHash:              nfc.TokenHash(req.Token),
		Wallet:                 claims.Wallet,
		Network:                claims.Network,
		MerchantID:             req.MerchantID,
		TerminalID:             req.TerminalID,
		ExternalRef:            req.ExternalRef,
		AmountBRLMinor:         int64(amount),
		FeeBRLMinor:            int64(feeBRL),
		TotalBRLMinor:          int64(totalBRL),
		FeeBps:                 feeBps,
		USDTRate:               price.Price,
		RequiredUSDTMic:        int64(required),
		HoldExpiresAt:          time.Now().UTC().Add(time.Duration(s.cfg.NFCHoldTTLSeconds) * time.Second),
		LiquidityPolicyEnabled: s.cfg.NFCLiquidityPolicyEnabled,
		TreasurySnapshotMaxAge: time.Duration(s.cfg.NFCTreasurySnapshotMaxAgeSec) * time.Second,
	})
	addServerTiming(r.Context(), "db_transaction", time.Since(stage))
	if err != nil {
		if errors.Is(err, database.ErrNFCIdempotencyPayloadMismatch) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "idempotency key replayed with different payload", "code": "NFC_IDEMPOTENCY_PAYLOAD_MISMATCH"})
			return
		}
		if errors.Is(err, database.ErrNFCLiquidityUnavailable) {
			metrics.IncNFCLiquidityRejection()
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "NFC treasury liquidity unavailable", "code": "NFC_LIQUIDITY_UNAVAILABLE", "response_code": "91"})
			return
		}
		writeError(w, err)
		return
	}
	auth.Idempotent = idempotent
	if idempotent {
		metrics.IncNFCIdempotencyReplay()
	}
	metrics.RecordNFCAuthorization(auth.Status)
	if auth.Status == database.NFCStatusDeclined && auth.Reason == "efi_treasury_liquidity_unavailable" {
		metrics.IncNFCLiquidityRejection()
	}
	statusCode := http.StatusOK
	if auth.Status == database.NFCStatusApproved {
		statusCode = http.StatusAccepted
	}
	writeJSON(w, statusCode, nfcAuthorizationView(auth))
}

func (s *Server) handleNFCGetAuthorization(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	stage := time.Now()
	auth, err := s.db.GetNFCAuthorization(r.Context(), strings.TrimSpace(r.PathValue("id")))
	addServerTiming(r.Context(), "authorization_lookup", time.Since(stage))
	if err != nil {
		writeError(w, err)
		return
	}
	if auth == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	stage = time.Now()
	if _, ok := s.authorizeNFCTerminal(w, r, auth.MerchantID, auth.TerminalID); !ok {
		addServerTiming(r.Context(), "terminal_auth", time.Since(stage))
		return
	}
	addServerTiming(r.Context(), "terminal_auth", time.Since(stage))
	writeJSON(w, http.StatusOK, nfcAuthorizationView(auth))
}

func (s *Server) handleNFCCaptureAuthorization(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	stage := time.Now()
	current, err := s.db.GetNFCAuthorization(r.Context(), id)
	addServerTiming(r.Context(), "authorization_lookup", time.Since(stage))
	if err != nil {
		writeError(w, err)
		return
	}
	if current == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	stage = time.Now()
	if _, ok := s.authorizeNFCTerminal(w, r, current.MerchantID, current.TerminalID); !ok {
		addServerTiming(r.Context(), "terminal_auth", time.Since(stage))
		return
	}
	addServerTiming(r.Context(), "terminal_auth", time.Since(stage))
	stage = time.Now()
	capture, err := s.db.CaptureNFCAuthorization(r.Context(), id)
	addServerTiming(r.Context(), "ledger_capture", time.Since(stage))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "NFC_CAPTURE_FAILED"})
		return
	}
	if capture == nil || capture.Authorization == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	s.publishNFCEvent("nfc.capture.completed", capture.Authorization, capture.Settlement)
	metrics.IncNFCCapture()
	writeJSON(w, http.StatusOK, nfcAuthorizationView(capture.Authorization, capture.Settlement))
}

func (s *Server) handleNFCReverseAuthorization(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	stage := time.Now()
	current, err := s.db.GetNFCAuthorization(r.Context(), id)
	addServerTiming(r.Context(), "authorization_lookup", time.Since(stage))
	if err != nil {
		writeError(w, err)
		return
	}
	if current == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	stage = time.Now()
	if _, ok := s.authorizeNFCTerminal(w, r, current.MerchantID, current.TerminalID); !ok {
		addServerTiming(r.Context(), "terminal_auth", time.Since(stage))
		return
	}
	addServerTiming(r.Context(), "terminal_auth", time.Since(stage))
	stage = time.Now()
	auth, err := s.db.ReverseNFCAuthorization(r.Context(), id)
	addServerTiming(r.Context(), "ledger_reverse", time.Since(stage))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "NFC_REVERSAL_FAILED"})
		return
	}
	if auth == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "authorization not found"})
		return
	}
	s.publishNFCEvent("nfc.authorization.reversed", auth)
	metrics.IncNFCReverse()
	writeJSON(w, http.StatusOK, nfcAuthorizationView(auth))
}

func (s *Server) handleNFCBalance(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	wallet := strings.ToLower(strings.TrimSpace(r.PathValue("wallet")))
	if !common.IsHexAddress(wallet) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet must be a valid EVM address"})
		return
	}
	network := normalizeStablecoinNetwork(r.URL.Query().Get("network"))
	if network == "" {
		network = "BSC"
	}
	bal, err := s.db.GetNFCBalance(r.Context(), wallet, network)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nfcBalanceView(bal))
}

func (s *Server) handleNFCSandboxFund(w http.ResponseWriter, r *http.Request) {
	if !s.nfcReady(w) {
		return
	}
	if !s.cfg.AllowSimulations {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "sandbox funding is disabled when ALLOW_SIMULATIONS=false"})
		return
	}
	if _, ok := s.authorizeChainFX(w, r); !ok {
		return
	}
	var req nfcSandboxFundRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	req.WalletAddress = strings.ToLower(strings.TrimSpace(req.WalletAddress))
	req.Network = normalizeStablecoinNetwork(req.Network)
	if req.Network == "" {
		req.Network = "BSC"
	}
	if !common.IsHexAddress(req.WalletAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet_address must be a valid EVM address"})
		return
	}
	amount, err := money.ParseToken(req.AmountUSDT)
	if err != nil || amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_usdt must be a positive decimal amount"})
		return
	}
	bal, err := s.db.AddNFCBalance(r.Context(), database.NFCFundingInput{
		Wallet:     req.WalletAddress,
		Network:    req.Network,
		Asset:      "USDT",
		DeltaMicro: int64(amount),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nfcBalanceView(bal))
}

func (s *Server) nfcReady(w http.ResponseWriter) bool {
	if s == nil || s.cfg == nil || !s.cfg.NFCEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "NFC rail is disabled", "code": "NFC_DISABLED"})
		return false
	}
	if strings.TrimSpace(s.cfg.NFCTokenSecret) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "NFC_TOKEN_SECRET is not configured", "code": "NFC_SECRET_MISSING"})
		return false
	}
	if s.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database unavailable", "code": "DB_UNAVAILABLE"})
		return false
	}
	return true
}

func (s *Server) authorizeNFCTerminal(w http.ResponseWriter, r *http.Request, merchantID, terminalID string) (*database.NFCTerminalPolicy, bool) {
	apiKey := chainFXAPIKeyFromHeader(r)
	terminal, err := s.db.ValidateNFCTerminal(r.Context(), merchantID, terminalID, apiKey)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	if terminal != nil {
		if terminal.MerchantStatus != "active" || terminal.TerminalStatus != "active" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "NFC terminal disabled", "code": "NFC_TERMINAL_DISABLED", "response_code": "57"})
			return nil, false
		}
		return terminal, true
	}
	if !s.cfg.IsProduction() {
		if _, ok := s.authorizeChainFX(w, r); ok {
			return &database.NFCTerminalPolicy{
				MerchantID:        strings.TrimSpace(merchantID),
				TerminalID:        strings.TrimSpace(terminalID),
				MerchantStatus:    "active",
				TerminalStatus:    "active",
				MaxAmountBRLMinor: int64(s.cfg.NFCMaxAmountBRL * 100),
				RiskPolicyVersion: "development",
			}, true
		}
		return nil, false
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "NFC terminal credential required", "code": "NFC_TERMINAL_AUTH_REQUIRED", "response_code": "05"})
	return nil, false
}

func nfcAuthorizationView(a *database.NFCAuthorization, settlements ...*database.MerchantSettlement) map[string]any {
	var settlement *database.MerchantSettlement
	if len(settlements) > 0 {
		settlement = settlements[0]
	}
	out := map[string]any{
		"authorization_id":    a.ID,
		"token_id":            a.TokenID,
		"wallet_address":      a.Wallet,
		"network":             a.Network,
		"merchant_id":         a.MerchantID,
		"terminal_id":         a.TerminalID,
		"amount_brl":          fmt.Sprintf("%.2f", float64(a.AmountBRLMinor)/100),
		"merchant_amount_brl": fmt.Sprintf("%.2f", float64(a.AmountBRLMinor)/100),
		"chainfx_fee_brl":     fmt.Sprintf("%.2f", float64(a.FeeBRLMinor)/100),
		"total_debit_brl":     fmt.Sprintf("%.2f", float64(a.TotalBRLMinor)/100),
		"fee_bps":             a.FeeBps,
		"required_usdt":       fmt.Sprintf("%.6f", float64(a.RequiredUSDTMic)/1_000_000),
		"usdt_rate":           fmt.Sprintf("%.4f", a.USDTRate),
		"status":              a.Status,
		"response_code":       a.ResponseCode,
		"reason":              a.Reason,
		"idempotent":          a.Idempotent,
		"created_at":          a.CreatedAt,
		"rail":                "chainfx_tap",
		"scheme":              "chainfx_own_closed_loop",
		"card_network":        "none",
		"settlement":          nfcSettlementView(a, settlement),
	}
	if a.ExternalRef != "" {
		out["external_ref"] = a.ExternalRef
	}
	if a.HoldExpiresAt != nil {
		out["hold_expires_at"] = *a.HoldExpiresAt
	}
	if a.BRLReservationID != "" {
		out["brl_reservation_id"] = a.BRLReservationID
		out["brl_reservation_status"] = a.BRLReservationStatus
	}
	return out
}

func (s *Server) publishNFCEvent(eventType string, a *database.NFCAuthorization, settlements ...*database.MerchantSettlement) {
	if s == nil || s.workers == nil || s.workers.Bus == nil || a == nil {
		return
	}
	var settlement *database.MerchantSettlement
	if len(settlements) > 0 {
		settlement = settlements[0]
	}
	payload := map[string]any{
		"authorization_id":          a.ID,
		"wallet_address":            a.Wallet,
		"network":                   a.Network,
		"merchant_id":               a.MerchantID,
		"terminal_id":               a.TerminalID,
		"external_ref":              a.ExternalRef,
		"amount_brl_minor":          a.AmountBRLMinor,
		"merchant_amount_brl_minor": a.AmountBRLMinor,
		"chainfx_fee_brl_minor":     a.FeeBRLMinor,
		"total_debit_brl_minor":     a.TotalBRLMinor,
		"fee_bps":                   a.FeeBps,
		"required_usdt_micro":       a.RequiredUSDTMic,
		"rail":                      "chainfx_tap",
		"scheme":                    "chainfx_own_closed_loop",
		"card_network":              "none",
		"fiat_settlement_rail":      "efi_pix_send",
		"merchant_settlement_mode":  "manual",
	}
	if settlement != nil {
		payload["settlement_id"] = settlement.ID
		payload["settlement_status"] = settlement.Status
		payload["provider"] = settlement.Provider
		payload["provider_reference"] = settlement.ProviderReference
		payload["retry_count"] = settlement.RetryCount
		payload["merchant_settlement_mode"] = "manual_or_automatic_worker"
	}
	s.workers.Bus.Publish(workers.Event{
		Type:    eventType,
		OrderID: a.ID,
		Payload: payload,
	})
}

func nfcSettlementView(a *database.NFCAuthorization, settlement *database.MerchantSettlement) map[string]any {
	mode := "not_applicable"
	switch a.Status {
	case database.NFCStatusApproved:
		mode = "hold_active"
	case database.NFCStatusCaptured:
		mode = "merchant_settlement_pending"
	case database.NFCStatusReversed:
		mode = "reversed_no_fiat_settlement"
	case database.NFCStatusExpired:
		mode = "expired_hold_released"
	case database.NFCStatusRequiresFunding:
		mode = "insufficient_usdt"
	case database.NFCStatusDeclined:
		mode = "declined"
	}
	out := map[string]any{
		"fiat_rail":                "efi_pix_send",
		"crypto_source_asset":      "USDT",
		"crypto_debit_source":      "nfc_internal_usdt_ledger",
		"merchant_settlement_mode": mode,
		"card_network":             "none",
	}
	if settlement != nil {
		out["settlement_id"] = settlement.ID
		out["settlement_status"] = settlement.Status
		out["provider"] = settlement.Provider
		out["provider_reference"] = settlement.ProviderReference
		out["txid"] = settlement.TXID
		out["retry_count"] = settlement.RetryCount
	}
	return out
}

func nfcBalanceView(b *database.NFCBalance) map[string]any {
	return map[string]any{
		"wallet_address": b.Wallet,
		"network":        b.Network,
		"asset":          b.Asset,
		"available_usdt": fmt.Sprintf("%.6f", float64(b.AvailableMicro)/1_000_000),
		"locked_usdt":    fmt.Sprintf("%.6f", float64(b.LockedMicro)/1_000_000),
		"updated_at":     b.UpdatedAt,
	}
}
