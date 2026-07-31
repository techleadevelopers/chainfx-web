package mobile

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func (s *Server) commerceProvider() commerceProvider {
	if bitrefillEnabled() {
		return newBitrefillProvider()
	}
	return nil
}

func (s *Server) handleCommerceProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"items": []map[string]any{
			{"id": "bitrefill", "name": "Bitrefill", "enabled": bitrefillEnabled(), "purchases_enabled": bitrefillLivePurchasesEnabled(), "types": []string{"gift_cards", "topups", "esim"}},
			{"id": "manual", "name": "Manual Review", "enabled": true, "types": []string{"gift_cards"}},
		},
	})
}

func (s *Server) handleCommerceBitrefillStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{
		"provider":          "bitrefill",
		"configured":        bitrefillEnabled(),
		"reachable":         false,
		"purchases_enabled": bitrefillLivePurchasesEnabled(),
		"balance_available": false,
		"last_success_at":   nil,
		"last_error_code":   nil,
	}
	if !bitrefillEnabled() {
		writeJSON(w, http.StatusOK, status)
		return
	}
	provider := newBitrefillProvider()
	if err := provider.Ping(r.Context()); err != nil {
		status["last_error_code"] = commerceProviderErrorCode(err)
		writeJSON(w, http.StatusOK, status)
		return
	}
	status["reachable"] = true
	status["last_success_at"] = time.Now().UTC()
	if balance, err := provider.Balance(r.Context()); err == nil && balance != nil {
		status["balance_available"] = true
		status["balance_currency"] = balance.Currency
		status["balance"] = balance.Balance
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleCommerceCategories(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": []map[string]any{
		{"id": "gift_cards", "label": "Gift Cards"},
		{"id": "shopping", "label": "Compras online"},
		{"id": "travel", "label": "Viagem"},
		{"id": "food", "label": "Restaurantes"},
		{"id": "games", "label": "Jogos"},
		{"id": "topups", "label": "Recarga de celular"},
		{"id": "mobile", "label": "Telefonia móvel"},
		{"id": "entertainment", "label": "Entretenimento"},
	}})
}

func (s *Server) handleCommerceProducts(w http.ResponseWriter, r *http.Request) {
	filter := commerceProductFilter{
		Country:  firstNonEmptyStr(r.URL.Query().Get("country"), "BR"),
		Category: strings.TrimSpace(r.URL.Query().Get("category")),
		Search:   strings.TrimSpace(r.URL.Query().Get("search")),
		Featured: strings.EqualFold(r.URL.Query().Get("featured"), "true"),
	}
	products, err := s.listCachedCommerceProducts(r, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao consultar catalogo local"})
		return
	}
	source := "chainfx_cache"
	if len(products) == 0 && bitrefillEnabled() && !strings.EqualFold(os.Getenv("BITREFILL_CATALOG_SYNC_ENABLED"), "false") {
		if liveProducts, err := s.fetchBitrefillCatalog(r.Context(), filter); err == nil && len(liveProducts) > 0 {
			products = liveProducts
			source = "bitrefill_live"
			req, _ := http.NewRequestWithContext(contextFromRequest(r), http.MethodGet, "http://chainfx.internal/commerce-sync", nil)
			for _, product := range liveProducts {
				_ = s.upsertCommerceProduct(req, product)
			}
		}
	} else {
		go s.syncBitrefillCatalog(contextFromRequest(r), filter)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": products, "provider": source, "country": strings.ToUpper(filter.Country), "bitrefill_configured": bitrefillEnabled()})
}

func (s *Server) handleCommerceProduct(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "product id obrigatorio"})
		return
	}
	product, err := s.getCachedCommerceProduct(r, id)
	if err == nil && product != nil {
		writeJSON(w, http.StatusOK, product)
		return
	}
	providerID := strings.TrimPrefix(id, "bitrefill_")
	provider := s.commerceProvider()
	if provider == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "produto nao encontrado"})
		return
	}
	product, err = provider.GetProduct(r.Context(), providerID)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, mobileProductError("NETWORK_UNAVAILABLE", "Produto indisponivel no momento."))
		return
	}
	_ = s.upsertCommerceProduct(r, *product)
	writeJSON(w, http.StatusOK, product)
}

