package mobile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"payment-gateway/internal/email"
	"payment-gateway/internal/privacy"
)

type mobileGiftCardProduct struct {
	ID                 string
	ProviderID         string
	ProviderSlug       string
	ProviderName       string
	ProductID          string
	Brand              string
	Title              string
	Description        string
	Category           string
	Currency           string
	FaceValueMinor     int64
	PriceBRLMinor      int64
	DiscountBps        int
	ProductType        string
	DeliveryMode       string
	ImageURL           string
	RequiresKYC        bool
	CatalogID          string
	Subtitle           string
	Badge              string
	OfferText          string
	SortOrder          int
	Packages           []commerceProductPackage
	MinimumAmountMinor int64
	MaximumAmountMinor int64
}

type mobileGiftCardQuote struct {
	QuoteID              string
	Product              mobileGiftCardProduct
	Quantity             int
	AmountBRLMinor       int64
	FeeBRLMinor          int64
	TotalBRLMinor        int64
	USDTRateMicro        int64
	RequiredUSDTMicro    int64
	AvailableUSDTMicro   int64
	LockedUSDTMicro      int64
	OnchainUSDTMicro     int64
	HasSufficientBalance bool
	ExpiresAt            time.Time
	RecipientPhone       string
}

type giftCardProviderResult struct {
	Status            string
	ProviderStatus    string
	ProviderReference string
	RedemptionCode    string
	RedemptionPIN     string
	RedemptionURL     string
	EmailStatus       string
	ErrorMessage      string
}

func (s *Server) handleGiftCardCatalog(w http.ResponseWriter, r *http.Request) {
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema gift cards indisponivel"})
		return
	}
	rows, err := s.db.SQL.QueryContext(r.Context(), `
SELECT pp.id, pp.provider_id, p.slug, p.name, pp.product_id, pp.brand, pp.title, pp.description,
       pp.category, pp.currency, pp.face_value_minor, pp.price_brl::text, pp.discount_bps,
       pp.product_type, pp.delivery_mode, pp.image_url, pp.requires_kyc,
       gc.id, gc.subtitle, gc.badge, gc.offer_text, gc.sort_order
FROM mobile_gift_cards gc
JOIN gift_card_provider_products pp ON pp.id = gc.provider_product_id
JOIN gift_card_providers p ON p.id = pp.provider_id
WHERE gc.active=true AND pp.active=true AND p.status='active'
ORDER BY gc.sort_order ASC, pp.brand ASC`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar catalogo"})
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		product := mobileGiftCardProduct{}
		var priceText string
		if err := rows.Scan(
			&product.ID, &product.ProviderID, &product.ProviderSlug, &product.ProviderName, &product.ProductID,
			&product.Brand, &product.Title, &product.Description, &product.Category, &product.Currency,
			&product.FaceValueMinor, &priceText, &product.DiscountBps, &product.ProductType, &product.DeliveryMode,
			&product.ImageURL, &product.RequiresKYC, &product.CatalogID, &product.Subtitle, &product.Badge,
			&product.OfferText, &product.SortOrder,
		); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao montar catalogo"})
			return
		}
		product.PriceBRLMinor = decimalToMinor(priceText, brlMinorScale)
		items = append(items, giftCardProductPayload(product))
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao ler catalogo"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGiftCardQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProductID      string `json:"product_id"`
		Quantity       int    `json:"quantity"`
		FundingMethod  string `json:"funding_method"`
		RecipientPhone string `json:"recipient_phone"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ProductID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product_id obrigatorio"})
		return
	}
	quote, ok := s.mobileGiftCardQuotePayload(w, r, req.ProductID, req.Quantity, req.RecipientPhone)
	if !ok {
		return
	}
	fundingMethod := normalizeGiftCardFundingMethod(req.FundingMethod)
	s.recordGiftCardQuote(r, quote, fundingMethod)
	writeJSON(w, http.StatusOK, giftCardQuotePayload(quote, fundingMethod))
}

func (s *Server) handleGiftCardProduct(w http.ResponseWriter, r *http.Request) {
	productID := strings.TrimSpace(r.PathValue("id"))
	if productID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product id obrigatorio"})
		return
	}
	product, err := s.getGiftCardProduct(r, productID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "gift card nao encontrado"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar gift card"})
		return
	}
	writeJSON(w, http.StatusOK, giftCardProductPayload(product))
}

func (s *Server) handleGiftCardPurchase(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	idempotencyKey := idempotencyKeyFromCtx(r.Context())
	if idempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "idempotency key obrigatorio"})
		return
	}
	var req struct {
		QuoteID        string `json:"quote_id"`
		ProductID      string `json:"product_id"`
		Quantity       int    `json:"quantity"`
		UnitPrice      any    `json:"unit_price"`
		FundingMethod  string `json:"funding_method"`
		RecipientPhone string `json:"recipient_phone"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ProductID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product_id obrigatorio"})
		return
	}
	unitPriceMinor := decimalToMinor(req.UnitPrice, brlMinorScale)
	quote, ok := s.mobileGiftCardQuotePayloadWithUnitPrice(w, r, req.ProductID, req.Quantity, unitPriceMinor, req.RecipientPhone)
	if !ok {
		return
	}
	if req.QuoteID != "" && req.QuoteID != quote.QuoteID {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "cotacao expirada", "code": "QUOTE_EXPIRED"})
		return
	}
	fundingMethod := normalizeGiftCardFundingMethod(req.FundingMethod)
	level, err := mobileDB(s.db).GetApprovedKYCLevel(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao validar KYC"})
		return
	}
	if quote.Product.RequiresKYC && int(level) <= 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "KYC obrigatorio para comprar gift card", "code": "KYC_REQUIRED"})
		return
	}
	recipientPhone := normalizeE164Phone(req.RecipientPhone, "BR")
	if giftCardOrderType(quote.Product.ProductType) == "mobile_topup" {
		var valid bool
		recipientPhone, valid = normalizeBrazilMobileTopupPhone(req.RecipientPhone)
		if !valid {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "telefone invalido para recarga", "code": "RECIPIENT_PHONE_INVALID"})
			return
		}
		if quote.RecipientPhone != "" && recipientPhone != quote.RecipientPhone {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "telefone diferente da cotacao", "code": "QUOTE_PHONE_MISMATCH"})
			return
		}
	}
	if fundingMethod == "internal_usdt" &&
		quote.Product.ProviderSlug == "bitrefill" &&
		!strings.EqualFold(os.Getenv("GIFT_CARD_PROVIDER_MODE"), "mock") &&
		!bitrefillLivePurchasesEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "compras Bitrefill live desabilitadas", "code": "BITREFILL_LIVE_PURCHASES_DISABLED"})
		return
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil || user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "wallet do usuario nao registrada"})
		return
	}
	wallet := strings.ToLower(strings.TrimSpace(*user.WalletAddress))
	orderID := "mgco_" + mobilePayHash(uid + ":" + idempotencyKey)[:24]

	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema gift cards indisponivel"})
		return
	}
	if err := mobileDB(s.db).ensureMobilePaySchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema payment engine indisponivel"})
		return
	}
	tx, err := s.db.SQL.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database indisponivel"})
		return
	}
	defer tx.Rollback() //nolint:errcheck

	existing, existingStatus, err := txGetGiftCardOrderByIdempotency(r, tx, uid, idempotencyKey, req.ProductID, quote.Quantity, quote.RequiredUSDTMicro, recipientPhone)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, mobileProductError("NETWORK_UNAVAILABLE", "Servico indisponivel no momento."))
		return
	}
	if existing != "" {
		_ = tx.Commit()
		writeJSON(w, http.StatusOK, map[string]any{"order_id": existing, "status": existingStatus, "idempotent": true})
		return
	}
	if (fundingMethod == "internal_usdt" || fundingMethod == "onchain_treasury_hot") && isPaymentEngineCommerceProduct(quote.Product.ProductType) {
		if err := s.txConsumeCanonicalGiftCardQuoteAndCreateIntent(r, tx, uid, idempotencyKey, orderID, wallet, quote, fundingMethod); err != nil {
			writeJSON(w, http.StatusConflict, mobileProductError("TRANSACTION_PENDING", "Operacao em processamento."))
			return
		}
	}

	provider := giftCardProviderResult{
		Status:         "awaiting_payment",
		ProviderStatus: "pending_pix_funding",
		EmailStatus:    "not_sent",
	}
	if fundingMethod == "pix" {
		provider.ProviderReference = "pix_" + mobilePayHash(orderID)[:16]
	} else if fundingMethod == "onchain_treasury_hot" {
		provider = giftCardProviderResult{Status: "awaiting_funding", ProviderStatus: "awaiting_usdt_treasury", EmailStatus: "not_sent"}
	} else {
		res, err := tx.ExecContext(r.Context(), `
UPDATE nfc_wallet_balances
SET available_usdt_micro = available_usdt_micro - $3,
    locked_usdt_micro = locked_usdt_micro + $3,
    updated_at = NOW()
WHERE lower(wallet_address) = lower($1)
  AND network = $2
  AND asset = 'USDT'
  AND available_usdt_micro >= $3`,
			wallet, "BSC", quote.RequiredUSDTMicro)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao travar saldo USDT"})
			return
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": "saldo USDT insuficiente", "code": "INSUFFICIENT_USDT"})
			return
		}
		if err := txInsertGiftCardLedgerEntry(r, tx, wallet, orderID, "gift_card_purchase_lock", -quote.RequiredUSDTMicro, quote.RequiredUSDTMicro); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao auditar reserva USDT"})
			return
		}
		provider = giftCardProviderResult{Status: "funds_locked", ProviderStatus: "ready_for_provider_purchase", EmailStatus: "not_sent"}
		if strings.EqualFold(os.Getenv("GIFT_CARD_PROVIDER_MODE"), "mock") {
			provider = s.purchaseGiftCardViaProvider(r, quote, orderID, user.Email)
		} else if quote.Product.ProviderSlug == "bitrefill" && bitrefillEnabled() {
			if bitrefillLivePurchasesEnabled() {
				provider = giftCardProviderResult{Status: "funds_locked", ProviderStatus: "queued_provider_purchase", EmailStatus: "not_sent"}
			} else {
				provider = giftCardProviderResult{Status: "funds_locked", ProviderStatus: "bitrefill_live_purchases_disabled", EmailStatus: "not_sent"}
			}
		} else {
			provider = giftCardProviderResult{
				Status:         "manual_review",
				ProviderStatus: "provider_adapter_not_configured",
				EmailStatus:    "not_sent",
				ErrorMessage:   "provider real nao configurado para " + quote.Product.ProviderSlug,
			}
		}
	}
	codeEnc, pinEnc, urlEnc := s.encryptGiftCardRedemption(provider)

	_, err = tx.ExecContext(r.Context(), `
INSERT INTO mobile_gift_card_orders
  (id, user_id, wallet_address, idempotency_key, product_id, provider_id, provider_product_id,
   quantity, amount_brl, fee_brl, usdt_rate, required_usdt_micro, status, provider_status,
   provider_reference, redemption_code, redemption_pin, redemption_url, email_status, error_message, funding_method, pix_code, pix_expires_at,
   redemption_code_enc, redemption_pin_enc, redemption_url_enc, recipient_phone)
VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)`,
		orderID, uid, wallet, idempotencyKey, quote.Product.ProductID, quote.Product.ProviderID, quote.Product.ID,
		quote.Quantity, brlMinorString(quote.AmountBRLMinor), brlMinorString(quote.FeeBRLMinor), minorString(quote.USDTRateMicro, usdtMicroScale), quote.RequiredUSDTMicro, provider.Status,
		provider.ProviderStatus, provider.ProviderReference, maskGiftCardSecret(provider.RedemptionCode), maskGiftCardSecret(provider.RedemptionPIN),
		maskGiftCardSecret(provider.RedemptionURL), provider.EmailStatus, provider.ErrorMessage, fundingMethod, giftCardPixCode(orderID, quote, fundingMethod),
		giftCardPixExpiry(fundingMethod), codeEnc, pinEnc, urlEnc, recipientPhone)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar pedido gift card"})
		return
	}
	txInsertGiftCardProviderAttempt(r, tx, orderID, quote.Product.ProviderID, "purchase", provider)
	if provider.Status == "delivered" {
		if err := txCaptureGiftCardLocked(r, tx, wallet, orderID, quote.RequiredUSDTMicro); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao capturar saldo USDT"})
			return
		}
		txInsertGiftCardDelivery(r, tx, orderID, quote.Product.DeliveryMode, codeEnc, pinEnc, urlEnc)
	} else if fundingMethod == "internal_usdt" && provider.Status == "funds_locked" {
		txInsertGiftCardOutbox(r, tx, orderID, "commerce.purchase.requested", quote, provider)
	} else if provider.Status == "failed" && fundingMethod == "internal_usdt" {
		if err := txReleaseGiftCardLocked(r, tx, wallet, orderID, quote.RequiredUSDTMicro); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao liberar saldo USDT"})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar gift card"})
		return
	}
	if provider.Status == "delivered" {
		s.sendGiftCardOrderEmailAsync(user.Email, orderID, quote, provider)
	}
	payload := giftCardOrderCreatedPayload(orderID, quote, provider, fundingMethod)
	if fundingMethod == "onchain_treasury_hot" {
		addGiftCardOnchainFundingFields(payload, s, quote)
	}
	writeJSON(w, http.StatusAccepted, payload)
}

