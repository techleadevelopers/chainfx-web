package e2e_test

import "testing"

func TestBuyQuoteAndIdempotencyContract(t *testing.T) {
	c := newE2EClient(t)

	quote := c.post("/quote", map[string]any{
		"side":            "buy",
		"asset":           "USDT",
		"fiatCurrency":    "BRL",
		"amountFiat":      50,
		"paymentMethod":   "pix",
		"deliveryNetwork": "BEP20",
	}, "")
	requireStatus(t, quote, 200, 201)
	quoteID := requireStringField(t, quote.Body, "quoteId")

	key := idempotencyKey("buy")
	body := map[string]any{
		"quoteId":        quoteID,
		"asset":          "USDT",
		"fiatCurrency":   "BRL",
		"amountFiat":     50,
		"paymentMethod":  "pix",
		"destAddress":    envWallet("E2E_DEST_WALLET", testAgentWallet),
		"customer":       map[string]any{"name": "E2E Buyer"},
		"idempotencyKey": key,
	}
	created := c.post("/buy", body, key)
	requireStatus(t, created, 200, 201)

	replay := c.post("/buy", body, key)
	requireStatus(t, replay, 200, 201)

	divergent := map[string]any{
		"quoteId":        quoteID,
		"asset":          "USDT",
		"fiatCurrency":   "BRL",
		"amountFiat":     60,
		"paymentMethod":  "pix",
		"destAddress":    envWallet("E2E_DEST_WALLET", testAgentWallet),
		"idempotencyKey": key,
	}
	conflict := c.post("/buy", divergent, key)
	requireStatus(t, conflict, 409)
}

func TestBuyRejectsConsumedQuote(t *testing.T) {
	c := newE2EClient(t)
	quote := c.post("/quote", map[string]any{
		"side":         "buy",
		"asset":        "USDT",
		"fiatCurrency": "BRL",
		"amountFiat":   50,
	}, "")
	requireStatus(t, quote, 200, 201)
	quoteID := requireStringField(t, quote.Body, "quoteId")

	body := map[string]any{
		"quoteId":      quoteID,
		"asset":        "USDT",
		"fiatCurrency": "BRL",
		"amountFiat":   50,
		"destAddress":  envWallet("E2E_DEST_WALLET", testAgentWallet),
	}
	requireStatus(t, c.post("/buy", body, idempotencyKey("buy-first")), 200, 201)
	requireStatus(t, c.post("/buy", body, idempotencyKey("buy-consumed")), 409)
}