func (s *Server) syncBitrefillCatalog(ctx context.Context, filter commerceProductFilter) {
	if !bitrefillEnabled() || strings.EqualFold(os.Getenv("BITREFILL_CATALOG_SYNC_ENABLED"), "false") {
		return
	}
	products, err := s.fetchBitrefillCatalog(ctx, filter)
	if err != nil {
		return
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://chainfx.internal/commerce-sync", nil)
	for _, product := range products {
		_ = s.upsertCommerceProduct(req, product)
	}
}

func (s *Server) fetchBitrefillCatalog(ctx context.Context, filter commerceProductFilter) ([]commerceProduct, error) {
	provider := s.commerceProvider()
	if provider == nil {
		return nil, bitrefillProviderError{Code: "provider_not_configured", Message: "Bitrefill credentials not configured"}
	}
	return provider.ListProducts(ctx, filter)
}

func contextFromRequest(r *http.Request) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ctx
}

func (s *Server) listCachedCommerceProducts(r *http.Request, filter commerceProductFilter) ([]commerceProduct, error) {
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		return nil, err
	}
	rows, err := s.db.SQL.QueryContext(r.Context(), `
SELECT metadata::text
FROM gift_card_provider_products
WHERE provider_id='provider_bitrefill' AND active=true
ORDER BY updated_at DESC, brand ASC
LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]commerceProduct, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var product commerceProduct
		if err := json.Unmarshal([]byte(raw), &product); err != nil || product.ID == "" {
			continue
		}
		if filter.Country != "" && product.CountryCode != "" && !strings.EqualFold(product.CountryCode, filter.Country) && product.CountryCode != "XI" {
			continue
		}
		if filter.Category != "" && !commerceProductMatchesCategory(product, filter.Category) {
			continue
		}
		if filter.Search != "" {
			text := strings.ToLower(product.Title + " " + product.Brand + " " + strings.Join(product.Categories, " "))
			if !strings.Contains(text, strings.ToLower(filter.Search)) {
				continue
			}
		}
		if filter.Featured && !product.Featured {
			continue
		}
		out = append(out, product)
	}
	return out, rows.Err()
}

func (s *Server) handleCommerceQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
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
	fundingMethod := normalizeGiftCardFundingMethod(req.FundingMethod)
	s.recordGiftCardQuote(r, quote, fundingMethod)
	writeJSON(w, http.StatusOK, giftCardQuotePayload(quote, fundingMethod))
}

func (s *Server) handleCommerceOrders(w http.ResponseWriter, r *http.Request) {
	s.handleGiftCardPurchase(w, r)
}

func (s *Server) handleCommerceOrder(w http.ResponseWriter, r *http.Request) {
	s.handleGiftCardOrder(w, r)
}

func (s *Server) handleCommerceBitrefillWebhook(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimSpace(r.URL.Query().Get("secret"))
	expected := strings.TrimSpace(envOr("BITREFILL_WEBHOOK_SECRET", ""))
	if expected != "" && secret != expected {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "webhook nao autorizado"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(http.MaxBytesReader(w, r.Body, 1<<20), 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "json invalido"})
		return
	}
	data := responseDataMap(payload)
	eventType := firstNonEmptyStr(
		strings.TrimSpace(fmt.Sprint(payload["type"])),
		strings.TrimSpace(fmt.Sprint(data["status"])),
		"invoice",
	)
	externalID := firstNonEmptyStr(
		strings.TrimSpace(fmt.Sprint(data["id"])),
		strings.TrimSpace(fmt.Sprint(payload["id"])),
	)
	eventID := "brwh_" + mobilePayHash(eventType + ":" + externalID + ":" + fmt.Sprint(data["updated_at"]))[:24]
	raw, _ := json.Marshal(payload)
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "schema commerce indisponivel"})
		return
	}
	_, _ = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO gift_card_webhook_events (id, provider, event_type, external_id, payload)
VALUES ($1, 'bitrefill', $2, $3, $4::jsonb)
ON CONFLICT (id) DO NOTHING`, eventID, eventType, externalID, string(raw))
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) upsertCommerceProduct(r *http.Request, product commerceProduct) error {
	if err := mobileDB(s.db).ensureMobileGiftCardSchema(r.Context()); err != nil {
		return err
	}
	priceMinor := commerceProductDefaultPriceMinor(product)
	meta, _ := json.Marshal(product)
	_, err := s.db.SQL.ExecContext(r.Context(), `
INSERT INTO gift_card_provider_products
  (id, provider_id, product_id, external_sku, brand, title, description, category, currency,
   face_value_minor, price_brl, discount_bps, product_type, delivery_mode, image_url, active, requires_kyc, metadata)
VALUES ($1, 'provider_bitrefill', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, true, true, $15::jsonb)
ON CONFLICT (product_id) DO UPDATE SET
  brand=EXCLUDED.brand,
  title=EXCLUDED.title,
  category=EXCLUDED.category,
  currency=EXCLUDED.currency,
  price_brl=EXCLUDED.price_brl,
  discount_bps=EXCLUDED.discount_bps,
  product_type=EXCLUDED.product_type,
  delivery_mode=EXCLUDED.delivery_mode,
  image_url=EXCLUDED.image_url,
  metadata=EXCLUDED.metadata,
  updated_at=NOW()`,
		"gcpp_bitrefill_"+product.ProviderProductID,
		product.ID,
		product.ProviderProductID,
		product.Brand,
		product.Title,
		product.RedeemInstructions,
		firstNonEmptyStr(firstCommerceCategory(product), "gift_cards"),
		firstNonEmptyStr(product.Currency, "BRL"),
		priceMinor,
		brlMinorString(priceMinor),
		product.DiscountBps,
		firstNonEmptyStr(product.Type, "gift_card"),
		commerceProductDeliveryMode(product),
		product.LogoURL,
		string(meta))
	if err != nil {
		return err
	}
	_, err = s.db.SQL.ExecContext(r.Context(), `
INSERT INTO mobile_gift_cards
  (id, provider_product_id, brand, title, subtitle, badge, offer_text, sort_order, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, 100, true)
ON CONFLICT (id) DO UPDATE SET
  brand=EXCLUDED.brand,
  title=EXCLUDED.title,
  subtitle=EXCLUDED.subtitle,
  badge=EXCLUDED.badge,
  offer_text=EXCLUDED.offer_text,
  active=true,
  updated_at=NOW()`,
		"mgc_"+product.ID,
		"gcpp_bitrefill_"+product.ProviderProductID,
		product.Brand,
		product.Title,
		firstCommerceCategory(product),
		commerceProductBadge(product),
		commerceProductOffer(product))
	return err
}