func (s *Server) handleGiftCardFundingConfirm(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	orderID := strings.TrimSpace(r.PathValue("id"))
	var req struct {
		TxHash string `json:"tx_hash"`
	}
	if err := decodeJSON(r, &req); err != nil || orderID == "" || strings.TrimSpace(req.TxHash) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order_id e tx_hash obrigatorios"})
		return
	}
	s.handleGiftCardFundingConfirmationForOrder(w, r, uid, orderID, strings.TrimSpace(req.TxHash))
}

func (s *Server) handleGiftCardFundingConfirmationForOrder(w http.ResponseWriter, r *http.Request, uid, orderID, txHash string) {
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema gift cards indisponivel"})
		return
	}
	if err := mobileDB(s.db).ensureMobilePaySchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema payment engine indisponivel"})
		return
	}
	var wallet, fundingMethod, status string
	var requiredMic int64
	err := s.db.SQL.QueryRowContext(r.Context(), `
SELECT wallet_address, funding_method, status, required_usdt_micro
FROM mobile_gift_card_orders
WHERE id=$1 AND user_id=$2::uuid`, orderID, uid).Scan(&wallet, &fundingMethod, &status, &requiredMic)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "pedido nao encontrado"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar pedido"})
		return
	}
	if fundingMethod != "onchain_treasury_hot" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "pedido nao usa funding on-chain"})
		return
	}
	if status != "awaiting_funding" && status != "funding_seen" {
		writeJSON(w, http.StatusOK, map[string]any{"order_id": orderID, "status": status, "idempotent": true})
		return
	}
	network, tokenContract, tokenDecimals, _, treasuryAddress, err := s.mobilePayFundingSpec()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, mobileProductError("ROUTE_UNAVAILABLE", "Pagamento indisponivel no momento."))
		return
	}
	receipt, err := s.verifyMobilePayUSDTFunding(r.Context(), network, txHash, tokenContract, tokenDecimals, wallet, treasuryAddress, requiredMic)
	if err != nil {
		if pendingStatus, ok := isMobilePayFundingPending(err); ok {
			_, _ = s.db.SQL.ExecContext(r.Context(), `
UPDATE mobile_gift_card_orders
SET status=$2, provider_status='awaiting_usdt_confirmations', provider_reference=$3, updated_at=NOW()
WHERE id=$1 AND user_id=$4::uuid`, orderID, pendingStatus, strings.ToLower(txHash), uid)
			_, _ = s.db.SQL.ExecContext(r.Context(), `
UPDATE mobile_payment_intents
SET status=$2, provider_status='awaiting_usdt_confirmations', funding_tx_hash=$3, updated_at=NOW()
WHERE id=$1 AND user_id=$4::uuid`, orderID, pendingStatus, strings.ToLower(txHash), uid)
			writeJSON(w, http.StatusAccepted, map[string]any{
				"order_id": orderID, "status": pendingStatus, "provider_status": "awaiting_usdt_confirmations",
				"tx_hash": strings.ToLower(txHash), "next_step": "retry_funding_confirmation",
			})
			return
		}
		_, _ = s.db.SQL.ExecContext(r.Context(), `
UPDATE mobile_gift_card_orders
SET status='manual_review', provider_status='funding_verification_failed', error_message=$3, provider_reference=$4, updated_at=NOW()
WHERE id=$1 AND user_id=$2::uuid`, orderID, uid, err.Error(), strings.ToLower(txHash))
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "MANUAL_REVIEW", "error": "Funding em analise.", "status": "manual_review"})
		return
	}
	tx, err := s.db.SQL.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database indisponivel"})
		return
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(r.Context(), `
UPDATE mobile_gift_card_orders
SET status='funds_locked',
    provider_status='ready_for_provider_purchase',
    provider_reference=$3,
    updated_at=NOW()
WHERE id=$1 AND user_id=$2::uuid
  AND status IN ('awaiting_funding','funding_seen')`, orderID, uid, receipt.TxHash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar pedido"})
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		_ = tx.Commit()
		writeJSON(w, http.StatusOK, map[string]any{"order_id": orderID, "status": "funds_locked", "idempotent": true})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE mobile_payment_intents
SET status='funding_confirmed',
    provider_status='provider_execution_pending',
    funding_tx_hash=$3,
    funding_amount_raw=$4,
    funding_block_number=$5,
    funding_confirmations=$6,
    funding_confirmed_at=COALESCE(funding_confirmed_at, NOW()),
    updated_at=NOW()
WHERE id=$1 AND user_id=$2::uuid`, orderID, uid, receipt.TxHash, receipt.AmountRaw, int64(receipt.BlockNumber), int64(receipt.Confirmations)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar funding"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO mobile_payment_funding_transactions
  (id, payment_intent_id, user_id, tx_hash, network, asset, token_contract, token_decimals,
   from_address, to_address, amount_raw, required_amount_raw, block_number, block_hash, log_index,
   confirmations, status)
VALUES ($1,$2,$3::uuid,$4,$5,'USDT',$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'confirmed')
ON CONFLICT (tx_hash) DO NOTHING`,
		"mpfund_"+mobilePayHash(receipt.TxHash)[:24], orderID, uid, receipt.TxHash, network, tokenContract, tokenDecimals,
		receipt.From, receipt.To, receipt.AmountRaw, receipt.RequiredRaw, int64(receipt.BlockNumber), receipt.BlockHash, receipt.LogIndex, int64(receipt.Confirmations)); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "tx_hash ja usado ou erro ao registrar funding"})
		return
	}
	providerKey := "mgc-bitrefill-" + mobilePayHash(orderID)[:24]
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO mobile_payment_executions
  (id, payment_intent_id, user_id, provider, provider_idempotency_key, status, next_attempt_at, provider_status)
