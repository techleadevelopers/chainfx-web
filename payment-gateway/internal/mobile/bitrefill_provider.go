package mobile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type bitrefillProvider struct {
	client *http.Client
}

type bitrefillProviderError struct {
	Code       string
	HTTPStatus int
	Message    string
}

func (e bitrefillProviderError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

type bitrefillBalance struct {
	Currency string `json:"currency"`
	Balance  string `json:"balance"`
}

func newBitrefillProvider() *bitrefillProvider {
	timeout := time.Duration(envInt("BITREFILL_TIMEOUT_SECONDS", 10)) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &bitrefillProvider{client: &http.Client{Timeout: timeout}}
}

func bitrefillEnabled() bool {
	return strings.TrimSpace(os.Getenv("BITREFILL_API_KEY")) != "" ||
		(strings.TrimSpace(os.Getenv("BITREFILL_API_ID")) != "" && strings.TrimSpace(os.Getenv("BITREFILL_API_SECRET")) != "")
}

func bitrefillBaseURL() string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("BITREFILL_BASE_URL")), "/"); value != "" {
		return value
	}
	return "https://api.bitrefill.com/v2"
}

func bitrefillLivePurchasesEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("BITREFILL_LIVE_PURCHASES_ENABLED")), "true")
}

func (p *bitrefillProvider) authHeader() string {
	if key := strings.TrimSpace(os.Getenv("BITREFILL_API_KEY")); key != "" {
		return "Bearer " + key
	}
	id := strings.TrimSpace(os.Getenv("BITREFILL_API_ID"))
	secret := strings.TrimSpace(os.Getenv("BITREFILL_API_SECRET"))
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
}

func (p *bitrefillProvider) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, bitrefillBaseURL()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", p.authHeader())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ChainFX-Mobile-Commerce/1.0")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := p.client.Do(req)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return bitrefillProviderError{Code: "provider_timeout", Message: "provider request timeout"}
		}
		if ctx.Err() != nil {
			return bitrefillProviderError{Code: "provider_timeout", Message: "provider request cancelled"}
		}
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return bitrefillProviderError{
			Code:       bitrefillErrorCode(res.StatusCode, raw),
			HTTPStatus: res.StatusCode,
			Message:    bitrefillSafeErrorMessage(raw),
		}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (p *bitrefillProvider) Ping(ctx context.Context) error {
	var decoded map[string]any
	return p.doJSON(ctx, http.MethodGet, "/ping", nil, &decoded)
}

func (p *bitrefillProvider) Balance(ctx context.Context) (*bitrefillBalance, error) {
	var decoded map[string]any
	if err := p.doJSON(ctx, http.MethodGet, "/accounts/balance", nil, &decoded); err != nil {
		return nil, err
	}
	data := responseDataMap(decoded)
	return &bitrefillBalance{
		Currency: strings.ToUpper(strings.TrimSpace(fmt.Sprint(data["currency"]))),
		Balance:  strings.TrimSpace(fmt.Sprint(data["balance"])),
	}, nil
}

func (p *bitrefillProvider) ListProducts(ctx context.Context, filter commerceProductFilter) ([]commerceProduct, error) {
	values := url.Values{}
	values.Set("limit", "50")
	if country := strings.ToUpper(strings.TrimSpace(filter.Country)); country != "" {
		values.Set("country", country+",XI")
	} else {
		values.Set("country", "BR,XI")
	}
	if category := bitrefillCategory(filter.Category); category != "" {
		values.Set("category", category)
	}
	if productType := bitrefillProductType(filter.Category); productType != "" {
		values.Set("type", productType)
	}
	var decoded map[string]any
	path := "/products?" + values.Encode()
	if strings.TrimSpace(filter.Search) != "" {
		search := url.Values{}
		search.Set("q", strings.TrimSpace(filter.Search))
		search.Set("limit", "50")
		path = "/products/search?" + search.Encode()
	}
	if err := p.doJSON(ctx, http.MethodGet, path, nil, &decoded); err != nil {
		return nil, err
	}
	items := responseDataSlice(decoded)
	products := make([]commerceProduct, 0, len(items))
	for _, item := range items {
		product := normalizeBitrefillProduct(item)
		if product.ID == "" || !product.Available {
			continue
		}
		if filter.Category != "" && !commerceProductMatchesCategory(product, filter.Category) {
			continue
		}
		products = append(products, product)
	}
	return products, nil
}

