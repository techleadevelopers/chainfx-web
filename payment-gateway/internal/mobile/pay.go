package mobile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type mobilePayParsed struct {
	ID              string  `json:"parsed_payment_id"`
	PaymentType     string  `json:"payment_type"`
	BeneficiaryName string  `json:"beneficiary_name"`
	Document        string  `json:"document,omitempty"`
	Description     string  `json:"description,omitempty"`
	AmountBRL       float64 `json:"amount_brl,omitempty"`
	RawCode         string  `json:"raw_code,omitempty"`
	Valid           bool    `json:"valid"`
	Error           string  `json:"error,omitempty"`
}

func (s *Server) handleMobilePayParse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RawCode string `json:"raw_code"`
		Source  string `json:"source"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.RawCode) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "raw_code obrigatorio"})
		return
	}
	parsed := parseMobilePayCode(req.RawCode)
	if !parsed.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": parsed.Error, "valid": false})
		return
	}
	writeJSON(w, http.StatusOK, parsed)
}

func (s *Server) handleMobilePayQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RawCode       string  `json:"raw_code"`
		PaymentType   string  `json:"payment_type"`
		AmountBRL     float64 `json:"amount_brl"`
		ParsedPayment string  `json:"parsed_payment_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	parsed := parseMobilePayCode(req.RawCode)
	if !parsed.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": parsed.Error, "valid": false})
		return
	}
	if req.PaymentType != "" {
		parsed.PaymentType = strings.ToLower(strings.TrimSpace(req.PaymentType))
	}
	if req.AmountBRL > 0 {
		parsed.AmountBRL = req.AmountBRL
	}
	quote, ok := s.mobilePayQuotePayload(w, r, parsed)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, quote)
}

