package e2e_test

import "testing"

func TestSellQuoteAndIdempotencyContract(t *testing.T) {
	c := newE2EClient(t)

	quote := c.post("/quote", map[string]any{
		"side":         "sell",
		"asset":        "USDT",
		"fiatCurrency": "BRL",
		"amountUSDT":   10,
	}, "")
	requireStatus(t, quote, 200, 201)
	quoteID := requireStringField(t, quote.Body, "quoteId")

	key := idempotencyKey("sell")
	body := map[string]any{
		"quoteId":        quoteID,
		"asset":          "USDT",
		"amountUSDT":     10,
		"fiatCurrency":   "BRL",
		"senderWallet":   envWallet("E2E_PAYER_WALLET", testPayerWallet),
		"pixKey":         getenv("E2E_PIX_KEY", "e2e@example.com"),
		"idempotencyKey": key,
	}
	created := c.post("/sell", body, key)
	requireStatus(t, created, 200, 201)

	replay := c.post("/sell", body, key)
	requireStatus(t, replay, 200, 201)

	divergent := map[string]any{
		"quoteId":        quoteID,
		"asset":          "USDT",
		"amountUSDT":     11,
		"fiatCurrency":   "BRL",
		"senderWallet":   envWallet("E2E_PAYER_WALLET", testPayerWallet),
		"pixKey":         getenv("E2E_PIX_KEY", "e2e@example.com"),
		"idempotencyKey": key,
	}
	conflict := c.post("/sell", divergent, key)
	requireStatus(t, conflict, 409)
}
