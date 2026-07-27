package server

import (
	"testing"

	"payment-gateway/internal/config"
)

func TestValidBuyDeliveryAddressUsesStrictSolanaValidation(t *testing.T) {
	valid := "11111111111111111111111111111111"
	if !validBuyDeliveryAddress("SOLANA", valid) {
		t.Fatalf("expected 32-byte Solana public key to be accepted")
	}

	invalid := "111111111111111111111111111111111"
	if validBuyDeliveryAddress("SOLANA", invalid) {
		t.Fatalf("expected base58 address with invalid decoded length to be rejected")
	}
}

func TestExecutableBuyPairsRejectsInvalidCartesianFallbackPairs(t *testing.T) {
	s := &Server{cfg: &config.Config{
		LiquidityRouterEnabled:   true,
		LiquidityAllowedPairs:    "",
		LiquidityAllowedAssets:   "USDT,BTC,SOL,USDC,ETH",
		LiquidityAllowedNetworks: "BSC,BITCOIN,SOLANA,BASE",
		SupportedNetworks:        "BSC,BITCOIN,SOLANA,BASE",
		LiquidityProviderURLs:    "mock=https://liquidity-provider.local",
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
		BaseUsdcContract:         "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		BingXEnabled:             true,
		BingXAPIKey:              "key",
		BingXAPISecret:           "secret",
		BingXTradeEnabled:        true,
		BingXWithdrawEnabled:     true,
		BingXAllowedAssets:       "BTC,SOL,ETH",
		BingXAllowedNetworks:     "BITCOIN,SOLANA,BASE",
	}}

	if !s.buyLiquidityPairSupported("BTC", "BITCOIN") {
		t.Fatalf("expected native BTC:BITCOIN to be supported")
	}
	if !s.buyLiquidityPairSupported("SOL", "SOLANA") {
		t.Fatalf("expected native SOL:SOLANA to be supported")
	}
	if !s.buyLiquidityPairSupported("USDT", "BSC") {
		t.Fatalf("expected configured USDT:BSC to be supported")
	}
	if !s.buyLiquidityPairSupported("USDC", "BASE") {
		t.Fatalf("expected configured USDC:BASE to be supported")
	}
	if !s.buyLiquidityPairSupported("ETH", "BASE") {
		t.Fatalf("expected native ETH:BASE to be supported")
	}

	for _, tc := range []struct {
		asset   string
		network string
	}{
		{"BTC", "BSC"},
		{"SOL", "BSC"},
		{"USDT", "BITCOIN"},
		{"USDT", "SOLANA"},
		{"USDT", "BASE"},
	} {
		if s.buyLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to be rejected", tc.asset, tc.network)
		}
	}

	pairs := s.executableBuyPairs()
	if len(pairs) != 5 {
		t.Fatalf("expected 5 executable pairs, got %d: %+v", len(pairs), pairs)
	}
}

func TestExecutableBuyPairsRequiresRealRouterProviderForNonHotWalletPairs(t *testing.T) {
	s := &Server{cfg: &config.Config{
		LiquidityRouterEnabled:   true,
		LiquidityAllowedPairs:    "USDT:BSC:0x55d398326f99059fF775485246999027B3197955:18,BTC:BITCOIN::8,SOL:SOLANA::9,ETH:BASE::18",
		LiquidityAllowedAssets:   "USDT,BTC,SOL,ETH",
		LiquidityAllowedNetworks: "BSC,BITCOIN,SOLANA,BASE",
		SupportedNetworks:        "BSC,BITCOIN,SOLANA,BASE",
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
	}}

	if !s.buyLiquidityPairSupported("USDT", "BSC") {
		t.Fatalf("expected hot-wallet USDT:BSC to remain supported")
	}
	for _, tc := range []struct {
		asset   string
		network string
	}{
		{"BTC", "BITCOIN"},
		{"SOL", "SOLANA"},
		{"ETH", "BASE"},
	} {
		if s.buyLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to require an executable router provider", tc.asset, tc.network)
		}
	}
	pairs := s.executableBuyPairs()
	if len(pairs) != 1 || pairs[0].Asset != "USDT" || pairs[0].Network != "BSC" {
		t.Fatalf("expected only USDT:BSC, got %+v", pairs)
	}
}