func (s *Server) handleMobilePayConfirm(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	idempotencyKey := idempotencyKeyFromCtx(r.Context())
	if idempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "idempotency key obrigatorio"})
		return
	}
	var req struct {
		QuoteID         string  `json:"quote_id"`
		PaymentID       string  `json:"payment_id"`
		ParsedPaymentID string  `json:"parsed_payment_id"`
		RawCode         string  `json:"raw_code"`
		PaymentType     string  `json:"payment_type"`
		AmountBRL       float64 `json:"amount_brl"`
		FundingAsset    string  `json:"funding_asset"`
		FundingSource   string  `json:"funding_source"`
		FundingNetwork  string  `json:"funding_network"`
		TxHash          string  `json:"tx_hash"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	if strings.TrimSpace(req.QuoteID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "quote_id obrigatorio"})
		return
	}
	parsed := parseMobilePayCode(req.RawCode)
	if !parsed.Valid && strings.TrimSpace(req.PaymentID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": parsed.Error, "valid": false})
		return
	}
	network, tokenContract, tokenDecimals, chainID, treasuryAddress, err := s.mobilePayFundingSpec()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, mobileProductError("ROUTE_UNAVAILABLE", "Pagamento indisponivel no momento."))
		return
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil || user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "wallet do usuario nao registrada"})
		return
	}
	wallet := strings.ToLower(strings.TrimSpace(*user.WalletAddress))
	paymentID := strings.TrimSpace(req.PaymentID)
	if paymentID == "" {
		paymentID = "mpay_" + mobilePayHash(uid + ":" + idempotencyKey)[:24]
	}
	requiredMic := int64(0)
	amountBRL := parsed.AmountBRL

	if err := mobileDB(s.db).ensureMobilePaySchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema mobile pay indisponivel"})
		return
	}
	tx, err := s.db.SQL.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database indisponivel"})
		return
	}
	defer tx.Rollback() //nolint:errcheck

	existing, existingStatus, err := txGetMobilePaymentByIdempotency(r, tx, uid, idempotencyKey)
	if err != nil {
		slog.Error("mobile payment idempotency lookup failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	if existing != "" && strings.TrimSpace(req.TxHash) == "" {
		var existingRequiredMic int64
		var existingNetwork, existingTreasury, existingToken string
		var existingDecimals int
		_ = tx.QueryRowContext(r.Context(), `
SELECT required_usdt_micro, funding_network, treasury_address, funding_token_contract, funding_token_decimals
FROM mobile_payment_intents WHERE id=$1 AND user_id=$2::uuid`, existing, uid).Scan(
			&existingRequiredMic, &existingNetwork, &existingTreasury, &existingToken, &existingDecimals)
		if existingRequiredMic > 0 {
			requiredMic = existingRequiredMic
		}
		if strings.TrimSpace(existingNetwork) != "" {
			network = existingNetwork
		}
		if strings.TrimSpace(existingTreasury) != "" {
			treasuryAddress = existingTreasury
		}
		if strings.TrimSpace(existingToken) != "" {
			tokenContract = existingToken
		}
		if existingDecimals > 0 {
			tokenDecimals = existingDecimals
		}
		_ = tx.Commit()
		writeJSON(w, http.StatusOK, map[string]any{
			"payment_id":       existing,
			"status":           existingStatus,
			"idempotent":       true,
			"funding_asset":    "USDT",
			"funding_network":  network,
			"treasury_address": treasuryAddress,
			"token_contract":   tokenContract,
			"token_decimals":   tokenDecimals,
			"chain_id":         chainID,
			"required_usdt":    fmt.Sprintf("%.6f", float64(requiredMic)/1_000_000),
			"next_step":        "send_usdt_to_treasury",
		})
		return
	}

	if strings.TrimSpace(req.PaymentID) != "" {
		var storedFeeBRL, storedTotalBRL, storedUSDTRate float64
		var quoteExpiresAt *time.Time
		err = tx.QueryRowContext(r.Context(), `
SELECT id, wallet_address, payment_type, amount_brl::float8, fee_brl::float8, total_brl::float8,
       usdt_rate::float8, required_usdt_micro, funding_network, funding_token_contract,
       funding_token_decimals, treasury_address, quote_expires_at
FROM mobile_payment_intents
WHERE id=$1 AND user_id=$2::uuid
FOR UPDATE`, req.PaymentID, uid).Scan(
			&paymentID, &wallet, &parsed.PaymentType, &parsed.AmountBRL,
			&storedFeeBRL, &storedTotalBRL, &storedUSDTRate, &requiredMic,
			&network, &tokenContract, &tokenDecimals, &treasuryAddress, &quoteExpiresAt,
		)
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "pagamento nao encontrado"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar pagamento mobile"})
			return
		}
		amountBRL = parsed.AmountBRL
		if quoteExpiresAt != nil && time.Now().UTC().After(quoteExpiresAt.UTC()) && strings.TrimSpace(req.TxHash) != "" {
			_, _ = tx.ExecContext(r.Context(), `
UPDATE mobile_payment_intents
SET status='manual_review', provider_status='funding_after_quote_expired',
    error_message='funding recebido depois da expiracao do quote; revisar refund',
    updated_at=NOW()
WHERE id=$1 AND user_id=$2::uuid`, paymentID, uid)
			_ = tx.Commit()
			writeJSON(w, http.StatusConflict, map[string]any{
				"payment_id":      paymentID,
				"status":          "manual_review",
				"provider_status": "funding_after_quote_expired",
				"next_step":       "manual_refund_review",
				"error":           "quote expirado antes da confirmacao do funding",
			})
			return
		}
	} else {
		quoteRecord, err := txGetMobilePaymentQuote(r.Context(), tx, uid, strings.TrimSpace(req.QuoteID))
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "quote_id invalido ou nao pertence ao usuario"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar quote"})
			return
		}
		if quoteRecord.ConsumedAt.Valid {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "quote_id ja consumido"})
			return
		}
		if time.Now().UTC().After(quoteRecord.ExpiresAt.UTC()) {
			_, _ = tx.ExecContext(r.Context(), `
UPDATE mobile_payment_quotes
SET status='expired', updated_at=NOW()
WHERE quote_id=$1 AND user_id=$2::uuid AND consumed_at IS NULL`, quoteRecord.QuoteID, uid)
			writeJSON(w, http.StatusConflict, map[string]any{"error": "quote expirado", "status": "expired"})
			return
		}
		if parsed.ID != "" && quoteRecord.ParsedPaymentID != "" && parsed.ID != quoteRecord.ParsedPaymentID {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "quote nao corresponde ao pagamento escaneado"})
			return
		}
		parsed.PaymentType = quoteRecord.PaymentType
		parsed.BeneficiaryName = quoteRecord.BeneficiaryName
		parsed.Document = quoteRecord.Document
		parsed.Description = quoteRecord.Description
		parsed.AmountBRL = quoteRecord.AmountBRL
		amountBRL = quoteRecord.AmountBRL
		requiredMic = quoteRecord.RequiredUSDTMic
		network = quoteRecord.FundingNetwork
		tokenContract = quoteRecord.FundingTokenContract
		tokenDecimals = quoteRecord.FundingTokenDecimals
		treasuryAddress = quoteRecord.TreasuryAddress
		_, err = tx.ExecContext(r.Context(), `
INSERT INTO mobile_payment_intents
  (id, user_id, wallet_address, idempotency_key, quote_id, raw_code, payment_type, beneficiary_name,
   document, description, amount_brl, fee_brl, total_brl, usdt_rate, required_usdt_micro,
   status, provider, provider_status, funding_asset, funding_network, funding_token_contract,
   funding_token_decimals, treasury_address, quote_expires_at)
VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
        'awaiting_funding','efi','awaiting_usdt_treasury','USDT',$16,$17,$18,$19,$20)`,
			paymentID, uid, wallet, idempotencyKey, quoteRecord.QuoteID, req.RawCode, parsed.PaymentType, parsed.BeneficiaryName,
			parsed.Document, parsed.Description, quoteRecord.AmountBRL, quoteRecord.FeeBRL, quoteRecord.TotalBRL,
			quoteRecord.USDTRate, requiredMic, network, tokenContract, tokenDecimals, treasuryAddress, quoteRecord.ExpiresAt)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar pagamento mobile"})
			return
		}
		if _, err := tx.ExecContext(r.Context(), `