func (s *Server) getCachedCommerceProduct(r *http.Request, id string) (*commerceProduct, error) {
	var raw string
	err := s.db.SQL.QueryRowContext(r.Context(), `
SELECT metadata::text
FROM gift_card_provider_products
WHERE product_id=$1 AND provider_id='provider_bitrefill'`, id).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var product commerceProduct
	if err := json.Unmarshal([]byte(raw), &product); err != nil {
		return nil, err
	}
	return &product, nil
}

func (s *Server) updateCommerceProductPrice(r *http.Request, productID string, unitPriceMinor int64) error {
	if unitPriceMinor <= 0 {
		return nil
	}
	_, err := s.db.SQL.ExecContext(r.Context(), `
UPDATE gift_card_provider_products
SET price_brl=$2, face_value_minor=$3, updated_at=NOW()
WHERE product_id=$1`,
		productID, brlMinorString(unitPriceMinor), unitPriceMinor)
	return err
}

func commerceProductDefaultPriceMinor(product commerceProduct) int64 {
	if len(product.Denominations) > 0 && strings.TrimSpace(product.Denominations[0]) != "" {
		return decimalToMinor(product.Denominations[0], brlMinorScale)
	}
	if product.MinimumAmountMinor > 0 {
		return product.MinimumAmountMinor
	}
	return 1000
}

func firstCommerceCategory(product commerceProduct) string {
	if product.Type == "phone_refill" {
		return "topups"
	}
	if len(product.Categories) > 0 {
		return product.Categories[0]
	}
	return "gift_cards"
}

func commerceProductDeliveryMode(product commerceProduct) string {
	switch product.Type {
	case "phone_refill":
		return "topup"
	case "esim":
		return "link"
	default:
		return "code"
	}
}

func commerceProductBadge(product commerceProduct) string {
	if product.DiscountBps > 0 {
		return "Desconto"
	}
	return "Novo"
}

func commerceProductOffer(product commerceProduct) string {
	if product.DiscountBps > 0 {
		return "Até " + minorString(int64(product.DiscountBps), 100) + "% de desconto"
	}
	return brlMinorString(commerceProductDefaultPriceMinor(product)) + " " + firstNonEmptyStr(product.Currency, "BRL")
}

func initBitrefillProviderSeed(ctxReq *http.Request, db *sql.DB) {
	_, _ = db.ExecContext(ctxReq.Context(), `
INSERT INTO gift_card_providers (id, slug, name, status)
VALUES ('provider_bitrefill', 'bitrefill', 'Bitrefill', 'active')
ON CONFLICT (id) DO UPDATE SET status='active', updated_at=NOW()`)
}

func commerceProviderErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if providerErr, ok := err.(bitrefillProviderError); ok {
		return providerErr.Code
	}
	return "provider_error"
}