VALUES ($1,$2,$3::uuid,'bitrefill',$4,'pending',NOW(),'purchase_pending')
ON CONFLICT (payment_intent_id, provider) DO NOTHING`,
		"mpexec_"+mobilePayHash(orderID + ":bitrefill")[:24], orderID, uid, providerKey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao criar execucao provider"})
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO mobile_payment_ledger_entries
  (id, payment_intent_id, user_id, entry_type, asset, network, amount_micro, tx_hash, provider, metadata)
VALUES ($1,$2,$3::uuid,'funding_confirmed','USDT',$4,$5,$6,'bitrefill',
        jsonb_build_object('treasury_address',$7,'token_contract',$8,'order_table','mobile_gift_card_orders'))
ON CONFLICT (payment_intent_id, entry_type) DO NOTHING`,
		"mpledger_"+mobilePayHash(orderID + ":funding_confirmed")[:24], orderID, uid, network, requiredMic, receipt.TxHash, treasuryAddress, tokenContract); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao auditar funding"})
		return
	}
	txInsertGiftCardOutbox(r, tx, orderID, "commerce.purchase.requested", mobileGiftCardQuote{RequiredUSDTMicro: requiredMic}, giftCardProviderResult{ProviderStatus: "purchase_pending"})
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao confirmar funding"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"order_id": orderID, "status": "funds_locked", "provider_status": "provider_execution_pending",
		"funding_tx_hash": receipt.TxHash, "confirmations": receipt.Confirmations, "next_step": "provider_execution",
	})
}

func (s *Server) handleGiftCardOrder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order id obrigatorio"})
		return
	}
	order, ok := s.getGiftCardOrderPayload(w, r, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) handleGiftCardDelivery(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order id obrigatorio"})
		return
	}
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.LGPDSecret) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "criptografia de voucher indisponivel"})
		return
	}
	codec, err := privacy.New(s.cfg.LGPDSecret)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "criptografia de voucher indisponivel"})
		return
	}
	var deliveryType, codeEnc, pinEnc, urlEnc, instructions string
	var deliveredAt sql.NullTime
	err = s.db.SQL.QueryRowContext(r.Context(), `
SELECT d.redemption_type, d.redemption_code_enc, d.redemption_pin_enc, d.redemption_url_enc,
       COALESCE(pp.description, ''), d.delivered_at
FROM gift_card_deliveries d
JOIN mobile_gift_card_orders o ON o.id = d.order_id
JOIN gift_card_provider_products pp ON pp.product_id = o.product_id
WHERE d.order_id=$1 AND o.user_id=$2::uuid`, id, userIDFromCtx(r)).Scan(&deliveryType, &codeEnc, &pinEnc, &urlEnc, &instructions, &deliveredAt)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "voucher nao encontrado"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar voucher"})
		return
	}
	code, _ := codec.Decrypt(codeEnc)
	pin, _ := codec.Decrypt(pinEnc)
	urlValue, _ := codec.Decrypt(urlEnc)
	writeJSON(w, http.StatusOK, map[string]any{
		"order_id": id, "delivery_type": deliveryType, "redemption_code": code,
		"redemption_pin": pin, "redemption_url": urlValue, "instructions": instructions,
		"delivered_at": deliveredAt.Time,
	})
}

func (s *Server) handleGiftCardOrders(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema gift cards indisponivel"})
		return
	}
	rows, err := s.db.SQL.QueryContext(r.Context(), `
SELECT o.id, o.product_id, pp.brand, pp.title, pp.product_type, o.quantity, o.amount_brl::text,
       o.fee_brl::text, o.usdt_rate::text, o.required_usdt_micro, o.status, o.provider_status,
       o.provider_reference, o.redemption_code, o.redemption_pin, o.redemption_url,
       o.email_status, o.error_message, o.created_at, o.updated_at
FROM mobile_gift_card_orders o
JOIN gift_card_provider_products pp ON pp.product_id = o.product_id
WHERE o.user_id=$1::uuid
ORDER BY o.created_at DESC
LIMIT 50`, uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar pedidos"})
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		item, err := scanGiftCardOrderPayload(rows)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao montar pedido"})
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao ler pedidos"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) mobileGiftCardQuotePayload(w http.ResponseWriter, r *http.Request, productID string, quantity int, recipientPhone string) (mobileGiftCardQuote, bool) {
	return s.mobileGiftCardQuotePayloadWithUnitPrice(w, r, productID, quantity, 0, recipientPhone)
}