UPDATE mobile_payment_quotes
SET status='consumed', consumed_at=NOW(), consumed_intent_id=$3, updated_at=NOW()
WHERE quote_id=$1 AND user_id=$2::uuid AND consumed_at IS NULL`, quoteRecord.QuoteID, uid, paymentID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao consumir quote"})
			return
		}
	}

	txHash := strings.TrimSpace(req.TxHash)
	if txHash == "" {
		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar pagamento"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"payment_id":       paymentID,
			"status":           "awaiting_funding",
			"provider":         "efi",
			"provider_status":  "awaiting_usdt_treasury",
			"funding_asset":    "USDT",
			"funding_network":  network,
			"funding_source":   "onchain_treasury_hot",
			"treasury_address": treasuryAddress,
			"token_contract":   tokenContract,
			"token_decimals":   tokenDecimals,
			"chain_id":         chainID,
			"required_usdt":    fmt.Sprintf("%.6f", float64(requiredMic)/1_000_000),
			"amount_brl":       fmt.Sprintf("%.2f", amountBRL),
			"next_step":        "send_usdt_to_treasury",
		})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar pagamento"})
		return
	}
	receipt, err := s.verifyMobilePayUSDTFunding(r.Context(), network, txHash, tokenContract, tokenDecimals, wallet, treasuryAddress, requiredMic)
	if err != nil {
		if pendingStatus, ok := isMobilePayFundingPending(err); ok {
			_, _ = s.db.SQL.ExecContext(r.Context(), `
UPDATE mobile_payment_intents
SET status=$3, provider_status='awaiting_usdt_confirmations', funding_tx_hash=$4, updated_at=NOW()
WHERE id=$1 AND user_id=$2::uuid`, paymentID, uid, pendingStatus, strings.ToLower(txHash))
			writeJSON(w, http.StatusAccepted, map[string]any{
				"payment_id":      paymentID,
				"status":          pendingStatus,
				"provider_status": "awaiting_usdt_confirmations",
				"tx_hash":         strings.ToLower(txHash),
				"message":         "Transacao aguardando confirmacao.",
				"next_step":       "retry_funding_confirmation",
			})
			return
		}
		_, _ = s.db.SQL.ExecContext(r.Context(), `
UPDATE mobile_payment_intents
SET status='manual_review', provider_status='funding_verification_failed', error_message=$3,
    funding_tx_hash=$4, updated_at=NOW()
WHERE id=$1 AND user_id=$2::uuid`, paymentID, uid, err.Error(), strings.ToLower(txHash))
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "MANUAL_REVIEW", "error": "Pagamento em analise.", "status": "manual_review"})
		return
	}
	res, err := s.db.SQL.ExecContext(r.Context(), `