func (p *bitrefillProvider) GetProduct(ctx context.Context, providerProductID string) (*commerceProduct, error) {
	var decoded map[string]any
	if err := p.doJSON(ctx, http.MethodGet, "/products/"+url.PathEscape(providerProductID), nil, &decoded); err != nil {
		return nil, err
	}
	data := responseDataMap(decoded)
	product := normalizeBitrefillProduct(data)
	return &product, nil
}

func (p *bitrefillProvider) Purchase(ctx context.Context, request commercePurchaseRequest) (*commercePurchaseResult, error) {
	if !bitrefillLivePurchasesEnabled() {
		return nil, bitrefillProviderError{Code: "live_purchases_disabled", Message: "compras Bitrefill live desabilitadas"}
	}
	product, err := p.GetProduct(ctx, request.Product.ProviderProductID)
	if err != nil {
		return nil, err
	}
	item := map[string]any{
		"product_id": request.Product.ProviderProductID,
		"quantity":   maxInt(request.Quantity, 1),
	}
	if pkg := chooseBitrefillPackage(*product, request.UnitPriceMinor); pkg.ID != "" {
		item["package_id"] = pkg.ID
	} else {
		item["value"] = brlMinorString(request.UnitPriceMinor)
	}
	if phone := normalizeE164Phone(request.RecipientPhone, request.RecipientCountry); phone != "" {
		item["phone_number"] = phone
	}
	payload := map[string]any{
		"products":       []map[string]any{item},
		"payment_method": "balance",
		"auto_pay":       true,
	}
	if webhook := strings.TrimSpace(os.Getenv("BITREFILL_WEBHOOK_URL")); webhook != "" {
		payload["webhook_url"] = webhook
	}
	if request.RecipientEmail != "" {
		payload["email"] = request.RecipientEmail
	}
	var invoiceResp map[string]any
	if err := p.doJSON(ctx, http.MethodPost, "/invoices", payload, &invoiceResp); err != nil {
		return nil, err
	}
	invoice := responseDataMap(invoiceResp)
	status := strings.TrimSpace(fmt.Sprint(invoice["status"]))
	orderID := firstBitrefillOrderID(invoice["orders"])
	result := &commercePurchaseResult{
		Status:            bitrefillOrderStatus(status),
		ProviderStatus:    status,
		ProviderReference: strings.TrimSpace(fmt.Sprint(invoice["id"])),
		TransactionID:     orderID,
	}
	if orderID != "" {
		order, err := p.GetOrder(ctx, orderID)
		if err == nil && order != nil {
			order.ProviderReference = result.ProviderReference
			order.TransactionID = orderID
			return order, nil
		}
	}
	return result, nil
}

func (p *bitrefillProvider) GetOrder(ctx context.Context, providerOrderID string) (*commercePurchaseResult, error) {
	var decoded map[string]any
	if err := p.doJSON(ctx, http.MethodGet, "/orders/"+url.PathEscape(providerOrderID), nil, &decoded); err != nil {
		return nil, err
	}
	order := responseDataMap(decoded)
	redemption, _ := order["redemption_info"].(map[string]any)
	status := strings.TrimSpace(fmt.Sprint(order["status"]))
	result := &commercePurchaseResult{
		Status:            bitrefillOrderStatus(status),
		ProviderStatus:    status,
		ProviderReference: providerOrderID,
		TransactionID:     providerOrderID,
		RedemptionCode:    strings.TrimSpace(fmt.Sprint(redemption["code"])),
		RedemptionPIN:     strings.TrimSpace(fmt.Sprint(redemption["pin"])),
		RedemptionURL:     strings.TrimSpace(fmt.Sprint(redemption["link"])),
	}
	if result.RedemptionCode != "" || result.RedemptionPIN != "" || result.RedemptionURL != "" {
		result.Status = "delivered"
	}
	return result, nil
}

