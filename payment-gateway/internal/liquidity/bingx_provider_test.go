package liquidity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBingXQuoteUsesSpotDepthAndAddsFees(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openApi/spot/v1/market/depth" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("symbol") != "SOL-USDT" {
			t.Fatalf("unexpected symbol %s", r.URL.Query().Get("symbol"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"asks":[["100.00","10"]],"bids":[["99.00","10"]]}}`))
	}))
	defer server.Close()

	provider := &BingXProvider{
		BaseURL:         server.URL,
		AllowedAssets:   "SOL",
		AllowedNetworks: "SOLANA",
		TakerFeeBps:     10,
		WithdrawFeeUSDT: 0.2,
		Now:             fixedBingXNow,
	}
	quote, err := provider.Quote(context.Background(), Request{
		OrderID:         "buy-sol",
		Asset:           "SOL",
		Network:         "SOLANA",
		AmountBRL:       500,
		CryptoAmount:    1,
		QuoteLockedRate: 500,
		DestAddress:     "5Q7uLxkVQ2mN4wH3rP9tB6sC1zY5aE7gF2hJ3kL4mN5A",
		TokenDecimals:   9,
	})
	if err != nil {
		t.Fatalf("Quote returned error: %v", err)
	}
	if quote.Provider != "bingx" || quote.ProviderType != "exchange" {
		t.Fatalf("unexpected provider quote: %+v", quote)
	}
	if quote.CryptoAmount != 1 || quote.TotalCostBRL <= 500 || quote.ProviderFeeBRL <= 0 || quote.NetworkFeeBRL <= 0 {
		t.Fatalf("fees/cost not applied correctly: %+v", quote)
	}
	if quote.Metadata["symbol"] != "SOL-USDT" || quote.Metadata["withdrawNetwork"] != "SOL" {
		t.Fatalf("metadata missing routing details: %+v", quote.Metadata)
	}
}

func TestBingXExecuteRequiresExplicitTradeEnable(t *testing.T) {
	provider := &BingXProvider{
		AllowedAssets:   "SOL",
		AllowedNetworks: "SOLANA",
	}
	_, err := provider.Execute(context.Background(), Request{
		Asset:       "SOL",
		Network:     "SOLANA",
		DestAddress: "5Q7uLxkVQ2mN4wH3rP9tB6sC1zY5aE7gF2hJ3kL4mN5A",
	}, Quote{CryptoAmount: 1})
	if err == nil || !strings.Contains(err.Error(), "trade disabled") {
		t.Fatalf("expected trade disabled error, got %v", err)
	}
}

func TestBingXExecutePlacesSignedMarketBuyAndStopsBeforeWithdraw(t *testing.T) {
	const secret = "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openApi/spot/v1/trade/order" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("X-BX-APIKEY") != "test-key" {
			t.Fatalf("missing api key header")
		}
		query := r.URL.Query()
		signature := query.Get("signature")
		query.Del("signature")
		if signature == "" {
			t.Fatalf("missing signature")
		}
		if signature != hmacSHA256Hex(query.Encode(), secret) {
			t.Fatalf("invalid signature")
		}
		if query.Get("symbol") != "SOL-USDT" || query.Get("side") != "BUY" || query.Get("type") != "MARKET" || query.Get("quantity") != "1.25" {
			t.Fatalf("unexpected order query %s", query.Encode())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"orderId":"ord-1"}}`))
	}))
	defer server.Close()

	provider := &BingXProvider{
		BaseURL:         server.URL,
		APIKey:          "test-key",
		APISecret:       secret,
		RecvWindowMS:    5000,
		AllowedAssets:   "SOL",
		AllowedNetworks: "SOLANA",
		TradeEnabled:    true,
		WithdrawEnabled: false,
		Now:             fixedBingXNow,
	}
	exec, err := provider.Execute(context.Background(), Request{
		Asset:       "SOL",
		Network:     "SOLANA",
		DestAddress: "5Q7uLxkVQ2mN4wH3rP9tB6sC1zY5aE7gF2hJ3kL4mN5A",
	}, Quote{CryptoAmount: 1.25, Metadata: map[string]any{"symbol": "SOL-USDT"}})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if exec.Provider != "bingx" || exec.ExternalOrderID != "ord-1" || exec.Status != "pending_withdrawal" {
		t.Fatalf("unexpected execution: %+v", exec)
	}
}

func TestBingXQuotesDirectUSDTWithoutSpotPair(t *testing.T) {
	provider := &BingXProvider{AllowedAssets: "USDT,SOL", AllowedNetworks: "BSC,SOLANA"}
	quote, err := provider.Quote(context.Background(), Request{
		OrderID:      "buy-usdt",
		Asset:        "USDT",
		Network:      "BSC",
		CryptoAmount: 10,
		DestAddress:  "0x0000000000000000000000000000000000000001",
	})
	if err != nil {
		t.Fatalf("expected USDT direct route to be quoted: %v", err)
	}
	if quote.Asset != "USDT" || quote.CryptoAmount != 10 || quote.Metadata["symbol"] != "USDT" {
		t.Fatalf("unexpected USDT quote: %+v", quote)
	}
}

func fixedBingXNow() time.Time {
	return time.UnixMilli(1720000000000).UTC()
}

func hmacSHA256Hex(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