WITH updated AS (
  UPDATE mobile_payment_intents
  SET status='funding_confirmed',
      provider_status='provider_execution_pending',
      funding_tx_hash=$3,
      funding_amount_raw=$4,
      funding_block_number=$5,
      funding_confirmations=$6,
      funding_confirmed_at=COALESCE(funding_confirmed_at, NOW()),
      updated_at=NOW()
  WHERE id=$1 AND user_id=$2::uuid
    AND (funding_tx_hash='' OR lower(funding_tx_hash)=lower($3))
  RETURNING id, user_id, required_usdt_micro
)
INSERT INTO mobile_payment_funding_transactions
  (id, payment_intent_id, user_id, tx_hash, network, asset, token_contract, token_decimals,
   from_address, to_address, amount_raw, required_amount_raw, block_number, block_hash, log_index,
   confirmations, status)
SELECT 'mpfund_' || substr(md5($3), 1, 24), id, user_id, lower($3), $7, 'USDT', $8, $9,
       $10, $11, $4, $12, $5, $13, $14, $6, 'confirmed'
FROM updated
ON CONFLICT (tx_hash) DO NOTHING`,
		paymentID, uid, receipt.TxHash, receipt.AmountRaw, int64(receipt.BlockNumber), int64(receipt.Confirmations),
		network, tokenContract, tokenDecimals, receipt.From, receipt.To, receipt.RequiredRaw, receipt.BlockHash, receipt.LogIndex)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "tx_hash ja usado ou erro ao registrar funding"})
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "funding nao foi registrado; verifique se o payment_id ja possui outro tx_hash"})
		return
	}
	providerKey := "mpay-efi-" + mobilePayHash(paymentID)[:24]
	_, _ = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO mobile_payment_executions
  (id, payment_intent_id, user_id, provider, provider_idempotency_key, status, next_attempt_at)
VALUES ($1,$2,$3::uuid,'efi',$4,'pending',NOW())
ON CONFLICT (payment_intent_id, provider) DO NOTHING`,
		"mpexec_"+mobilePayHash(paymentID + ":efi")[:24], paymentID, uid, providerKey)
	_, _ = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO mobile_payment_ledger_entries
  (id, payment_intent_id, user_id, entry_type, asset, network, amount_micro, tx_hash, provider, metadata)
VALUES ($1,$2,$3::uuid,'funding_confirmed','USDT',$4,$5,$6,'efi',
        jsonb_build_object('treasury_address',$7,'token_contract',$8))
ON CONFLICT (payment_intent_id, entry_type) DO NOTHING`,
		"mpledger_"+mobilePayHash(paymentID + ":funding_confirmed")[:24], paymentID, uid, network, requiredMic, receipt.TxHash, treasuryAddress, tokenContract)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"payment_id":      paymentID,
		"status":          "funding_confirmed",
		"provider":        "efi",
		"provider_status": "provider_execution_pending",
		"funding_asset":   "USDT",
		"funding_source":  "onchain_treasury_hot",
		"funding_tx_hash": receipt.TxHash,
		"confirmations":   receipt.Confirmations,
		"required_usdt":   fmt.Sprintf("%.6f", float64(requiredMic)/1_000_000),
		"amount_brl":      fmt.Sprintf("%.2f", amountBRL),
		"execution_id":    "mpexec_" + mobilePayHash(paymentID + ":efi")[:24],
		"next_step":       "efi_provider_execution",
	})
}

func (s *Server) handleMobilePayStatus(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payment id obrigatorio"})
		return
	}
	var out struct {
		ID, PaymentType, BeneficiaryName, Status, ProviderStatus, ProviderReference, ErrorMessage string
		FundingAsset, FundingNetwork, TreasuryAddress, TokenContract, FundingTxHash               string
		AmountBRL, FeeBRL, USDTRate                                                               float64
		RequiredUSDTMic                                                                           int64
		FundingConfirmations                                                                      int64
		CreatedAt, UpdatedAt                                                                      time.Time
	}
	err := s.db.SQL.QueryRowContext(r.Context(), `
SELECT id, payment_type, beneficiary_name, amount_brl::float8, fee_brl::float8, usdt_rate::float8,
       required_usdt_micro, status, provider_status, provider_reference, error_message,
       funding_asset, funding_network, treasury_address, funding_token_contract, funding_tx_hash,
       funding_confirmations, created_at, updated_at