func (s *Server) mobileGiftCardQuotePayloadWithUnitPrice(w http.ResponseWriter, r *http.Request, productID string, quantity int, unitPriceMinor int64, recipientPhone string) (mobileGiftCardQuote, bool) {
	if quantity <= 0 {
		quantity = 1
	}
	if quantity > 10 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "quantidade maxima 10"})
		return mobileGiftCardQuote{}, false
	}
	product, err := s.getGiftCardProduct(r, productID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "gift card nao encontrado"})
		return mobileGiftCardQuote{}, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar gift card"})
		return mobileGiftCardQuote{}, false
	}
	priceMinor, priceErr := canonicalGiftCardUnitPriceMinor(product, unitPriceMinor)
	if priceErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": priceErr.Error(), "code": "INVALID_DENOMINATION"})
		return mobileGiftCardQuote{}, false
	}
	product.PriceBRLMinor = priceMinor
	if product.FaceValueMinor <= 0 {
		product.FaceValueMinor = priceMinor
	}
	normalizedPhone := ""
	if giftCardOrderType(product.ProductType) == "mobile_topup" {
		var valid bool
		normalizedPhone, valid = normalizeBrazilMobileTopupPhone(recipientPhone)
		if !valid {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "telefone invalido para recarga", "code": "RECIPIENT_PHONE_INVALID"})
			return mobileGiftCardQuote{}, false
		}
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), userIDFromCtx(r))
	if err != nil || user == nil || user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "wallet do usuario nao registrada"})
		return mobileGiftCardQuote{}, false
	}
	rateMicros := int64(0)
	if s.workers != nil && s.workers.PriceWorker != nil {
		rateMicros = decimalToMinor(fmt.Sprintf("%.8f", s.workers.PriceWorker.GetPrice("BRL")), usdtMicroScale)
	}
	if rateMicros <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "USDT/BRL indisponivel"})
		return mobileGiftCardQuote{}, false
	}
	feeBps := s.giftCardQuoteFeeBps(product)
	amountBRLMinor := product.PriceBRLMinor * int64(quantity)
	feeBRLMinor := feeMinor(amountBRLMinor, feeBps)
	totalBRLMinor := amountBRLMinor + feeBRLMinor
	requiredMic := usdtMicrosFromBRL(totalBRLMinor, rateMicros)
	bal, _ := s.db.GetNFCBalance(r.Context(), *user.WalletAddress, "BSC")
	availableMicros := int64(0)
	lockedMicros := int64(0)
	if bal != nil {
		availableMicros = bal.AvailableMicro
		lockedMicros = bal.LockedMicro
	}
	onchain := s.mobileOnchainWalletBalancesAll(r.Context(), *user.WalletAddress)
	onchainUSDTMicros := int64(onchain.bscUSDT * 1_000_000)
	quoteID := "gcq_" + strings.TrimPrefix(mobileSwapQuoteID(), "msq_")
	return mobileGiftCardQuote{
		QuoteID: quoteID, Product: product, Quantity: quantity, AmountBRLMinor: amountBRLMinor, FeeBRLMinor: feeBRLMinor,
		TotalBRLMinor: totalBRLMinor, USDTRateMicro: rateMicros, RequiredUSDTMicro: requiredMic,
		AvailableUSDTMicro: availableMicros, LockedUSDTMicro: lockedMicros, HasSufficientBalance: availableMicros >= requiredMic,
		OnchainUSDTMicro: onchainUSDTMicros,
		ExpiresAt:        time.Now().UTC().Add(90 * time.Second),
		RecipientPhone:   normalizedPhone,
	}, true
}

func (s *Server) giftCardQuoteFeeBps(product mobileGiftCardProduct) int {
	if strings.EqualFold(product.ProviderSlug, "bitrefill") && giftCardOrderType(product.ProductType) == "mobile_topup" {
		return 1000
	}
	if s.cfg != nil {
		return firstPositiveIntMobile(s.cfg.NFCFeeBps, s.cfg.M2MPixFeeBps)
	}
	return 0
}

func isPaymentEngineCommerceProduct(productType string) bool {
	switch giftCardOrderType(productType) {
	case "gift_card", "mobile_topup", "esim":
		return true
	default:
		return false
	}
}

func canonicalGiftCardUnitPriceMinor(product mobileGiftCardProduct, requestedMinor int64) (int64, error) {
	if requestedMinor < 0 {
		return 0, fmt.Errorf("valor invalido")
	}
	if len(product.Packages) > 0 {
		if requestedMinor <= 0 && len(product.Packages) == 1 {
			return product.Packages[0].ValueMinor, nil
		}
		for _, pkg := range product.Packages {
			if pkg.ValueMinor > 0 && pkg.ValueMinor == requestedMinor {
				return pkg.ValueMinor, nil
			}
		}
		return 0, fmt.Errorf("denominacao indisponivel para o produto")
	}
	if product.MinimumAmountMinor > 0 || product.MaximumAmountMinor > 0 {
		if requestedMinor <= 0 {
			return 0, fmt.Errorf("valor da recarga obrigatorio")
		}
		if product.MinimumAmountMinor > 0 && requestedMinor < product.MinimumAmountMinor {
			return 0, fmt.Errorf("valor abaixo do minimo do produto")
		}
		if product.MaximumAmountMinor > 0 && requestedMinor > product.MaximumAmountMinor {
			return 0, fmt.Errorf("valor acima do maximo do produto")
		}
		return requestedMinor, nil
	}
	if requestedMinor > 0 {
		if product.PriceBRLMinor > 0 && requestedMinor != product.PriceBRLMinor {
			return 0, fmt.Errorf("valor nao corresponde ao produto")
		}
		return requestedMinor, nil
	}
	if product.PriceBRLMinor > 0 {
		return product.PriceBRLMinor, nil
	}
	return 0, fmt.Errorf("valor do produto indisponivel")
}

func normalizeBrazilMobileTopupPhone(phone string) (string, bool) {
	raw := strings.TrimSpace(phone)
	if raw == "" {
		return "", false
	}
	for _, r := range raw {
		if (r >= '0' && r <= '9') || r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' {
			continue
		}
		return "", false
	}
	digits := onlyDigits(raw)
	if strings.HasPrefix(digits, "00") {
		digits = strings.TrimPrefix(digits, "00")
	}
	if strings.HasPrefix(digits, "55") {
		digits = strings.TrimPrefix(digits, "55")
	}
	if len(digits) != 10 && len(digits) != 11 {
		return "", false
	}
	ddd := digits[:2]
	if ddd < "11" || ddd > "99" {
		return "", false
	}
	subscriber := digits[2:]
	if len(subscriber) == 9 && subscriber[0] != '9' {
		return "", false
	}
	if len(subscriber) == 8 && subscriber[0] < '2' {
		return "", false
	}
	return "+55" + digits, true
}

func (s *Server) getGiftCardProduct(r *http.Request, productID string) (mobileGiftCardProduct, error) {
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		return mobileGiftCardProduct{}, err
	}
	product := mobileGiftCardProduct{}
	var priceText string
	err := s.db.SQL.QueryRowContext(r.Context(), `
SELECT pp.id, pp.provider_id, p.slug, p.name, pp.product_id, pp.brand, pp.title, pp.description,
       pp.category, pp.currency, pp.face_value_minor, pp.price_brl::text, pp.discount_bps,
       pp.product_type, pp.delivery_mode, pp.image_url, pp.requires_kyc,
       COALESCE(gc.id,''), COALESCE(gc.subtitle,''), COALESCE(gc.badge,''), COALESCE(gc.offer_text,''), COALESCE(gc.sort_order,100)
FROM gift_card_provider_products pp
JOIN gift_card_providers p ON p.id = pp.provider_id
LEFT JOIN mobile_gift_cards gc ON gc.provider_product_id = pp.id AND gc.active=true
WHERE pp.product_id=$1 AND pp.active=true AND p.status='active'`, strings.TrimSpace(productID)).Scan(
		&product.ID, &product.ProviderID, &product.ProviderSlug, &product.ProviderName, &product.ProductID,
		&product.Brand, &product.Title, &product.Description, &product.Category, &product.Currency,
		&product.FaceValueMinor, &priceText, &product.DiscountBps, &product.ProductType, &product.DeliveryMode,
		&product.ImageURL, &product.RequiresKYC, &product.CatalogID, &product.Subtitle, &product.Badge,
		&product.OfferText, &product.SortOrder,
	)
	product.PriceBRLMinor = decimalToMinor(priceText, brlMinorScale)
	if err == nil {
		var rawMeta string
		if metaErr := s.db.SQL.QueryRowContext(r.Context(), `
SELECT metadata::text
FROM gift_card_provider_products
WHERE product_id=$1 AND provider_id=$2`, strings.TrimSpace(productID), product.ProviderID).Scan(&rawMeta); metaErr == nil && strings.TrimSpace(rawMeta) != "" {
			var commerce commerceProduct
			if json.Unmarshal([]byte(rawMeta), &commerce) == nil {
				product.Packages = commerce.Packages
				product.MinimumAmountMinor = commerce.MinimumAmountMinor
				product.MaximumAmountMinor = commerce.MaximumAmountMinor
			}
		}
	}
	return product, err
}