func normalizeBitrefillProduct(item map[string]any) commerceProduct {
	id := strings.TrimSpace(fmt.Sprint(item["id"]))
	name := firstNonEmptyStr(
		strings.TrimSpace(fmt.Sprint(item["name"])),
		strings.TrimSpace(fmt.Sprint(item["base_name"])),
		strings.TrimSpace(fmt.Sprint(item["brand"])),
	)
	country := strings.ToUpper(firstNonEmptyStr(
		strings.TrimSpace(fmt.Sprint(item["country_code"])),
		strings.TrimSpace(fmt.Sprint(item["country"])),
	))
	currency := strings.ToUpper(strings.TrimSpace(fmt.Sprint(item["currency"])))
	packages := bitrefillPackages(item["packages"])
	denominations := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if pkg.ValueMinor > 0 {
			denominations = append(denominations, pkg.Value)
		}
	}
	minimumMinor, maximumMinor := bitrefillRange(item["range"])
	productType := strings.TrimSpace(fmt.Sprint(item["type"]))
	if productType == "" {
		productType = "gift_card"
	}
	productType = normalizeCommerceProductType(productType)
	available := true
	if inStock, ok := item["in_stock"].(bool); ok {
		available = inStock
	}
	product := commerceProduct{
		ID:                 "bitrefill_" + id,
		Provider:           "bitrefill",
		ProviderProductID:  id,
		Type:               productType,
		Brand:              name,
		Title:              name,
		CountryCode:        country,
		Currency:           currency,
		Categories:         bitrefillCategories(item["categories"]),
		DenominationType:   bitrefillDenominationType(packages, minimumMinor, maximumMinor),
		Denominations:      denominations,
		Packages:           packages,
		MinimumAmountMinor: minimumMinor,
		MaximumAmountMinor: maximumMinor,
		MinimumAmount:      brlMinorString(minimumMinor),
		MaximumAmount:      brlMinorString(maximumMinor),
		LogoURL:            firstNonEmptyStr(stayFirstString(item["image"]), stayFirstString(item["logo"]), "https://cdn.bitrefill.com/primg/w250h100i1/"+id+".webp"),
		RedeemInstructions: firstNonEmptyStr(strings.TrimSpace(fmt.Sprint(item["redeem_instructions"])), strings.TrimSpace(fmt.Sprint(item["description"]))),
		Available:          available,
	}
	if len(product.Categories) == 0 {
		if product.Type == "phone_refill" {
			product.Categories = []string{"topups", "mobile"}
		} else {
			product.Categories = []string{"gift_cards"}
		}
	}
	return product
}

func bitrefillPackages(value any) []commerceProductPackage {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]commerceProductPackage, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id := strings.TrimSpace(fmt.Sprint(item["package_id"]))
		if id == "" {
			id = strings.TrimSpace(fmt.Sprint(item["id"]))
		}
		valueMinor := decimalToMinor(item["value"], brlMinorScale)
		out = append(out, commerceProductPackage{ID: id, ValueMinor: valueMinor, Value: brlMinorString(valueMinor)})
	}
	return out
}

func bitrefillRange(value any) (int64, int64) {
	item, ok := value.(map[string]any)
	if !ok {
		return 0, 0
	}
	return decimalToMinor(item["min"], brlMinorScale), decimalToMinor(item["max"], brlMinorScale)
}

func bitrefillCategories(value any) []string {
	switch raw := value.(type) {
	case []any:
		out := make([]string, 0, len(raw))
		for _, entry := range raw {
			if text := strings.TrimSpace(fmt.Sprint(entry)); text != "" {
				out = append(out, strings.ToLower(text))
			}
		}
		return out
	case string:
		if raw != "" {
			return []string{strings.ToLower(raw)}
		}
	}
	return nil
}

