package money

import "testing"

func TestFiatTokenConversionsRoundToMinorUnits(t *testing.T) {
	rate := RateFromFloat(5.13)
	tokens := TokenFromFloat(20)

	fiat := FiatFromTokens(tokens, rate)
	if fiat.String() != "102.60" {
		t.Fatalf("expected 102.60, got %s", fiat.String())
	}

	back := TokensFromFiat(fiat, rate)
	if back.String() != "20.000000" {
		t.Fatalf("expected 20.000000, got %s", back.String())
	}
}

func TestBpsCalculationsUseIntegerRounding(t *testing.T) {
	amount := MoneyFromFloat(100)
	if got := FeeBps(amount, 275); got.String() != "2.75" {
		t.Fatalf("expected 2.75, got %s", got.String())
	}

	rate := RateFromFloat(5.13)
	if got := AddBps(rate, 100).String(); got != "5.1813" {
		t.Fatalf("expected 5.1813, got %s", got)
	}
	if got := SubtractBps(rate, 123).String(); got != "5.066901" {
		t.Fatalf("expected 5.066901, got %s", got)
	}
}

func TestGrossForNetToken(t *testing.T) {
	net := TokenFromFloat(500)
	gross := GrossForNetToken(net, 600)
	if gross.String() != "531.914894" {
		t.Fatalf("expected 531.914894, got %s", gross.String())
	}
	fee := TokenUnits(int64(gross) - int64(net))
	if fee.String() != "31.914894" {
		t.Fatalf("expected 31.914894, got %s", fee.String())
	}
}