func (s *Server) purchaseGiftCardViaProvider(r *http.Request, quote mobileGiftCardQuote, orderID, recipientEmail string) giftCardProviderResult {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("GIFT_CARD_PROVIDER_MODE")))
	if mode == "mock" {
		code := strings.ToUpper(quote.Product.Brand[:minInt(len(quote.Product.Brand), 3)]) + "-" + strings.ToUpper(mobilePayHash(orderID)[:12])
		return giftCardProviderResult{
			Status:            "delivered",
			ProviderStatus:    "mock_delivered",
			ProviderReference: "mock_" + mobilePayHash(orderID)[:16],
			RedemptionCode:    code,
			RedemptionURL:     "",
			EmailStatus:       "pending_email_delivery",
		}
	}
	if quote.Product.ProviderSlug == "bitrefill" && bitrefillEnabled() {
		if !bitrefillLivePurchasesEnabled() {
			return giftCardProviderResult{
				Status:         "funds_locked",
				ProviderStatus: "bitrefill_live_purchases_disabled",
				EmailStatus:    "not_sent",
			}
		}
		provider := newBitrefillProvider()
		result, err := provider.Purchase(r.Context(), commercePurchaseRequest{
			Product: commerceProduct{
				ID:                quote.Product.ProductID,
				Provider:          "bitrefill",
				ProviderProductID: strings.TrimPrefix(quote.Product.ID, "gcpp_bitrefill_"),
				Type:              "gift_card",
				Brand:             quote.Product.Brand,
				Title:             quote.Product.Title,
				CountryCode:       "BR",
				Currency:          quote.Product.Currency,
			},
			Quantity:         quote.Quantity,
			UnitPriceMinor:   quote.AmountBRLMinor / int64(maxInt(quote.Quantity, 1)),
			CustomIdentifier: orderID,
			SenderName:       "ChainFX",
			RecipientEmail:   recipientEmail,
		})
		if err != nil {
			return giftCardProviderResult{
				Status:         "failed",
				ProviderStatus: "bitrefill_purchase_failed",
				EmailStatus:    "not_sent",
				ErrorMessage:   err.Error(),
			}
		}
		if result.RedemptionCode != "" || result.RedemptionPIN != "" || result.RedemptionURL != "" {
			result.Status = "delivered"
		}
		return giftCardProviderResult{
			Status:            firstNonEmptyStr(result.Status, "purchasing"),
			ProviderStatus:    firstNonEmptyStr(result.ProviderStatus, "bitrefill_processing"),
			ProviderReference: result.ProviderReference,
			RedemptionCode:    result.RedemptionCode,
			RedemptionPIN:     result.RedemptionPIN,
			RedemptionURL:     result.RedemptionURL,
			EmailStatus:       "pending_email_delivery",
			ErrorMessage:      result.ErrorMessage,
		}
	}
	return giftCardProviderResult{
		Status:         "manual_review",
		ProviderStatus: "provider_adapter_not_configured",
		EmailStatus:    "not_sent",
		ErrorMessage:   "provider real nao configurado para " + quote.Product.ProviderSlug,
	}
}

func giftCardProductPayload(product mobileGiftCardProduct) map[string]any {
	productKind := giftCardProductKind(product.ProductType)
	return map[string]any{
		"id": product.CatalogID, "provider_product_id": product.ID, "product_id": product.ProductID,
		"provider": product.ProviderSlug, "brand": product.Brand, "title": product.Title,
		"subtitle": product.Subtitle, "description": product.Description, "category": product.Category,
		"currency": product.Currency, "face_value_minor": product.FaceValueMinor, "price_brl": brlMinorString(product.PriceBRLMinor),
		"discount_bps": product.DiscountBps, "product_type": productKind, "product_kind": productKind, "order_type": giftCardOrderType(productKind),
		"catalog_id": product.CatalogID, "provider_product_row_id": product.ID, "delivery_mode": product.DeliveryMode,
		"image_url": product.ImageURL, "badge": product.Badge, "offer_text": product.OfferText,
		"requires_kyc": product.RequiresKYC,
	}
}

func giftCardQuotePayload(quote mobileGiftCardQuote, fundingMethod string) map[string]any {
	productKind := giftCardProductKind(quote.Product.ProductType)
	availableMicros := quote.AvailableUSDTMicro
	sufficient := quote.HasSufficientBalance
	if fundingMethod == "onchain_treasury_hot" {
		availableMicros = quote.OnchainUSDTMicro
		sufficient = quote.OnchainUSDTMicro >= quote.RequiredUSDTMicro
	}
	return map[string]any{
		"quote_id": quote.QuoteID, "product": giftCardProductPayload(quote.Product), "quantity": quote.Quantity,
		"quote_type": "commerce", "order_type": giftCardOrderType(productKind), "product_type": productKind, "product_kind": productKind, "provider": quote.Product.ProviderSlug,
		"amount_brl": brlMinorString(quote.AmountBRLMinor), "fee_brl": brlMinorString(quote.FeeBRLMinor), "total_brl": brlMinorString(quote.TotalBRLMinor),
		"usdt_rate": minorString(quote.USDTRateMicro, usdtMicroScale), "total_usdt": usdtMicroString(quote.RequiredUSDTMicro), "required_usdt": usdtMicroString(quote.RequiredUSDTMicro),
		"available_usdt": usdtMicroString(availableMicros), "locked_usdt": usdtMicroString(quote.LockedUSDTMicro),
		"funding_method": fundingMethod, "funding_asset": giftCardFundingAsset(fundingMethod), "funding_source": giftCardFundingSource(fundingMethod),
		"has_sufficient_balance": sufficient, "expires_at": quote.ExpiresAt,
		"payment_methods": []map[string]any{
			{"key": "onchain_treasury_hot", "label": "USDT wallet", "detail": "Envia USDT para a treasury ChainFX", "asset": "USDT", "recommended": true},
			{"key": "internal_usdt", "label": "Saldo USDT interno", "detail": "Ledger interno ChainFX legado", "asset": "USDT", "recommended": false},
			{"key": "pix", "label": "PIX", "detail": "Depositar em BRL", "asset": "BRL", "recommended": false},
		},
	}
}

func addGiftCardOnchainFundingFields(payload map[string]any, s *Server, quote mobileGiftCardQuote) {
	if payload == nil || s == nil {
		return
	}
	network, tokenContract, tokenDecimals, chainID, treasuryAddress, err := s.mobilePayFundingSpec()
	if err != nil {
		payload["funding_error"] = "treasury_route_unavailable"
		return
	}
	payload["funding_asset"] = "USDT"
	payload["funding_network"] = network
	payload["funding_source"] = "onchain_treasury_hot"
	payload["treasury_address"] = treasuryAddress
	payload["token_contract"] = tokenContract
	payload["token_decimals"] = tokenDecimals
	payload["chain_id"] = chainID
	payload["required_usdt"] = usdtMicroString(quote.RequiredUSDTMicro)
	payload["next_step"] = "send_usdt_to_treasury"
	payload["gas_payer"] = "chainfx_required"
}

