package mobile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBitrefillProviderUsesBearerAndMapsProducts(t *testing.T) {
	t.Setenv("BITREFILL_API_KEY", "test-api-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-api-key" {
			t.Fatalf("Authorization header = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Fatalf("missing User-Agent")
		}
		if r.URL.Path != "/products" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":         "test-gift-card-code",
					"name":       "Test Gift Card Code",
					"country":    "BR",
					"currency":   "BRL",
					"categories": []string{"giftcard", "games"},
					"in_stock":   true,
					"packages": []map[string]any{
						{"package_id": "test-gift-card-code<&>10", "value": 10},
					},
				},
			},
		})
	}))
	defer server.Close()
	t.Setenv("BITREFILL_BASE_URL", server.URL)

	products, err := newBitrefillProvider().ListProducts(context.Background(), commerceProductFilter{Country: "BR"})
	if err != nil {
		t.Fatalf("ListProducts returned error: %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("expected 1 product, got %d", len(products))
	}
	product := products[0]
	if product.Provider != "bitrefill" || product.ProviderProductID != "test-gift-card-code" {
		t.Fatalf("unexpected product: %+v", product)
	}
	if product.DenominationType != "fixed" || len(product.Packages) != 1 || product.Packages[0].ID == "" {
		t.Fatalf("packages not normalized: %+v", product)
	}
}

func TestBitrefillProviderMapsUnauthorized(t *testing.T) {
	t.Setenv("BITREFILL_API_KEY", "test-api-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Setenv("BITREFILL_BASE_URL", server.URL)

	_, err := newBitrefillProvider().ListProducts(context.Background(), commerceProductFilter{})
	providerErr, ok := err.(bitrefillProviderError)
	if !ok {
		t.Fatalf("expected bitrefillProviderError, got %T %v", err, err)
	}
	if providerErr.Code != "provider_unauthorized" || providerErr.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("unexpected error mapping: %+v", providerErr)
	}
}

func TestBitrefillProviderPurchaseRequiresLiveFlag(t *testing.T) {
	t.Setenv("BITREFILL_API_KEY", "test-api-key")
	t.Setenv("BITREFILL_LIVE_PURCHASES_ENABLED", "false")

	_, err := newBitrefillProvider().Purchase(context.Background(), commercePurchaseRequest{
		Product:        commerceProduct{ProviderProductID: "test-gift-card-code"},
		Quantity:       1,
		UnitPriceMinor: 1000,
	})
	providerErr, ok := err.(bitrefillProviderError)
	if !ok {
		t.Fatalf("expected bitrefillProviderError, got %T %v", err, err)
	}
	if providerErr.Code != "live_purchases_disabled" {
		t.Fatalf("unexpected code: %s", providerErr.Code)
	}
}

func TestBitrefillPurchaseSendsCustomIdentifier(t *testing.T) {
	t.Setenv("BITREFILL_API_KEY", "test-api-key")
	t.Setenv("BITREFILL_LIVE_PURCHASES_ENABLED", "true")
	var invoicePayload string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/products/test-gift-card-code":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"id":       "test-gift-card-code",
				"name":     "Test",
				"currency": "BRL",
				"packages": []map[string]any{{"package_id": "pkg10", "value": 10}},
			}})
		case "/invoices":
			raw, _ := io.ReadAll(r.Body)
			invoicePayload = string(raw)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"id":     "inv_1",
				"status": "payment_confirmed",
				"orders": []map[string]any{{"id": "ord_1"}},
			}})
		case "/orders/ord_1":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"id":              "ord_1",
				"status":          "delivered",
				"redemption_info": map[string]any{"code": "CODE"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("BITREFILL_BASE_URL", server.URL)

	result, err := newBitrefillProvider().Purchase(context.Background(), commercePurchaseRequest{
		Product:          commerceProduct{ProviderProductID: "test-gift-card-code"},
		Quantity:         1,
		UnitPriceMinor:   1000,
		CustomIdentifier: "giftcard:mgco_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "delivered" || result.TransactionID != "ord_1" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.Contains(invoicePayload, `"custom_identifier":"giftcard:mgco_test"`) ||
		!strings.Contains(invoicePayload, `"external_id":"giftcard:mgco_test"`) {
		t.Fatalf("custom identifier missing from payload: %s", invoicePayload)
	}
}