FROM mobile_payment_intents
WHERE id=$1 AND user_id=$2::uuid`, id, uid).Scan(
		&out.ID, &out.PaymentType, &out.BeneficiaryName, &out.AmountBRL, &out.FeeBRL, &out.USDTRate,
		&out.RequiredUSDTMic, &out.Status, &out.ProviderStatus, &out.ProviderReference, &out.ErrorMessage,
		&out.FundingAsset, &out.FundingNetwork, &out.TreasuryAddress, &out.TokenContract, &out.FundingTxHash,
		&out.FundingConfirmations,
		&out.CreatedAt, &out.UpdatedAt)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "pagamento nao encontrado"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar pagamento"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"payment_id": out.ID, "payment_type": out.PaymentType, "beneficiary_name": out.BeneficiaryName,
		"amount_brl": fmt.Sprintf("%.2f", out.AmountBRL), "fee_brl": fmt.Sprintf("%.2f", out.FeeBRL),
		"usdt_rate": fmt.Sprintf("%.4f", out.USDTRate), "required_usdt": fmt.Sprintf("%.6f", float64(out.RequiredUSDTMic)/1_000_000),
		"status": out.Status, "provider_status": out.ProviderStatus, "provider_reference": out.ProviderReference,
		"funding_asset": out.FundingAsset, "funding_network": out.FundingNetwork, "treasury_address": out.TreasuryAddress,
		"token_contract": out.TokenContract, "funding_tx_hash": out.FundingTxHash, "confirmations": out.FundingConfirmations,
		"error_message": out.ErrorMessage, "created_at": out.CreatedAt, "updated_at": out.UpdatedAt,
	})
}

func (s *Server) mobilePayQuotePayload(w http.ResponseWriter, r *http.Request, parsed mobilePayParsed) (map[string]any, bool) {
	if !parsed.Valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": parsed.Error, "valid": false})
		return nil, false
	}
	if strings.ToLower(strings.TrimSpace(parsed.PaymentType)) != "pix" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "somente QR Pix e suportado para QR Pay mobile"})
		return nil, false
	}
	if parsed.AmountBRL <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl obrigatorio no QR ou payload"})
		return nil, false
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), userIDFromCtx(r))
	if err != nil || user == nil || user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "wallet do usuario nao registrada"})
		return nil, false
	}
	rate := 0.0
	if s.workers != nil && s.workers.PriceWorker != nil {
		rate = s.workers.PriceWorker.GetPrice("BRL")
	}
	if rate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "USDT/BRL indisponivel"})
		return nil, false
	}
	network, tokenContract, tokenDecimals, chainID, treasuryAddress, err := s.mobilePayFundingSpec()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, mobileProductError("ROUTE_UNAVAILABLE", "Pagamento indisponivel no momento."))
		return nil, false
	}
	feeBps := 0
	if s.cfg != nil {
		feeBps = firstPositiveIntMobile(s.cfg.NFCFeeBps, s.cfg.M2MPixFeeBps)
	}
	feeBRL := math.Round((parsed.AmountBRL*float64(feeBps)/10_000)*100) / 100
	totalBRL := parsed.AmountBRL + feeBRL
	totalUSDT := totalBRL / rate
	onchain := s.mobileOnchainWalletBalancesAll(r.Context(), *user.WalletAddress)
	available := 0.0
	switch network {
	case "POLYGON":
		available = onchain.polyUSDT
	default:
		available = onchain.bscUSDT
	}
	expiresAt := time.Now().UTC().Add(90 * time.Second)
	quoteID := mobileSwapQuoteID()
	requiredMic := int64(math.Ceil(totalUSDT * 1_000_000))
	if err := mobileDB(s.db).ensureMobilePaySchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema mobile pay indisponivel"})
		return nil, false
	}
	if existing := s.mobilePayExistingForRawCode(r.Context(), userIDFromCtx(r), parsed.RawCode); existing != nil {
		return existing, true
	}
	_, err = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO mobile_payment_quotes
  (quote_id, user_id, wallet_address, parsed_payment_id, raw_code_hash, payment_type,
   beneficiary_name, document, description, amount_brl, fee_brl, total_brl, usdt_rate,
   required_usdt_micro, funding_asset, funding_network, funding_token_contract,
   funding_token_decimals, treasury_address, expires_at)
VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'USDT',$15,$16,$17,$18,$19)`,
		quoteID, userIDFromCtx(r), strings.ToLower(strings.TrimSpace(*user.WalletAddress)),
		parsed.ID, mobilePayHash(parsed.RawCode), parsed.PaymentType, parsed.BeneficiaryName,
		parsed.Document, parsed.Description, parsed.AmountBRL, feeBRL, totalBRL, rate, requiredMic,
		network, tokenContract, tokenDecimals, treasuryAddress, expiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao persistir quote"})
		return nil, false
	}
	return map[string]any{
		"quote_id": quoteID, "parsed_payment_id": parsed.ID, "payment_type": parsed.PaymentType,
		"beneficiary_name": parsed.BeneficiaryName, "amount_brl": parsed.AmountBRL, "fee_brl": feeBRL,
		"total_brl": totalBRL, "usdt_rate": rate,
		"total_usdt":     fmt.Sprintf("%.6f", float64(requiredMic)/1_000_000),
		"required_usdt":  fmt.Sprintf("%.6f", float64(requiredMic)/1_000_000),
		"available_usdt": available, "locked_usdt": 0, "funding_asset": "USDT",
		"funding_network": network, "funding_source": "onchain_treasury_hot",
		"treasury_address": treasuryAddress, "token_contract": tokenContract, "token_decimals": tokenDecimals,
		"chain_id": chainID, "expires_at": expiresAt,
		"has_sufficient_balance": available >= totalUSDT,
	}, true
}

