package mobile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"payment-gateway/internal/config"
)

func TestMobileSwapQuoteDisabledIsRouteUnavailable(t *testing.T) {
	s := New(&config.Config{MobileSwapPancakeEnabled: false}, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/mobile/swap/quote", strings.NewReader(`{
		"from_asset":"USDT",
		"to_asset":"USDC",
		"amount":10,
		"slippage":0.005
	}`))
	rec := httptest.NewRecorder()

	s.handleSwapQuote(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["code"] != "ROUTE_UNAVAILABLE" {
		t.Fatalf("expected ROUTE_UNAVAILABLE, got %#v", body)
	}
	for _, leaked := range []string{"rpc", "database", "signer", "provider", "PancakeSwap"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), strings.ToLower(leaked)) {
			t.Fatalf("response leaks %q: %s", leaked, rec.Body.String())
		}
	}
}

func TestMobileBuyUsesSignedQuoteForRateAndFee(t *testing.T) {
	raw, err := os.ReadFile("orders.go")
	if err != nil {
		t.Fatalf("read orders.go: %v", err)
	}
	src := string(raw)
	for _, required := range []string{
		`consumeMobileTradeQuote(r.Context(), uid, req.QuoteID, "buy"`,
		`"rateLocked":    claims.Rate`,
		`"feeBRL":        claims.Fee`,
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("mobile buy authority invariant missing: %s", required)
		}
	}
	buyRequestStart := strings.Index(src, "func (s *Server) handleMobileBuy")
	if buyRequestStart < 0 {
		t.Fatal("could not locate mobile buy handler")
	}
	buyRequestEnd := strings.Index(src[buyRequestStart:], "if err := decodeJSON")
	if buyRequestStart < 0 || buyRequestEnd < 0 {
		t.Fatal("could not locate mobile buy create request shape")
	}
	requestShape := src[buyRequestStart : buyRequestStart+buyRequestEnd]
	for _, forbidden := range []string{"RateLocked", "FeeBRL", "Spread", "Provider"} {
		if strings.Contains(requestShape, forbidden) {
			t.Fatalf("mobile buy client request must not accept %s as authority", forbidden)
		}
	}
}

func TestMobileSwapExecuteRequiresRealPancakeQuoteCapability(t *testing.T) {
	raw, err := os.ReadFile("swap.go")
	if err != nil {
		t.Fatalf("read swap.go: %v", err)
	}
	src := string(raw)
	for _, required := range []string{
		"!s.cfg.MobileSwapPancakeEnabled",
		`!strings.EqualFold(claims.Provider, "pancakeswap_v2")`,
		`!strings.EqualFold(claims.Network, "BSC")`,
		`mobileProductError("ROUTE_UNAVAILABLE"`,
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("swap execute real capability invariant missing: %s", required)
		}
	}
}