func giftCardOrderCreatedPayload(orderID string, quote mobileGiftCardQuote, provider giftCardProviderResult, fundingMethod string) map[string]any {
	productKind := giftCardProductKind(quote.Product.ProductType)
	return map[string]any{
		"id": orderID, "order_id": orderID, "order_type": giftCardOrderType(productKind), "product_type": productKind, "product_kind": productKind,
		"provider": quote.Product.ProviderSlug, "status": provider.Status, "provider_status": provider.ProviderStatus,
		"provider_reference": provider.ProviderReference,
		"email_status":       provider.EmailStatus, "error_message": provider.ErrorMessage,
		"product": giftCardProductPayload(quote.Product), "quantity": quote.Quantity,
		"amount_brl": brlMinorString(quote.AmountBRLMinor), "fee_brl": brlMinorString(quote.FeeBRLMinor), "usdt_rate": minorString(quote.USDTRateMicro, usdtMicroScale),
		"required_usdt":  usdtMicroString(quote.RequiredUSDTMicro),
		"funding_method": fundingMethod, "funding_asset": giftCardFundingAsset(fundingMethod), "funding_source": giftCardFundingSource(fundingMethod),
		"pix_code": giftCardPixCode(orderID, quote, fundingMethod), "pix_expires_at": giftCardPixExpiry(fundingMethod),
	}
}

func txGetGiftCardOrderByIdempotency(r *http.Request, tx *sql.Tx, userID, key, productID string, quantity int, requiredMic int64, recipientPhone string) (id, status string, err error) {
	var existingProductID, existingRecipientPhone string
	var existingQuantity int
	var existingRequiredMic int64
	err = tx.QueryRowContext(r.Context(), `
SELECT id, status, product_id, quantity, required_usdt_micro, COALESCE(recipient_phone, '')
FROM mobile_gift_card_orders
WHERE user_id=$1::uuid AND idempotency_key=$2
FOR UPDATE`, userID, key).Scan(&id, &status, &existingProductID, &existingQuantity, &existingRequiredMic, &existingRecipientPhone)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err == nil && id != "" {
		if existingProductID != productID || existingQuantity != quantity || existingRequiredMic != requiredMic || existingRecipientPhone != recipientPhone {
			return "", "", fmt.Errorf("idempotency key reutilizada com payload diferente")
		}
	}
	return id, status, err
}

func (s *Server) txConsumeCanonicalGiftCardQuoteAndCreateIntent(r *http.Request, tx *sql.Tx, userID, idempotencyKey, paymentID, wallet string, quote mobileGiftCardQuote, fundingMethod string) error {
	if strings.TrimSpace(quote.QuoteID) == "" {
		return fmt.Errorf("quote_id obrigatorio")
	}
	network, tokenContract, tokenDecimals, _, treasuryAddress, err := s.mobilePayFundingSpec()
	if err != nil {
		return fmt.Errorf("rota treasury indisponivel")
	}
	var consumedAt sql.NullTime
	var productID, provider, providerProductID, quotedPhone string
	var quantity int
	var requiredMic int64
	var expiresAt time.Time
	err = tx.QueryRowContext(r.Context(), `
SELECT product_id, provider, provider_product_id, quantity, required_usdt_micro, expires_at, consumed_at, COALESCE(recipient_phone, '')
FROM mobile_payment_quotes
WHERE quote_id=$1 AND user_id=$2::uuid
FOR UPDATE`, quote.QuoteID, userID).Scan(&productID, &provider, &providerProductID, &quantity, &requiredMic, &expiresAt, &consumedAt, &quotedPhone)
	if err == sql.ErrNoRows {
		return fmt.Errorf("quote_id invalido ou nao pertence ao usuario")
	}
	if err != nil {
		return fmt.Errorf("erro ao buscar quote canonico")
	}
	if consumedAt.Valid {
		return fmt.Errorf("quote_id ja consumido")
	}
	if time.Now().UTC().After(expiresAt.UTC()) {
		_, _ = tx.ExecContext(r.Context(), `
UPDATE mobile_payment_quotes
SET status='expired', updated_at=NOW()
WHERE quote_id=$1 AND user_id=$2::uuid AND consumed_at IS NULL`, quote.QuoteID, userID)
		return fmt.Errorf("quote expirado")
	}
	if productID != quote.Product.ProductID || provider != quote.Product.ProviderSlug ||
		providerProductID != quote.Product.ID || quantity != quote.Quantity || requiredMic != quote.RequiredUSDTMicro {
		return fmt.Errorf("quote nao corresponde ao produto confirmado")
	}
	if quotedPhone != "" && quotedPhone != quote.RecipientPhone {
		return fmt.Errorf("telefone nao corresponde ao quote confirmado")
	}
	metadata, _ := json.Marshal(map[string]any{
		"order_id":            paymentID,
		"brand":               quote.Product.Brand,
		"title":               quote.Product.Title,
		"provider_product_id": quote.Product.ID,
		"recipient_phone":     quote.RecipientPhone,
		"legacy_order_table":  "mobile_gift_card_orders",
		"reservation_backend": giftCardFundingSource(fundingMethod),
		"funding_method":      fundingMethod,
	})
	status := "reserved"
	providerStatus := "funds_reserved"
	if fundingMethod == "onchain_treasury_hot" {
		status = "awaiting_funding"
		providerStatus = "awaiting_usdt_treasury"
	}
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO mobile_payment_intents
  (id, user_id, wallet_address, idempotency_key, quote_id, raw_code, payment_type,
   beneficiary_name, description, amount_brl, fee_brl, total_brl, usdt_rate,
   required_usdt_micro, status, provider, provider_status, funding_asset, funding_network,
   funding_token_contract, funding_token_decimals, treasury_address,
   quote_expires_at, product_id, provider_product_id, quantity, metadata)
VALUES ($1,$2::uuid,$3,$4,$5,'','gift_card',$6,$7,$8,$9,$10,$11,$12,
        $13,'bitrefill',$14,'USDT',$15,$16,$17,$18,$19,$20,$21,$22::jsonb)
ON CONFLICT (user_id, idempotency_key) DO NOTHING`,
		paymentID, userID, wallet, idempotencyKey, quote.QuoteID, quote.Product.Brand, quote.Product.Title,
		brlMinorString(quote.AmountBRLMinor), brlMinorString(quote.FeeBRLMinor), brlMinorString(quote.TotalBRLMinor),
		minorString(quote.USDTRateMicro, usdtMicroScale), quote.RequiredUSDTMicro, status, providerStatus,
		network, tokenContract, tokenDecimals, treasuryAddress, expiresAt,
		quote.Product.ProductID, quote.Product.ID, quote.Quantity, string(metadata)); err != nil {
		return fmt.Errorf("erro ao criar PaymentIntent gift card")
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE mobile_payment_quotes
SET status='consumed', consumed_at=NOW(), consumed_intent_id=$3, updated_at=NOW()
WHERE quote_id=$1 AND user_id=$2::uuid AND consumed_at IS NULL`, quote.QuoteID, userID, paymentID); err != nil {
		return fmt.Errorf("erro ao consumir quote canonico")
	}
	if fundingMethod == "internal_usdt" {
		providerKey := "mgc-bitrefill-" + mobilePayHash(paymentID)[:24]
		if _, err := tx.ExecContext(r.Context(), `
INSERT INTO mobile_payment_executions
  (id, payment_intent_id, user_id, provider, provider_idempotency_key, status, next_attempt_at, provider_status)
VALUES ($1,$2,$3::uuid,'bitrefill',$4,'pending',NOW(),'purchase_pending')
ON CONFLICT (payment_intent_id, provider) DO NOTHING`,
			"mpexec_"+mobilePayHash(paymentID + ":bitrefill")[:24], paymentID, userID, providerKey); err != nil {
			return fmt.Errorf("erro ao criar PaymentExecution gift card")
		}
	}
	return nil
}

func (s *Server) getGiftCardOrderPayload(w http.ResponseWriter, r *http.Request, id string) (map[string]any, bool) {
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema gift cards indisponivel"})
		return nil, false
	}
	row := s.db.SQL.QueryRowContext(r.Context(), `
SELECT o.id, o.product_id, pp.brand, pp.title, pp.product_type, o.quantity, o.amount_brl::text,
       o.fee_brl::text, o.usdt_rate::text, o.required_usdt_micro, o.status, o.provider_status,
       o.provider_reference, o.redemption_code, o.redemption_pin, o.redemption_url,
       o.email_status, o.error_message, o.created_at, o.updated_at
FROM mobile_gift_card_orders o
JOIN gift_card_provider_products pp ON pp.product_id = o.product_id
WHERE o.id=$1 AND o.user_id=$2::uuid`, id, userIDFromCtx(r))
	item, err := scanGiftCardOrderPayload(row)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "pedido nao encontrado"})
		return nil, false
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar pedido"})
		return nil, false
	}
	return item, true
}

