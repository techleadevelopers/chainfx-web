package liquidity

import "testing"

func TestParsePairEnrichesFamilyStandardAndNativeDecimals(t *testing.T) {
	cases := []struct {
		raw      string
		network  string
		family   string
		standard string
		decimals int
	}{
		{"ETH:BASE::18", "BASE", "EVM", "NATIVE", 18},
		{"SOL:SOLANA", "SOLANA", "SOLANA", "NATIVE", 9},
		{"APT:APTOS", "APTOS", "APTOS", "NATIVE", 8},
		{"USDC:ARBITRUM:0xaf88d065e77c8cC2239327C5EDb3A432268e5831:6", "ARBITRUM", "EVM", "ERC20", 6},
	}
	for _, tc := range cases {
		pair, ok := ParsePair(tc.raw)
		if !ok {
			t.Fatalf("ParsePair(%q) failed", tc.raw)
		}
		if pair.Network != tc.network || pair.Family != tc.family || pair.TokenStandard != tc.standard || pair.Decimals != tc.decimals {
			t.Fatalf("ParsePair(%q) = %#v", tc.raw, pair)
		}
	}
}

func TestNormalizeNetworkCoversTargetRails(t *testing.T) {
	aliases := map[string]string{
		"BEP-20":       "BSC",
		"MATIC":        "POLYGON",
		"base_mainnet": "BASE",
		"arb":          "ARBITRUM",
		"erc20":        "ETHEREUM",
		"sol":          "SOLANA",
		"apt":          "APTOS",
	}
	for raw, want := range aliases {
		if got := NormalizeNetwork(raw); got != want {
			t.Fatalf("NormalizeNetwork(%q)=%q want %q", raw, got, want)
		}
	}
}