func (s *Server) mobilePayExistingForRawCode(ctx context.Context, userID, rawCode string) map[string]any {
	rawCode = strings.TrimSpace(rawCode)
	if rawCode == "" || s == nil || s.db == nil || s.db.SQL == nil {
		return nil
	}
	var out struct {
		ID, QuoteID, PaymentType, BeneficiaryName, Status, ProviderStatus string
		AmountBRL, FeeBRL, TotalBRL, USDTRate                             float64
		RequiredUSDTMic                                                   int64
	}
	err := s.db.SQL.QueryRowContext(ctx, `
SELECT id, quote_id, payment_type, beneficiary_name, amount_brl::float8, fee_brl::float8,
       total_brl::float8, usdt_rate::float8, required_usdt_micro, status, provider_status
FROM mobile_payment_intents
WHERE user_id=$1::uuid
  AND raw_code=$2
  AND status IN ('awaiting_funding','funding_seen','funding_confirmed','processing',
                 'provider_pending','provider_unknown','completed','refund_pending',
                 'refunded','manual_review')
ORDER BY created_at DESC
LIMIT 1`, userID, rawCode).Scan(
		&out.ID, &out.QuoteID, &out.PaymentType, &out.BeneficiaryName, &out.AmountBRL, &out.FeeBRL,
		&out.TotalBRL, &out.USDTRate, &out.RequiredUSDTMic, &out.Status, &out.ProviderStatus)
	if err != nil {
		return nil
	}
	requiredUSDT := fmt.Sprintf("%.6f", float64(out.RequiredUSDTMic)/1_000_000)
	return map[string]any{
		"quote_id": out.QuoteID, "payment_id": out.ID, "existing_payment_id": out.ID,
		"payment_type": out.PaymentType, "beneficiary_name": out.BeneficiaryName,
		"amount_brl": out.AmountBRL, "fee_brl": out.FeeBRL, "total_brl": out.TotalBRL,
		"usdt_rate": out.USDTRate, "total_usdt": requiredUSDT, "required_usdt": requiredUSDT,
		"status": out.Status, "provider_status": out.ProviderStatus, "duplicate": true,
	}
}

