package workers

import (
	"strings"
	"testing"

	"payment-gateway/internal/config"
	"payment-gateway/internal/money"
)

func TestDCABRLParsingUsesMinorUnits(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"0.01", "0.01"},
		{"0.10", "0.10"},
		{"19.99", "19.99"},
		{"20.00", "20.00"},
		{"99.99", "99.99"},
		{"100.00", "100.00"},
		{"999.99", "999.99"},
		{"1000.00", "1000.00"},
	} {
		got, err := dcaParseBRL(tc.raw)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.raw, err)
		}
		if got.String() != tc.want {
			t.Fatalf("parse %s = %s, want %s", tc.raw, got.String(), tc.want)
		}
	}
}

func TestDCABRLParsingRejectsImplicitRounding(t *testing.T) {
	if _, err := dcaParseBRL("19.999"); err == nil {
		t.Fatal("expected amount with more than 2 decimal places to be rejected")
	}
}

func TestDCAFeeRoundingIsExplicitHalfUp(t *testing.T) {
	worker := &DCAWorker{}
	worker.cfg = &config.Config{}
	worker.cfg.BuyRateSpreadBps = 275

	fee, payout := worker.dcaFeeAndPayout(money.MoneyMinor(1999))
	if fee.String() != "0.55" {
		t.Fatalf("fee = %s, want 0.55", fee.String())
	}
	if payout.String() != "19.44" {
		t.Fatalf("payout = %s, want 19.44", payout.String())
	}
}

func TestDCAUSDTFundingRoundsUpToMicroUnits(t *testing.T) {
	required, err := dcaUSDTMicroCeil(money.MoneyMinor(1), "5.00")
	if err != nil {
		t.Fatalf("required micro: %v", err)
	}
	if required != 2000 {
		t.Fatalf("required micro = %d, want 2000", required)
	}

	required, err = dcaUSDTMicroCeil(money.MoneyMinor(1999), "5.13")
	if err != nil {
		t.Fatalf("required micro: %v", err)
	}
	if required != 3896687 {
		t.Fatalf("required micro = %d, want 3896687", required)
	}
}

func TestDCACryptoAmountSupportsManyDecimalsWithoutFloatAuthority(t *testing.T) {
	got, err := dcaCryptoFromFiat(money.MoneyMinor(1999), "123456.789123456789")
	if err != nil {
		t.Fatalf("crypto amount: %v", err)
	}
	if got == "" || got == "0" {
		t.Fatalf("crypto amount should be positive, got %q", got)
	}
	if strings.ContainsAny(got, "eE") {
		t.Fatalf("crypto amount must be fixed decimal, got %q", got)
	}
	parts := strings.SplitN(got, ".", 2)
	if len(parts) != 2 || len(parts[1]) > dcaCryptoDisplayDecimals {
		t.Fatalf("crypto amount must use at most %d decimals, got %q", dcaCryptoDisplayDecimals, got)
	}
}

func TestDCAQuoteSnapshotMatchesReservedEconomicAmount(t *testing.T) {
	amount := money.MoneyMinor(10_000)
	fee := money.FeeBps(amount, 0)
	payout := amount - fee
	crypto, err := dcaCryptoFromFiat(payout, "5.00")
	if err != nil {
		t.Fatalf("crypto amount: %v", err)
	}
	required, err := dcaUSDTMicroCeil(amount, "5.00")
	if err != nil {
		t.Fatalf("funding: %v", err)
	}
	quote := dcaQuoteSnapshot{
		AmountBRL:    amount,
		FeeBRL:       fee,
		PayoutBRL:    payout,
		RateBRL:      "5.00",
		CryptoAmount: crypto,
		RequiredUSDT: required,
	}
	if quote.AmountBRL.String() != "100.00" || quote.PayoutBRL.String() != "100.00" {
		t.Fatalf("gross/payout mismatch: gross=%s payout=%s", quote.AmountBRL.String(), quote.PayoutBRL.String())
	}
	if quote.CryptoAmount != "20" {
		t.Fatalf("crypto amount = %s, want 20", quote.CryptoAmount)
	}
	if quote.RequiredUSDT != 20_000_000 {
		t.Fatalf("reserve = %d, want 20000000", quote.RequiredUSDT)
	}
}
