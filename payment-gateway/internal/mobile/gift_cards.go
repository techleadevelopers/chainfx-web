package mobile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/email"
	"payment-gateway/internal/privacy"
)

type mobileGiftCardProduct struct {
	ID             string
	ProviderID     string
	ProviderSlug   string
	ProviderName   string
	ProductID      string
	Brand          string
	Title          string
	Description    string
	Category       string
	Currency       string
	FaceValueMinor int64
	PriceBRLMinor  int64
	DiscountBps    int
	ProductType    string
	DeliveryMode   string
	ImageURL       string
	RequiresKYC    bool
	CatalogID      string
	Subtitle       string
	Badge          string
	OfferText      string
	SortOrder      int
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
	HasSufficientBalance bool
	ExpiresAt            time.Time
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
		ProductID     string `json:"product_id"`
		Quantity      int    `json:"quantity"`
		FundingMethod string `json:"funding_method"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ProductID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product_id obrigatorio"})
		return
	}
	quote, ok := s.mobileGiftCardQuotePayload(w, r, req.ProductID, req.Quantity)
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
		FundingMethod  string `json:"funding_method"`
		RecipientPhone string `json:"recipient_phone"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.ProductID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product_id obrigatorio"})
		return
	}
	quote, ok := s.mobileGiftCardQuotePayload(w, r, req.ProductID, req.Quantity)
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
	if quote.Product.ProductType == "phone_refill" && recipientPhone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "telefone obrigatorio para recarga", "code": "RECIPIENT_PHONE_REQUIRED"})
		return
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
	tx, err := s.db.SQL.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "database indisponivel"})
		return
	}
	defer tx.Rollback() //nolint:errcheck

	existing, existingStatus, err := txGetGiftCardOrderByIdempotency(r, tx, uid, idempotencyKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if existing != "" {
		_ = tx.Commit()
		writeJSON(w, http.StatusOK, map[string]any{"order_id": existing, "status": existingStatus, "idempotent": true})
		return
	}

	provider := giftCardProviderResult{
		Status:         "awaiting_payment",
		ProviderStatus: "pending_pix_funding",
		EmailStatus:    "not_sent",
	}
	if fundingMethod == "pix" {
		provider.ProviderReference = "pix_" + mobilePayHash(orderID)[:16]
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
		txInsertGiftCardLedgerEntry(r, tx, wallet, orderID, "gift_card_purchase_lock", -quote.RequiredUSDTMicro, quote.RequiredUSDTMicro)
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
	writeJSON(w, http.StatusAccepted, giftCardOrderCreatedPayload(orderID, quote, provider, fundingMethod))
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

func (s *Server) mobileGiftCardQuotePayload(w http.ResponseWriter, r *http.Request, productID string, quantity int) (mobileGiftCardQuote, bool) {
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
	feeBps := 0
	if s.cfg != nil {
		feeBps = firstPositiveIntMobile(s.cfg.NFCFeeBps, s.cfg.M2MPixFeeBps)
	}
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
	quoteID := "gcq_" + mobilePayHash(product.ProductID + ":" + strconv.Itoa(quantity) + ":" + brlMinorString(amountBRLMinor) + ":" + minorString(rateMicros, usdtMicroScale))[:24]
	return mobileGiftCardQuote{
		QuoteID: quoteID, Product: product, Quantity: quantity, AmountBRLMinor: amountBRLMinor, FeeBRLMinor: feeBRLMinor,
		TotalBRLMinor: totalBRLMinor, USDTRateMicro: rateMicros, RequiredUSDTMicro: requiredMic,
		AvailableUSDTMicro: availableMicros, LockedUSDTMicro: lockedMicros, HasSufficientBalance: availableMicros >= requiredMic,
		ExpiresAt: time.Now().UTC().Add(90 * time.Second),
	}, true
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
	return map[string]any{
		"id": product.CatalogID, "provider_product_id": product.ID, "product_id": product.ProductID,
		"provider": product.ProviderSlug, "brand": product.Brand, "title": product.Title,
		"subtitle": product.Subtitle, "description": product.Description, "category": product.Category,
		"currency": product.Currency, "face_value_minor": product.FaceValueMinor, "price_brl": brlMinorString(product.PriceBRLMinor),
		"discount_bps": product.DiscountBps, "product_type": product.ProductType, "delivery_mode": product.DeliveryMode,
		"image_url": product.ImageURL, "badge": product.Badge, "offer_text": product.OfferText,
		"requires_kyc": product.RequiresKYC,
	}
}

func giftCardQuotePayload(quote mobileGiftCardQuote, fundingMethod string) map[string]any {
	return map[string]any{
		"quote_id": quote.QuoteID, "product": giftCardProductPayload(quote.Product), "quantity": quote.Quantity,
		"amount_brl": brlMinorString(quote.AmountBRLMinor), "fee_brl": brlMinorString(quote.FeeBRLMinor), "total_brl": brlMinorString(quote.TotalBRLMinor),
		"usdt_rate": minorString(quote.USDTRateMicro, usdtMicroScale), "total_usdt": usdtMicroString(quote.RequiredUSDTMicro), "required_usdt": usdtMicroString(quote.RequiredUSDTMicro),
		"available_usdt": usdtMicroString(quote.AvailableUSDTMicro), "locked_usdt": usdtMicroString(quote.LockedUSDTMicro),
		"funding_method": fundingMethod, "funding_asset": giftCardFundingAsset(fundingMethod), "funding_source": giftCardFundingSource(fundingMethod),
		"has_sufficient_balance": quote.HasSufficientBalance, "expires_at": quote.ExpiresAt,
		"payment_methods": []map[string]any{
			{"key": "internal_usdt", "label": "Saldo USDT", "detail": "Carteira interna ChainFX", "asset": "USDT", "recommended": true},
			{"key": "pix", "label": "PIX", "detail": "Depositar em BRL", "asset": "BRL", "recommended": false},
		},
	}
}

func giftCardOrderCreatedPayload(orderID string, quote mobileGiftCardQuote, provider giftCardProviderResult, fundingMethod string) map[string]any {
	return map[string]any{
		"order_id": orderID, "status": provider.Status, "provider_status": provider.ProviderStatus,
		"provider_reference": provider.ProviderReference,
		"email_status":       provider.EmailStatus, "error_message": provider.ErrorMessage,
		"product": giftCardProductPayload(quote.Product), "quantity": quote.Quantity,
		"amount_brl": brlMinorString(quote.AmountBRLMinor), "fee_brl": brlMinorString(quote.FeeBRLMinor), "usdt_rate": minorString(quote.USDTRateMicro, usdtMicroScale),
		"required_usdt":  usdtMicroString(quote.RequiredUSDTMicro),
		"funding_method": fundingMethod, "funding_asset": giftCardFundingAsset(fundingMethod), "funding_source": giftCardFundingSource(fundingMethod),
		"pix_code": giftCardPixCode(orderID, quote, fundingMethod), "pix_expires_at": giftCardPixExpiry(fundingMethod),
	}
}

func txGetGiftCardOrderByIdempotency(r *http.Request, tx *sql.Tx, userID, key string) (id, status string, err error) {
	err = tx.QueryRowContext(r.Context(), `
SELECT id, status
FROM mobile_gift_card_orders
WHERE user_id=$1::uuid AND idempotency_key=$2
FOR UPDATE`, userID, key).Scan(&id, &status)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return id, status, err
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
		"order_id": id, "product_id": productID, "brand": brand, "title": title, "product_type": productType,
		"quantity": quantity, "amount_brl": amountBRL, "fee_brl": feeBRL, "usdt_rate": usdtRate,
		"required_usdt": usdtMicroString(requiredUSDTMicro),
		"status":        status, "provider_status": providerStatus, "provider_reference": providerReference,
		"redemption_code": maskGiftCardSecret(redemptionCode), "redemption_pin": maskGiftCardSecret(redemptionPIN), "redemption_url": maskGiftCardSecret(redemptionURL),
		"email_status": emailStatus, "error_message": errorMessage, "created_at": createdAt, "updated_at": updatedAt,
	}, nil
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
	_, _ = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO gift_card_quotes
  (id, user_id, product_id, quantity, funding_method, amount_brl, fee_brl, total_brl, usdt_rate, required_usdt_micro, expires_at)
VALUES ($1,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (id) DO NOTHING`,
		quote.QuoteID, userIDFromCtx(r), quote.Product.ProductID, quote.Quantity, fundingMethod,
		brlMinorString(quote.AmountBRLMinor), brlMinorString(quote.FeeBRLMinor), brlMinorString(quote.TotalBRLMinor), minorString(quote.USDTRateMicro, usdtMicroScale), quote.RequiredUSDTMicro, quote.ExpiresAt)
}

func txInsertGiftCardLedgerEntry(r *http.Request, tx *sql.Tx, wallet, orderID, source string, availableDelta, lockedDelta int64) {
	_, _ = tx.ExecContext(r.Context(), `
INSERT INTO mobile_wallet_ledger_entries
  (id, wallet_address, network, asset, source, reference_id, available_delta_micro, locked_delta_micro)
VALUES ($1,$2,'BSC','USDT',$3,$4,$5,$6)`,
		"mwle_"+mobilePayHash(orderID + ":" + source)[:24], wallet, source, orderID, availableDelta, lockedDelta)
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
	txInsertGiftCardLedgerEntry(r, tx, wallet, orderID, "gift_card_purchase_capture", 0, -requiredMic)
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
	txInsertGiftCardLedgerEntry(r, tx, wallet, orderID, "gift_card_purchase_release", requiredMic, -requiredMic)
	_, _ = tx.ExecContext(r.Context(), `
UPDATE mobile_gift_card_orders
SET refunded_at=NOW(), updated_at=NOW()
WHERE id=$1`, orderID)
	return nil
}

func normalizeGiftCardFundingMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pix":
		return "pix"
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
			body := buildGiftCardEmailBody(orderID, quote, provider)
			if err := mailer.Send(email.Message{
				To:      to,
				Subject: "ChainFX - pedido de gift card",
				Body:    body,
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

func buildGiftCardEmailBody(orderID string, quote mobileGiftCardQuote, provider giftCardProviderResult) string {
	lines := []string{
		"Pedido ChainFX Gift Card",
		"",
		"Pedido: " + orderID,
		"Produto: " + quote.Product.Title,
		"Status: " + provider.Status,
		"Valor BRL: R$ " + brlMinorString(quote.TotalBRLMinor),
		"Total debitado: " + usdtMicroString(quote.RequiredUSDTMicro) + " USDT",
	}
	if provider.RedemptionCode != "" {
		lines = append(lines, "Codigo: "+provider.RedemptionCode)
	}
	if provider.RedemptionPIN != "" {
		lines = append(lines, "PIN: "+provider.RedemptionPIN)
	}
	if provider.RedemptionURL != "" {
		lines = append(lines, "Link: "+provider.RedemptionURL)
	}
	if provider.Status == "manual_review" {
		lines = append(lines, "", "Seu pedido esta em processamento/manual review e sera entregue assim que o provider confirmar.")
	}
	return strings.Join(lines, "\n")
}
