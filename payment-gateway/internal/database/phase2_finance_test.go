package database

import "testing"

func TestQuoteMatchesLocksNetworkAndPaymentMethod(t *testing.T) {
	q := &Quote{
		Side:          "buy",
		Asset:         "SOL",
		Network:       "SOLANA",
		FiatCurrency:  "BRL",
		PaymentMethod: "pix",
		AmountMinor:   10000,
	}

	if !quoteMatches(q, QuoteConsumeInput{Side: "buy", Asset: "SOL", Network: "SOLANA", FiatCurrency: "BRL", PaymentMethod: "pix", AmountMinor: 10000}) {
		t.Fatal("expected exact quote lock to match")
	}
	if quoteMatches(q, QuoteConsumeInput{Side: "buy", Asset: "SOL", Network: "BSC", FiatCurrency: "BRL", PaymentMethod: "pix", AmountMinor: 10000}) {
		t.Fatal("expected network mismatch to be rejected")
	}
	if quoteMatches(q, QuoteConsumeInput{Side: "buy", Asset: "SOL", Network: "SOLANA", FiatCurrency: "BRL", PaymentMethod: "credit_card", AmountMinor: 10000}) {
		t.Fatal("expected payment method mismatch to be rejected")
	}
	if quoteMatches(q, QuoteConsumeInput{Side: "buy", Asset: "SOL", Network: "SOLANA", FiatCurrency: "BRL", PaymentMethod: "pix", AmountMinor: 10100}) {
		t.Fatal("expected amount mismatch to be rejected")
	}
}