type giftCardOrderScanner interface {
	Scan(dest ...any) error
}

func scanGiftCardOrderPayload(scanner giftCardOrderScanner) (map[string]any, error) {
	var id, productID, brand, title, productType, status, providerStatus, providerReference string
	var redemptionCode, redemptionPIN, redemptionURL, emailStatus, errorMessage string
	var quantity int
	var amountBRL, feeBRL, usdtRate string
	var requiredUSDTMicro int64
	var createdAt, updatedAt time.Time
	if err := scanner.Scan(&id, &productID, &brand, &title, &productType, &quantity, &amountBRL, &feeBRL,
		&usdtRate, &requiredUSDTMicro, &status, &providerStatus, &providerReference, &redemptionCode,
		&redemptionPIN, &redemptionURL, &emailStatus, &errorMessage, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	return map[string]any{
		"id": id, "order_id": id, "order_type": giftCardOrderType(productType), "product_id": productID,
		"brand": brand, "title": title, "product_type": giftCardProductKind(productType), "product_kind": giftCardProductKind(productType),
		"quantity": quantity, "amount_brl": amountBRL, "fee_brl": feeBRL, "usdt_rate": usdtRate,
		"required_usdt": usdtMicroString(requiredUSDTMicro),
		"status":        status, "provider_status": providerStatus, "provider_reference": providerReference,
		"redemption_code": maskGiftCardSecret(redemptionCode), "redemption_pin": maskGiftCardSecret(redemptionPIN), "redemption_url": maskGiftCardSecret(redemptionURL),
		"email_status": emailStatus, "error_message": errorMessage, "created_at": createdAt, "updated_at": updatedAt,
	}, nil
}

func giftCardProductKind(productType string) string {
	kind := strings.TrimSpace(strings.ToLower(productType))
	if kind == "" {
		return "gift_card"
	}
	return kind
}

func giftCardOrderType(productType string) string {
	switch giftCardProductKind(productType) {
	case "phone_refill", "mobile_topup", "topup":
		return "mobile_topup"
	case "esim":
		return "esim"
	case "hotel", "travel":
		return "travel"
	default:
		return "gift_card"
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Server) recordGiftCardQuote(r *http.Request, quote mobileGiftCardQuote, fundingMethod string) {
	if s == nil || s.db == nil {
		return
	}
	_ = mobileDB(s.db).ensureMobilePaySchema(r.Context())
	metadata, _ := json.Marshal(map[string]any{
		"brand":            quote.Product.Brand,
		"title":            quote.Product.Title,
		"product_type":     giftCardProductKind(quote.Product.ProductType),
		"delivery_mode":    quote.Product.DeliveryMode,
		"funding_method":   fundingMethod,
		"face_value_minor": quote.Product.FaceValueMinor,
		"price_brl_minor":  quote.AmountBRLMinor,
		"recipient_phone":  quote.RecipientPhone,
	})
	_, _ = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO gift_card_quotes
  (id, user_id, product_id, quantity, funding_method, amount_brl, fee_brl, total_brl, usdt_rate, required_usdt_micro, expires_at)
VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (id) DO NOTHING`,
		quote.QuoteID, userIDFromCtx(r), quote.Product.ProductID, quote.Quantity, fundingMethod,
		brlMinorString(quote.AmountBRLMinor), brlMinorString(quote.FeeBRLMinor), brlMinorString(quote.TotalBRLMinor), minorString(quote.USDTRateMicro, usdtMicroScale), quote.RequiredUSDTMicro, quote.ExpiresAt)
	_, _ = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO mobile_payment_quotes
  (quote_id, user_id, wallet_address, parsed_payment_id, raw_code_hash, payment_type,
   beneficiary_name, description, amount_brl, fee_brl, total_brl, usdt_rate,
   required_usdt_micro, funding_asset, funding_network, product_id, provider,
   provider_product_id, quantity, recipient_phone, metadata, expires_at)
VALUES ($1,$2::uuid,'',$3,$4,'gift_card',$5,$6,$7,$8,$9,$10,$11,'USDT','BSC',$12,$13,$14,$15,$16,$17::jsonb,$18)
ON CONFLICT (quote_id) DO NOTHING`,
		quote.QuoteID, userIDFromCtx(r), quote.Product.ProductID, mobilePayHash(quote.Product.ID+":"+quote.QuoteID),
		quote.Product.Brand, quote.Product.Title, brlMinorString(quote.AmountBRLMinor), brlMinorString(quote.FeeBRLMinor),
		brlMinorString(quote.TotalBRLMinor), minorString(quote.USDTRateMicro, usdtMicroScale), quote.RequiredUSDTMicro,
		quote.Product.ProductID, quote.Product.ProviderSlug, quote.Product.ID, quote.Quantity, quote.RecipientPhone, string(metadata), quote.ExpiresAt)
}

func txInsertGiftCardLedgerEntry(r *http.Request, tx *sql.Tx, wallet, orderID, source string, availableDelta, lockedDelta int64) error {
	_, err := tx.ExecContext(r.Context(), `
INSERT INTO mobile_wallet_ledger_entries
  (id, wallet_address, network, asset, source, reference_id, available_delta_micro, locked_delta_micro)
VALUES ($1,$2,'BSC','USDT',$3,$4,$5,$6)`,
		"mwle_"+mobilePayHash(orderID + ":" + source)[:24], wallet, source, orderID, availableDelta, lockedDelta)
	return err
}

func txInsertGiftCardProviderAttempt(r *http.Request, tx *sql.Tx, orderID, providerID, action string, result giftCardProviderResult) {
	_, _ = tx.ExecContext(r.Context(), `
INSERT INTO gift_card_provider_attempts
  (id, order_id, provider_id, action, status, provider_reference, error_message)
VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		"gcpa_"+mobilePayHash(orderID + ":" + action + ":" + result.ProviderStatus)[:24],
		orderID, providerID, action, result.ProviderStatus, result.ProviderReference, result.ErrorMessage)
}

func txInsertGiftCardOutbox(r *http.Request, tx *sql.Tx, orderID, eventType string, quote mobileGiftCardQuote, result giftCardProviderResult) {
	payload, _ := json.Marshal(map[string]any{
		"order_id":            orderID,
		"quote_id":            quote.QuoteID,
		"product_id":          quote.Product.ProductID,
		"provider":            quote.Product.ProviderSlug,
		"provider_product_id": quote.Product.ID,
		"quantity":            quote.Quantity,
		"required_usdt_micro": quote.RequiredUSDTMicro,
		"provider_status":     result.ProviderStatus,
	})
	_, _ = tx.ExecContext(r.Context(), `
INSERT INTO commerce_outbox_events
  (id, event_type, aggregate_id, provider, payload)
VALUES ($1,$2,$3,$4,$5::jsonb)
ON CONFLICT (id) DO NOTHING`,
		"coev_"+mobilePayHash(orderID + ":" + eventType)[:24], eventType, orderID, quote.Product.ProviderSlug, string(payload))
}

func txInsertGiftCardDelivery(r *http.Request, tx *sql.Tx, orderID, deliveryMode, codeEnc, pinEnc, urlEnc string) {
	redemptionType := strings.TrimSpace(deliveryMode)
	if redemptionType == "" {
		redemptionType = "code"
	}
	_, _ = tx.ExecContext(r.Context(), `
INSERT INTO gift_card_deliveries
  (id, order_id, redemption_type, redemption_code_enc, redemption_pin_enc, redemption_url_enc, delivered_at)
VALUES ($1,$2,$3,$4,$5,$6,NOW())
ON CONFLICT (id) DO NOTHING`,
		"gcd_"+mobilePayHash(orderID + ":delivery")[:24], orderID, redemptionType, codeEnc, pinEnc, urlEnc)
}

func txCaptureGiftCardLocked(r *http.Request, tx *sql.Tx, wallet, orderID string, requiredMic int64) error {
	res, err := tx.ExecContext(r.Context(), `
UPDATE nfc_wallet_balances
SET locked_usdt_micro = locked_usdt_micro - $3,
    updated_at = NOW()
WHERE lower(wallet_address) = lower($1)
  AND network = $2
  AND asset = 'USDT'
  AND locked_usdt_micro >= $3`,
		wallet, "BSC", requiredMic)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("locked USDT insuficiente para capturar gift card")
	}
	if err := txInsertGiftCardLedgerEntry(r, tx, wallet, orderID, "gift_card_purchase_capture", 0, -requiredMic); err != nil {
		return err
	}
	_, _ = tx.ExecContext(r.Context(), `
UPDATE mobile_gift_card_orders
SET captured_at=NOW(), updated_at=NOW()
WHERE id=$1`, orderID)
	return nil
}

func txReleaseGiftCardLocked(r *http.Request, tx *sql.Tx, wallet, orderID string, requiredMic int64) error {
	res, err := tx.ExecContext(r.Context(), `
UPDATE nfc_wallet_balances
SET available_usdt_micro = available_usdt_micro + $3,
    locked_usdt_micro = locked_usdt_micro - $3,
    updated_at = NOW()
WHERE lower(wallet_address) = lower($1)
  AND network = $2
  AND asset = 'USDT'
  AND locked_usdt_micro >= $3`,
		wallet, "BSC", requiredMic)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("locked USDT insuficiente para liberar gift card")
	}
	if err := txInsertGiftCardLedgerEntry(r, tx, wallet, orderID, "gift_card_purchase_release", requiredMic, -requiredMic); err != nil {
		return err
	}
	_, _ = tx.ExecContext(r.Context(), `
UPDATE mobile_gift_card_orders
SET refunded_at=NOW(), updated_at=NOW()
WHERE id=$1`, orderID)
	return nil
}

func txCreditGiftCardProviderRefund(r *http.Request, tx *sql.Tx, wallet, orderID string, requiredMic int64) error {
	var inserted int
	if err := tx.QueryRowContext(r.Context(), `
INSERT INTO mobile_wallet_ledger_entries
  (id, wallet_address, network, asset, source, reference_id, available_delta_micro, locked_delta_micro)
VALUES ($1,$2,'BSC','USDT','gift_card_provider_refund',$3,$4,0)
ON CONFLICT (id) DO NOTHING
RETURNING 1`,
		"mwle_"+mobilePayHash(orderID + ":gift_card_provider_refund")[:24], wallet, orderID, requiredMic).Scan(&inserted); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if inserted != 1 {
		return nil
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE nfc_wallet_balances
SET available_usdt_micro = available_usdt_micro + $3,
    updated_at = NOW()
WHERE lower(wallet_address) = lower($1)
  AND network = $2
  AND asset = 'USDT'`, wallet, "BSC", requiredMic); err != nil {
		return err
	}
	_, err := tx.ExecContext(r.Context(), `
UPDATE mobile_gift_card_orders
SET refunded_at=COALESCE(refunded_at, NOW()), updated_at=NOW()
WHERE id=$1`, orderID)
	return err
}

func normalizeGiftCardFundingMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pix":
		return "pix"
	case "onchain", "onchain_treasury", "onchain_treasury_hot", "wallet_usdt", "evm_usdt":
		return "onchain_treasury_hot"
	default:
		return "internal_usdt"
	}
}