func bitrefillDenominationType(packages []commerceProductPackage, min, max int64) string {
	if len(packages) > 0 {
		return "fixed"
	}
	if min > 0 || max > 0 {
		return "range"
	}
	return "fixed"
}

func chooseBitrefillPackage(product commerceProduct, unitPriceMinor int64) commerceProductPackage {
	for _, pkg := range product.Packages {
		if pkg.ID != "" && (unitPriceMinor <= 0 || pkg.ValueMinor == unitPriceMinor) {
			return pkg
		}
	}
	return commerceProductPackage{}
}

func bitrefillCategory(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "", "all", "tudo":
		return ""
	case "shopping", "compras online":
		return "ecommerce,retail"
	case "travel", "viagem":
		return "travel,flights"
	case "food", "restaurantes":
		return "food,restaurants,food-delivery"
	case "games", "jogos":
		return "games"
	case "topups", "topup", "recarga", "recargas", "phone_refill":
		return "refill,Mobile,phone"
	case "mobile", "telefonia móvel", "telefonia mÃ³vel":
		return "refill,Mobile,phone"
	case "entertainment", "entretenimento":
		return "entertainment,streaming,music"
	default:
		return category
	}
}

func bitrefillProductType(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "mobile", "topups", "topup", "recarga", "recargas", "phone_refill", "telefonia móvel", "telefonia mÃ³vel", "telefonia mÃƒÂ³vel":
		return "phone_refill"
	default:
		return ""
	}
}

func normalizeCommerceProductType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "phone_refill", "phone-refill", "refill", "mobile", "topup", "top_up":
		return "phone_refill"
	case "esim", "e-sim":
		return "esim"
	default:
		return "gift_card"
	}
}

func normalizeE164Phone(phone, country string) string {
	digits := onlyDigits(phone)
	if digits == "" {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(phone), "+") {
		return "+" + digits
	}
	if strings.EqualFold(strings.TrimSpace(country), "BR") || strings.TrimSpace(country) == "" {
		if strings.HasPrefix(digits, "55") {
			return "+" + digits
		}
		return "+55" + digits
	}
	return "+" + digits
}

func bitrefillOrderStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "complete", "delivered":
		return "delivered"
	case "failed", "denied", "payment_error":
		return "failed"
	case "refunded":
		return "refunded"
	case "processing", "pending", "payment_confirmed":
		return "purchasing"
	default:
		return "purchasing"
	}
}

func responseDataSlice(decoded map[string]any) []map[string]any {
	data, ok := decoded["data"].([]any)
	if !ok {
		data, _ = decoded["products"].([]any)
	}
	out := make([]map[string]any, 0, len(data))
	for _, entry := range data {
		if item, ok := entry.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out
}

func responseDataMap(decoded map[string]any) map[string]any {
	if data, ok := decoded["data"].(map[string]any); ok {
		return data
	}
	return decoded
}

func firstBitrefillOrderID(value any) string {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		return ""
	}
	item, ok := raw[0].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(item["id"]))
}

func bitrefillErrorCode(status int, raw []byte) string {
	switch status {
	case http.StatusUnauthorized:
		return "provider_unauthorized"
	case http.StatusForbidden:
		return "provider_forbidden"
	case http.StatusNotFound:
		return "product_not_found"
	case http.StatusConflict:
		return "provider_conflict"
	case http.StatusUnprocessableEntity:
		text := strings.ToLower(string(raw))
		switch {
		case strings.Contains(text, "out_of_stock"):
			return "product_out_of_stock"
		case strings.Contains(text, "balance"):
			return "provider_balance_low"
		default:
			return "provider_invalid_request"
		}
	case http.StatusTooManyRequests:
		return "provider_rate_limited"
	default:
		if status >= 500 {
			return "provider_unavailable"
		}
		return "provider_error"
	}
}

func bitrefillSafeErrorMessage(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return strings.ReplaceAll(text, "\n", " ")
}
