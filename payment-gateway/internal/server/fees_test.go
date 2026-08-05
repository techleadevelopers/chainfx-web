package server

import (
	"testing"

	"payment-gateway/internal/config"
)

func TestTransactionFeePercentPlusFixedUsdForBRL(t *testing.T) {
	s := &Server{cfg: &config.Config{FeeBps: 200, FeeFixedUsd: 2}}

	fee := s.transactionFee(100, "BRL", 5)
	if fee != 12 {
		t.Fatalf("expected 12 BRL fee, got %.2f", fee)
	}
}

func TestTransactionFeePercentPlusFixedUsdForUSD(t *testing.T) {
	s := &Server{cfg: &config.Config{FeeBps: 200, FeeFixedUsd: 2}}

	fee := s.transactionFee(100, "USD", 1)
	if fee != 4 {
		t.Fatalf("expected 4 USD fee, got %.2f", fee)
	}
}

func TestTransactionFeeAddsPerUsdtFeeForBRL(t *testing.T) {
	s := &Server{cfg: &config.Config{FeeBps: 200, FeeFixedUsd: 0, FeePerUsdtUsd: 0.03}}

	fee := s.transactionFee(100, "BRL", 5)
	if fee != 5 {
		t.Fatalf("expected 5 BRL fee, got %.2f", fee)
	}
}

func TestSellRateUsesConfiguredBps(t *testing.T) {
	s := &Server{cfg: &config.Config{SellRateBps: 8772}}

	rate := s.sellRate(5.13)
	if rate != 4.5 {
		t.Fatalf("expected sell rate 4.50, got %.4f", rate)
	}
}

func TestSellQuotePaysPixBRLWithSellRate(t *testing.T) {
	s := &Server{cfg: &config.Config{SellRateBps: 8772}}

	rate, payout, spread := s.sellQuote(20, 5.13)
	if rate != 4.5 {
		t.Fatalf("expected sell rate 4.50, got %.4f", rate)
	}
	if payout != 90 {
		t.Fatalf("expected payout 90.00 BRL, got %.2f", payout)
	}
	if spread != 12.6 {
		t.Fatalf("expected spread 12.60 BRL, got %.2f", spread)
	}
}

func TestBTCSellQuoteDoesNotUseFixedUSDTBRLAsBTCRate(t *testing.T) {
	s := &Server{cfg: &config.Config{SellUsdtBrlRate: 5.00}}

	rate, payout, _ := s.sellQuoteForAsset("BTC", 0.01, 350000)
	if rate != 350000 {
		t.Fatalf("expected BTC sell rate to use BTCBRL market rate, got %.2f", rate)
	}
	if payout != 3500 {
		t.Fatalf("expected 0.01 BTC payout at BTCBRL rate, got %.2f", payout)
	}
}

func TestFeeFreeModeZeroesBuyFeeAndRateSpread(t *testing.T) {
	s := &Server{cfg: &config.Config{
		FeeFreeMode:      true,
		BuyTier1Bps:      750,
		BuyTier2Bps:      550,
		BuyTier3Bps:      450,
		BuyNetworkFeeBrl: 1.99,
		BuyMinFeeBrl:     4.99,
		BuyRateSpreadBps: 100,
	}}

	if fee := s.transactionFee(100, "BRL", 5); fee != 0 {
		t.Fatalf("expected zero buy fee, got %.2f", fee)
	}
	if rate := s.buyRate(5.13); rate != 5.13 {
		t.Fatalf("expected market rate without spread, got %.4f", rate)
	}
}

func TestFeeFreeModeZeroesSellUSDTSpread(t *testing.T) {
	s := &Server{cfg: &config.Config{FeeFreeMode: true, SellRateBps: 8772}}

	rate, payout, spread := s.sellQuoteForAsset("USDT", 100, 5)
	if rate != 5 {
		t.Fatalf("expected market sell rate, got %.4f", rate)
	}
	if payout != 500 {
		t.Fatalf("expected full market payout, got %.2f", payout)
	}
	if spread != 0 {
		t.Fatalf("expected zero sell spread, got %.2f", spread)
	}
}

func TestFeeFreeModeZeroesSellBTCSpread(t *testing.T) {
	s := &Server{cfg: &config.Config{FeeFreeMode: true, SellRateBps: 8772}}

	rate, payout, spread := s.sellQuoteForAsset("BTC", 0.01, 350000)
	if rate != 350000 {
		t.Fatalf("expected BTC market sell rate, got %.2f", rate)
	}
	if payout != 3500 {
		t.Fatalf("expected full BTC market payout, got %.2f", payout)
	}
	if spread != 0 {
		t.Fatalf("expected zero BTC sell spread, got %.2f", spread)
	}
}