func (s *Server) encryptGiftCardRedemption(provider giftCardProviderResult) (codeEnc, pinEnc, urlEnc string) {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.LGPDSecret) == "" {
		return "", "", ""
	}
	codec, err := privacy.New(s.cfg.LGPDSecret)
	if err != nil {
		return "", "", ""
	}
	codeEnc, _ = codec.Encrypt(provider.RedemptionCode)
	pinEnc, _ = codec.Encrypt(provider.RedemptionPIN)
	urlEnc, _ = codec.Encrypt(provider.RedemptionURL)
	return codeEnc, pinEnc, urlEnc
}

func maskGiftCardSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 6 {
		return "***"
	}
	return value[:3] + strings.Repeat("*", len(value)-6) + value[len(value)-3:]
}

func giftCardFundingAsset(method string) string {
	if method == "pix" {
		return "BRL"
	}
	return "USDT"
}

func giftCardFundingSource(method string) string {
	if method == "pix" {
		return "pix_deposit"
	}
	if method == "onchain_treasury_hot" {
		return "onchain_treasury_hot"
	}
	return "mobile_internal_usdt_ledger"
}

func giftCardPixCode(orderID string, quote mobileGiftCardQuote, method string) string {
	if method != "pix" {
		return ""
	}
	return "CHAINFX-GIFTCARD-PIX-" + strings.ToUpper(mobilePayHash(orderID + brlMinorString(quote.TotalBRLMinor))[:24])
}

func giftCardPixExpiry(method string) any {
	if method != "pix" {
		return nil
	}
	return time.Now().UTC().Add(30 * time.Minute)
}

func (s *Server) sendGiftCardOrderEmailAsync(to, orderID string, quote mobileGiftCardQuote, provider giftCardProviderResult) {
	if strings.TrimSpace(to) == "" || s == nil || s.cfg == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		mailer := email.NewService(s.cfg)
		status := "sent"
		if !mailer.Enabled() {
			status = "smtp_not_configured"
		} else {
			if err := mailer.SendTransaction(to, giftCardEmailSubject(quote.Product.ProductType), email.TransactionReceipt{
				Title:   giftCardEmailTitle(quote.Product.ProductType),
				Intro:   "Seu pedido foi concluido e os dados de resgate estao abaixo.",
				CTA:     "Abrir ChainFX",
				Details: giftCardEmailDetails(orderID, quote.Product.Title, quote.Product.ProductType, quote.TotalBRLMinor, quote.RequiredUSDTMicro, provider.Status, provider.RedemptionCode, provider.RedemptionPIN, provider.RedemptionURL),
			}); err != nil {
				status = "send_failed"
			}
		}
		_, _ = s.db.SQL.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET email_status=$2, updated_at=NOW()
WHERE id=$1`, orderID, status)
	}()
}

func giftCardEmailSubject(productType string) string {
	if giftCardOrderType(productType) == "mobile_topup" {
		return "Recarga concluida na ChainFX"
	}
	return "Gift card entregue na ChainFX"
}

func giftCardEmailTitle(productType string) string {
	if giftCardOrderType(productType) == "mobile_topup" {
		return "Recarga concluida"
	}
	return "Gift card entregue"
}

func giftCardEmailDetails(orderID, title, productType string, totalBRLMinor, requiredUSDTMicro int64, status, code, pin, url string) []email.TransactionDetail {
	productLabel := "Produto"
	if giftCardOrderType(productType) == "mobile_topup" {
		productLabel = "Recarga"
	}
	details := []email.TransactionDetail{
		{Label: productLabel, Value: title},
		{Label: "Status", Value: firstNonEmptyStr(status, "delivered")},
		{Label: "Valor", Value: "R$ " + brlMinorString(totalBRLMinor)},
		{Label: "USDT debitado", Value: usdtMicroString(requiredUSDTMicro) + " USDT"},
		{Label: "Ordem", Value: orderID, CopyHint: true},
		{Label: "Concluido em", Value: time.Now().Format("02/01/2006 15:04 MST")},
	}
	if strings.TrimSpace(code) != "" {
		details = append(details, email.TransactionDetail{Label: "Codigo", Value: code, CopyHint: true})
	}
	if strings.TrimSpace(pin) != "" {
		details = append(details, email.TransactionDetail{Label: "PIN", Value: pin, CopyHint: true})
	}
	if strings.TrimSpace(url) != "" {
		details = append(details, email.TransactionDetail{Label: "Link", Value: url, CopyHint: true})
	}
	return details
}
