package mobile

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
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
	writeJSON(w, http.StatusOK, parseMobilePayCode(req.RawCode))
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
		ParsedPaymentID string  `json:"parsed_payment_id"`
		RawCode         string  `json:"raw_code"`
		PaymentType     string  `json:"payment_type"`
		AmountBRL       float64 `json:"amount_brl"`
		FundingAsset    string  `json:"funding_asset"`
		FundingSource   string  `json:"funding_source"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	parsed := parseMobilePayCode(req.RawCode)
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
	requiredMic := int64(math.Ceil(numberFromMap(quote, "total_usdt") * 1_000_000))
	if requiredMic <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "total_usdt invalido"})
		return
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil || user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "wallet do usuario nao registrada"})
		return
	}
	wallet := strings.ToLower(strings.TrimSpace(*user.WalletAddress))
	paymentID := "mpay_" + mobilePayHash(uid + ":" + idempotencyKey)[:24]
	providerStatus := "pending_mobile_adapter"
	if strings.EqualFold(parsed.PaymentType, "pix") {
		providerStatus = "pending_efi_pix_send"
	}

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
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if existing != "" {
		_ = tx.Commit()
		writeJSON(w, http.StatusOK, map[string]any{"payment_id": existing, "status": existingStatus, "idempotent": true})
		return
	}

	res, err := tx.ExecContext(r.Context(), `
UPDATE nfc_wallet_balances
SET available_usdt_micro = available_usdt_micro - $3,
    locked_usdt_micro = locked_usdt_micro + $3,
    updated_at = NOW()
WHERE lower(wallet_address) = lower($1)
  AND network = $2
  AND asset = 'USDT'
  AND available_usdt_micro >= $3`,
		wallet, "BSC", requiredMic)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao travar saldo USDT"})
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": "saldo USDT insuficiente", "code": "INSUFFICIENT_USDT"})
		return
	}

	_, err = tx.ExecContext(r.Context(), `
INSERT INTO mobile_payment_intents
  (id, user_id, wallet_address, idempotency_key, raw_code, payment_type, beneficiary_name,
   document, description, amount_brl, fee_brl, usdt_rate, required_usdt_micro, status, provider_status)
VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'processing',$14)`,
		paymentID, uid, wallet, idempotencyKey, req.RawCode, parsed.PaymentType, parsed.BeneficiaryName,
		parsed.Document, parsed.Description, parsed.AmountBRL, numberFromMap(quote, "fee_brl"), numberFromMap(quote, "usdt_rate"),
		requiredMic, providerStatus)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar pagamento mobile"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar pagamento"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"payment_id":      paymentID,
		"status":          "processing",
		"provider":        "efi",
		"provider_status": providerStatus,
		"funding_asset":   "USDT",
		"funding_source":  "mobile_internal_usdt_ledger",
		"required_usdt":   fmt.Sprintf("%.6f", float64(requiredMic)/1_000_000),
		"amount_brl":      fmt.Sprintf("%.2f", parsed.AmountBRL),
		"next_step":       "mobile_pay_provider_settlement",
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
		AmountBRL, FeeBRL, USDTRate                                                               float64
		RequiredUSDTMic                                                                           int64
		CreatedAt, UpdatedAt                                                                      time.Time
	}
	err := s.db.SQL.QueryRowContext(r.Context(), `
SELECT id, payment_type, beneficiary_name, amount_brl::float8, fee_brl::float8, usdt_rate::float8,
       required_usdt_micro, status, provider_status, provider_reference, error_message, created_at, updated_at
FROM mobile_payment_intents
WHERE id=$1 AND user_id=$2::uuid`, id, uid).Scan(
		&out.ID, &out.PaymentType, &out.BeneficiaryName, &out.AmountBRL, &out.FeeBRL, &out.USDTRate,
		&out.RequiredUSDTMic, &out.Status, &out.ProviderStatus, &out.ProviderReference, &out.ErrorMessage,
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
		"error_message": out.ErrorMessage, "created_at": out.CreatedAt, "updated_at": out.UpdatedAt,
	})
}

func (s *Server) mobilePayQuotePayload(w http.ResponseWriter, r *http.Request, parsed mobilePayParsed) (map[string]any, bool) {
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
	feeBps := 0
	if s.cfg != nil {
		feeBps = firstPositiveIntMobile(s.cfg.NFCFeeBps, s.cfg.M2MPixFeeBps)
	}
	feeBRL := math.Round((parsed.AmountBRL*float64(feeBps)/10_000)*100) / 100
	totalBRL := parsed.AmountBRL + feeBRL
	totalUSDT := totalBRL / rate
	bal, _ := s.db.GetNFCBalance(r.Context(), *user.WalletAddress, "BSC")
	available := 0.0
	locked := 0.0
	if bal != nil {
		available = float64(bal.AvailableMicro) / 1_000_000
		locked = float64(bal.LockedMicro) / 1_000_000
	}
	quoteID := "mpq_" + mobilePayHash(parsed.ID + ":" + fmt.Sprintf("%.2f", parsed.AmountBRL) + ":" + fmt.Sprintf("%.4f", rate))[:24]
	return map[string]any{
		"quote_id": quoteID, "parsed_payment_id": parsed.ID, "payment_type": parsed.PaymentType,
		"beneficiary_name": parsed.BeneficiaryName, "amount_brl": parsed.AmountBRL, "fee_brl": feeBRL,
		"total_brl": totalBRL, "usdt_rate": rate, "total_usdt": totalUSDT, "required_usdt": totalUSDT,
		"available_usdt": available, "locked_usdt": locked, "funding_asset": "USDT",
		"funding_source": "mobile_internal_usdt_ledger", "expires_at": time.Now().UTC().Add(90 * time.Second),
		"has_sufficient_balance": available >= totalUSDT,
	}, true
}

func parseMobilePayCode(raw string) mobilePayParsed {
	raw = strings.TrimSpace(raw)
	paymentType := "boleto"
	beneficiary := "Boleto bancario"
	description := "Codigo escaneado"
	amount := parseMobilePixAmount(raw)
	if strings.Contains(strings.ToUpper(raw), "BR.GOV.BCB.PIX") {
		paymentType = "pix"
		beneficiary = mobilePayFirst(emvTag(raw, "59"), "Pagamento Pix")
		description = "QR Pix escaneado"
	} else if strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://") {
		paymentType = "payment_link"
		beneficiary = "Link de pagamento"
	} else if amount <= 0 {
		amount = parseMobileBarcodeAmount(raw)
	}
	return mobilePayParsed{
		ID:              "parsed_" + mobilePayHash(raw)[:24],
		PaymentType:     paymentType,
		BeneficiaryName: beneficiary,
		Description:     description,
		AmountBRL:       amount,
		RawCode:         raw,
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
