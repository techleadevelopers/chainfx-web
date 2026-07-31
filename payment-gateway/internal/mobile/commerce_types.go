package mobile

import (
	"context"
	"strings"
)

type commerceProductFilter struct {
	Country  string
	Category string
	Search   string
	Featured bool
}

type commerceProductPackage struct {
	ID         string `json:"id"`
	ValueMinor int64  `json:"face_value_minor"`
	Value      string `json:"value"`
}

type commerceProduct struct {
	ID                  string                   `json:"id"`
	Provider            string                   `json:"provider"`
	ProviderProductID   string                   `json:"provider_product_id"`
	Type                string                   `json:"type"`
	Brand               string                   `json:"brand"`
	Title               string                   `json:"title"`
	CountryCode         string                   `json:"country_code"`
	Currency            string                   `json:"currency"`
	Categories          []string                 `json:"categories"`
	DenominationType    string                   `json:"denomination_type"`
	Denominations       []string                 `json:"denominations"`
	Packages            []commerceProductPackage `json:"packages,omitempty"`
	MinimumAmountMinor  int64                    `json:"minimum_amount_minor"`
	MaximumAmountMinor  int64                    `json:"maximum_amount_minor"`
	MinimumAmount       string                   `json:"minimum_amount,omitempty"`
	MaximumAmount       string                   `json:"maximum_amount,omitempty"`
	LogoURL             string                   `json:"logo_url"`
	RedeemInstructions  string                   `json:"redeem_instructions"`
	Available           bool                     `json:"available"`
	DiscountBps         int                      `json:"discount_bps"`
	DiscountPercentage  string                   `json:"discount_percentage"`
	Featured            bool                     `json:"featured"`
	ProviderRawCurrency string                   `json:"provider_raw_currency,omitempty"`
}

type commercePurchaseRequest struct {
	Product           commerceProduct
	Quantity          int
	UnitPriceMinor    int64
	CustomIdentifier  string
	SenderName        string
	RecipientEmail    string
	RecipientCountry  string
	RecipientPhone    string
	ProductAdditional map[string]any
}

type commercePurchaseResult struct {
	Status            string
	ProviderStatus    string
	ProviderReference string
	TransactionID     string
	RedemptionCode    string
	RedemptionPIN     string
	RedemptionURL     string
	ErrorMessage      string
	CustomIdentifier  string
}

type commerceProvider interface {
	ListProducts(ctx context.Context, filter commerceProductFilter) ([]commerceProduct, error)
	GetProduct(ctx context.Context, providerProductID string) (*commerceProduct, error)
	Purchase(ctx context.Context, request commercePurchaseRequest) (*commercePurchaseResult, error)
	GetOrder(ctx context.Context, providerOrderID string) (*commercePurchaseResult, error)
}

func commerceProductMatchesCategory(product commerceProduct, category string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	if category == "" || category == "all" || category == "tudo" {
		return true
	}
	text := strings.ToLower(product.Title + " " + product.Brand + " " + strings.Join(product.Categories, " "))
	switch category {
	case "mobile", "topups", "recarga", "recargas", "telefonia móvel", "telefonia mÃ³vel", "telefonia mÃƒÂ³vel":
		return product.Type == "phone_refill" ||
			strings.Contains(text, "refill") ||
			strings.Contains(text, "mobile") ||
			strings.Contains(text, "phone") ||
			strings.Contains(text, "minutes") ||
			strings.Contains(text, "data")
	case "food", "restaurantes":
		return strings.Contains(text, "food") ||
			strings.Contains(text, "restaurant") ||
			strings.Contains(text, "ifood") ||
			strings.Contains(text, "rappi") ||
			strings.Contains(text, "99food") ||
			strings.Contains(text, "ze delivery") ||
			strings.Contains(text, "zé delivery") ||
			strings.Contains(text, "uber eats") ||
			strings.Contains(text, "mcdonald") ||
			strings.Contains(text, "outback") ||
			strings.Contains(text, "applebee") ||
			strings.Contains(text, "abraccio") ||
			strings.Contains(text, "abbraccio") ||
			strings.Contains(text, "braz pizzaria") ||
			strings.Contains(text, "braz pizza") ||
			strings.Contains(text, "carrefour") ||
			strings.Contains(text, "evino") ||
			strings.Contains(text, "cacau brazil") ||
			strings.Contains(text, "cacau brasil") ||
			strings.Contains(text, "kopenhagen") ||
			strings.Contains(text, "santa luzia") ||
			strings.Contains(text, "coco bambu") ||
			strings.Contains(text, "assai") ||
			strings.Contains(text, "fogo no chao") ||
			strings.Contains(text, "fogo no chão") ||
			strings.Contains(text, "baccio di latte") ||
			strings.Contains(text, "ofner") ||
			strings.Contains(text, "madero")
	case "games", "jogos":
		return strings.Contains(text, "game") || strings.Contains(text, "steam") || strings.Contains(text, "playstation") || strings.Contains(text, "xbox")
	case "travel", "viagem":
		return strings.Contains(text, "travel") || strings.Contains(text, "uber") || strings.Contains(text, "hotel") || strings.Contains(text, "flight")
	case "shopping", "compras online":
		return strings.Contains(text, "shopping") || strings.Contains(text, "amazon") || strings.Contains(text, "walmart")
	default:
		return strings.Contains(text, category)
	}
}