func parseMobilePayCode(raw string) mobilePayParsed {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return mobilePayParsed{Valid: false, Error: "raw_code obrigatorio"}
	}
	upper := strings.ToUpper(raw)
	amount := parseMobilePixAmount(raw)
	if !strings.Contains(upper, "BR.GOV.BCB.PIX") {
		return mobilePayParsed{RawCode: raw, Valid: false, Error: "QR Pay mobile aceita somente BR Code Pix"}
	}
	if emvTag(emvTag(raw, "26"), "00") != "BR.GOV.BCB.PIX" {
		return mobilePayParsed{RawCode: raw, Valid: false, Error: "BR Code Pix invalido"}
	}
	if mobilePixKeyFromBRCode(raw) == "" {
		return mobilePayParsed{RawCode: raw, Valid: false, Error: "chave Pix nao encontrada no BR Code"}
	}
	if amount <= 0 {
		return mobilePayParsed{RawCode: raw, Valid: false, Error: "QR Pix sem valor nao e suportado neste fluxo"}
	}
	beneficiary := mobilePayFirst(emvTag(raw, "59"), "Pagamento Pix")
	if beneficiary == "" {
		return mobilePayParsed{RawCode: raw, Valid: false, Error: "beneficiario Pix ausente"}
	}
	return mobilePayParsed{
		ID:              "parsed_" + mobilePayHash(raw)[:24],
		PaymentType:     "pix",
		BeneficiaryName: beneficiary,
		Description:     "QR Pix escaneado",
		AmountBRL:       amount,
		RawCode:         raw,
		Valid:           true,
	}
}

func parseMobilePixAmount(raw string) float64 {
	value := emvTag(raw, "54")
	if value == "" {
		return 0
	}
	amount, _ := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64)
	return amount
}

func parseMobileBarcodeAmount(raw string) float64 {
	digits := onlyDigitsMobile(raw)
	if len(digits) < 44 {
		return 0
	}
	cents, _ := strconv.ParseInt(digits[len(digits)-10:], 10, 64)
	if cents <= 0 {
		return 0
	}
	return float64(cents) / 100
}

func emvTag(raw, tag string) string {
	for i := 0; i+4 <= len(raw); {
		current := raw[i : i+2]
		size, err := strconv.Atoi(raw[i+2 : i+4])
		if err != nil || size < 0 || i+4+size > len(raw) {
			return ""
		}
		value := raw[i+4 : i+4+size]
		if current == tag {
			return strings.TrimSpace(value)
		}
		i += 4 + size
	}
	return ""
}

func txGetMobilePaymentByIdempotency(r *http.Request, tx *sql.Tx, userID, key string) (id, status string, err error) {
	err = tx.QueryRowContext(r.Context(), `
SELECT id, status
FROM mobile_payment_intents
WHERE user_id=$1::uuid AND idempotency_key=$2
FOR UPDATE`, userID, key).Scan(&id, &status)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return id, status, err
}

type mobilePaymentQuoteRecord struct {
	QuoteID              string
	ParsedPaymentID      string
	PaymentType          string
	BeneficiaryName      string
	Document             string
	Description          string
	FundingNetwork       string
	FundingTokenContract string
	TreasuryAddress      string
	AmountBRL            float64
	FeeBRL               float64
	TotalBRL             float64
	USDTRate             float64
	RequiredUSDTMic      int64
	FundingTokenDecimals int
	ExpiresAt            time.Time
	ConsumedAt           sql.NullTime
}

func txGetMobilePaymentQuote(ctx context.Context, tx *sql.Tx, userID, quoteID string) (*mobilePaymentQuoteRecord, error) {
	out := &mobilePaymentQuoteRecord{}
	err := tx.QueryRowContext(ctx, `
SELECT quote_id, parsed_payment_id, payment_type, beneficiary_name, document, description,
       amount_brl::float8, fee_brl::float8, total_brl::float8, usdt_rate::float8,
       required_usdt_micro, funding_network, funding_token_contract, funding_token_decimals,
       treasury_address, expires_at, consumed_at
FROM mobile_payment_quotes
WHERE quote_id=$1 AND user_id=$2::uuid
FOR UPDATE`, quoteID, userID).Scan(
		&out.QuoteID, &out.ParsedPaymentID, &out.PaymentType, &out.BeneficiaryName,
		&out.Document, &out.Description, &out.AmountBRL, &out.FeeBRL, &out.TotalBRL,
		&out.USDTRate, &out.RequiredUSDTMic, &out.FundingNetwork, &out.FundingTokenContract,
		&out.FundingTokenDecimals, &out.TreasuryAddress, &out.ExpiresAt, &out.ConsumedAt,
	)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func mobilePayHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func numberFromMap(value map[string]any, key string) float64 {
	switch v := value[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		out, _ := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
		return out
	default:
		return 0
	}
}

func mobilePayFirst(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
