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
