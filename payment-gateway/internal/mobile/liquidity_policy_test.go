package mobile

import (
	"testing"

	"payment-gateway/internal/config"
)

func TestMobileLiquiditySupportedPairsRejectsInvalidCartesianFallbackPairs(t *testing.T) {
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

	if !s.mobileLiquidityPairSupported("BTC", "BITCOIN") {
		t.Fatalf("expected native BTC:BITCOIN to be supported")
	}
	if !s.mobileLiquidityPairSupported("SOL", "SOLANA") {
		t.Fatalf("expected native SOL:SOLANA to be supported")
	}
	if !s.mobileLiquidityPairSupported("USDT", "BSC") {
		t.Fatalf("expected configured USDT:BSC to be supported")
	}
	if !s.mobileLiquidityPairSupported("USDC", "BASE") {
		t.Fatalf("expected configured USDC:BASE to be supported")
	}
	if !s.mobileLiquidityPairSupported("ETH", "BASE") {
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
		if s.mobileLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to be rejected", tc.asset, tc.network)
		}
	}

	pairs := s.mobileLiquiditySupportedPairs()
	if len(pairs) != 5 {
		t.Fatalf("expected 5 executable pairs, got %d: %+v", len(pairs), pairs)
	}
}

func TestMobileLiquiditySupportedPairsRequiresRealRouterProviderForNonHotWalletPairs(t *testing.T) {
	s := &Server{cfg: &config.Config{
		LiquidityRouterEnabled:   true,
		LiquidityAllowedPairs:    "USDT:BSC:0x55d398326f99059fF775485246999027B3197955:18,BTC:BITCOIN::8,SOL:SOLANA::9,ETH:BASE::18",
		LiquidityAllowedAssets:   "USDT,BTC,SOL,ETH",
		LiquidityAllowedNetworks: "BSC,BITCOIN,SOLANA,BASE",
		SupportedNetworks:        "BSC,BITCOIN,SOLANA,BASE",
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
	}}

	if !s.mobileLiquidityPairSupported("USDT", "BSC") {
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
		if !s.mobileLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to remain declared for receive/catalog", tc.asset, tc.network)
		}
		if s.mobileBuyLiquidityPairSupported(tc.asset, tc.network) {
			t.Fatalf("expected %s:%s to require an executable router provider for buy", tc.asset, tc.network)
		}
	}
	pairs := s.mobileLiquiditySupportedPairs()
	if len(pairs) != 3 {
		t.Fatalf("expected all declared catalog pairs, got %+v", pairs)
	}
	for _, pair := range pairs {
		if pair["asset"] == "USDT" && pair["network"] == "BSC" {
			if pair["buy_enabled"] != true {
				t.Fatalf("expected USDT:BSC buy_enabled=true, got %+v", pair)
			}
			continue
		}
		if pair["buy_enabled"] == true {
			t.Fatalf("expected non-hot pairs to have buy_enabled=false without provider, got %+v", pair)
		}
	}
}

func TestMobileLiquiditySupportedPairsKeepsUSDTBSCWithExtendedProductionPairs(t *testing.T) {
	const allowedPairs = "USDT:BSC:0x55d398326f99059fF775485246999027B3197955:18,USDT:POLYGON:0xc2132D05D31c914a87C6611C10748AEb04B58e8F:6,BTC:BITCOIN::8,SOL:SOLANA::9,BNB:BSC::18,ETH:BASE::18,ETH:ARBITRUM::18,ETH:ETHEREUM::18,USDC:BASE:0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913:6,USDC:ARBITRUM:0xaf88d065e77c8cC2239327C5EDb3A432268e5831:6,USDC:ETHEREUM:0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48:6,USDT:ETHEREUM:0xdAC17F958D2ee523a2206206994597C13D831ec7:6,USDT:ARBITRUM:0xfd086bc7CD5C481DCC9C85ebe478A1C0b69FCbb9:6,LINK:ETHEREUM:0x514910771AF9Ca656af840dff83E8264EcF986CA:18,LINK:ARBITRUM:0xf97f4df75117a78c1A5a0DBb814Af92458539FB4:18,LINK:BSC:0xF8A0BF9cF54Bb92F17374d9e9A321E6a111a51bD:18,LINK:POLYGON:0x53E0bca35eC356BD5ddDFebbd1Fc0fD03FaBad39:18,AVAX:BSC:0x1CE0c2827e2eF14D5C4f29a091d735A204794041:18,AVAX:POLYGON:0x2C89bbc92BD86F8075d1DEcc58C7F4E0107f286b:18"
	s := &Server{cfg: &config.Config{
		LiquidityRouterEnabled:   true,
		LiquidityAllowedPairs:    allowedPairs,
		LiquidityAllowedAssets:   "USDT,BTC,BNB,SOL,ETH,LINK,AVAX",
		LiquidityAllowedNetworks: "BSC,POLYGON,BASE,ARBITRUM,ETHEREUM,BITCOIN,SOLANA",
		SupportedNetworks:        "BSC,POLYGON,BASE,ARBITRUM,ETHEREUM,BITCOIN,SOLANA",
		LiquidityProviderURLs:    "mock=https://liquidity-provider.local",
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
		PolygonUsdtContract:      "0xc2132D05D31c914a87C6611C10748AEb04B58e8F",
		BingXEnabled:             true,
		BingXAPIKey:              "key",
		BingXAPISecret:           "secret",
		BingXTradeEnabled:        true,
		BingXWithdrawEnabled:     true,
		BingXAllowedAssets:       "BTC,ETH,BNB,SOL,LINK,AVAX",
		BingXAllowedNetworks:     "BITCOIN,BSC,POLYGON,SOLANA,BASE,ARBITRUM,ETHEREUM",
	}}

	if !s.mobileBuyLiquidityPairSupported("USDT", "BSC") {
		t.Fatalf("expected USDT:BSC to stay buy-enabled with extended pairs")
	}

	expectedUSDTPairs := map[string]bool{
		"BSC":      false,
		"POLYGON":  false,
		"ARBITRUM": false,
		"ETHEREUM": false,
	}
	for _, pair := range s.mobileLiquiditySupportedPairs() {
		if pair["asset"] == "USDT" {
			network, _ := pair["network"].(string)
			if _, ok := expectedUSDTPairs[network]; !ok {
				continue
			}
			expectedUSDTPairs[network] = true
			if pair["buy_enabled"] != true {
				t.Fatalf("expected USDT:%s buy_enabled=true, got %+v", network, pair)
			}
		}
	}
	for network, found := range expectedUSDTPairs {
		if !found {
			t.Fatalf("expected supported pairs to include USDT:%s, got %+v", network, s.mobileLiquiditySupportedPairs())
		}
	}
}

func TestMobileLiquiditySupportedPairsKeepsUSDTBSCWhenAllowedPairsValueIsQuoted(t *testing.T) {
	const allowedPairs = `"USDT:BSC:0x55d398326f99059fF775485246999027B3197955:18,BTC:BITCOIN::8,SOL:SOLANA::9"`
	s := &Server{cfg: &config.Config{
		LiquidityRouterEnabled:   true,
		LiquidityAllowedPairs:    allowedPairs,
		LiquidityAllowedAssets:   "USDT,BTC,SOL",
		LiquidityAllowedNetworks: "BSC,BITCOIN,SOLANA",
		SupportedNetworks:        "BSC,BITCOIN,SOLANA",
		BscUsdtContract:          "0x55d398326f99059fF775485246999027B3197955",
		LiquidityProviderURLs:    "mock=https://liquidity-provider.local",
	}}

	if !s.mobileBuyLiquidityPairSupported("USDT", "BSC") {
		t.Fatalf("expected quoted env value to keep USDT:BSC buy-enabled")
	}
	foundUSDTBSC := false
	for _, pair := range s.mobileLiquiditySupportedPairs() {
		if pair["asset"] == "USDT" && pair["network"] == "BSC" {
			foundUSDTBSC = true
			if pair["buy_enabled"] != true {
				t.Fatalf("expected quoted USDT:BSC buy_enabled=true, got %+v", pair)
			}
		}
	}
	if !foundUSDTBSC {
		t.Fatalf("expected supported pairs to include USDT:BSC, got %+v", s.mobileLiquiditySupportedPairs())
	}
}
