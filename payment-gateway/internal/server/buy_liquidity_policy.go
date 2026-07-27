package server

import (
	"net/http"
	"sort"
	"strings"

	"payment-gateway/internal/bitcoin"
	"payment-gateway/internal/liquidity"
	"payment-gateway/internal/solana"
)

func normalizeBuyDeliveryNetwork(network string) string {
	network = strings.TrimSpace(network)
	if network == "" {
		return "BSC"
	}
	return liquidity.NormalizeNetwork(network)
}

func (s *Server) buyLiquidityPairSupported(asset, network string) bool {
	_, ok := s.resolveExecutableBuyPair(asset, network)
	return ok
}

func (s *Server) resolveExecutableBuyPair(asset, network string) (liquidity.Pair, bool) {
	if s == nil || s.cfg == nil {
		return liquidity.Pair{}, false
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeBuyDeliveryNetwork(network)
	if asset == "" || network == "" {
		return liquidity.Pair{}, false
	}
	if !s.buyNetworkEnabled(network) {
		return liquidity.Pair{}, false
	}
	policy := liquidity.NewPairPolicy(s.cfg.LiquidityAllowedPairs)
	var pair liquidity.Pair
	var ok bool
	if !policy.Empty() {
		pair, ok = policy.Resolve(asset, network)
		if !ok {
			return liquidity.Pair{}, false
		}
	} else {
		if !containsCSVFoldServer(s.cfg.LiquidityAllowedAssets, asset) ||
			!containsCSVFoldServer(s.cfg.LiquidityAllowedNetworks, network) {
			return liquidity.Pair{}, false
		}
		pair, ok = liquidity.ParsePair(asset + ":" + network)
		if !ok {
			return liquidity.Pair{}, false
		}
	}
	pair, ok = s.hydrateAndValidateBuyPair(pair)
	if !ok {
		return liquidity.Pair{}, false
	}
	if !s.buyPairHasExecutionRoute(pair) {
		return liquidity.Pair{}, false
	}
	return pair, true
}

func buyPairExecutableWithoutRouter(asset, network string) bool {
	return strings.EqualFold(asset, "USDT") && normalizeBuyDeliveryNetwork(network) == "BSC"
}

func (s *Server) buyPairHasExecutionRoute(pair liquidity.Pair) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	pair = liquidity.EnrichPair(pair)
	if buyPairExecutableWithoutRouter(pair.Asset, pair.Network) {
		return true
	}
	if !s.cfg.LiquidityRouterEnabled {
		return false
	}
	if strings.TrimSpace(s.cfg.LiquidityProviderURLs) != "" {
		return true
	}
	if !s.cfg.BingXEnabled || !s.cfg.BingXTradeEnabled || !s.cfg.BingXWithdrawEnabled {
		return false
	}
	if strings.TrimSpace(s.cfg.BingXAPIKey) == "" || strings.TrimSpace(s.cfg.BingXAPISecret) == "" {
		return false
	}
	return containsCSVFoldServer(s.cfg.BingXAllowedAssets, pair.Asset) &&
		containsCSVFoldServer(s.cfg.BingXAllowedNetworks, pair.Network)
}

func (s *Server) handleBuyPairs(w http.ResponseWriter, r *http.Request) {
	pairs := s.executableBuyPairs()
	assets := make([]string, 0, len(pairs))
	networks := make([]string, 0, len(pairs))
	seenAssets := map[string]bool{}
	seenNetworks := map[string]bool{}
	for _, pair := range pairs {
		if !seenAssets[pair.Asset] {
			assets = append(assets, pair.Asset)
			seenAssets[pair.Asset] = true
		}
		if !seenNetworks[pair.Network] {
			networks = append(networks, pair.Network)
			seenNetworks[pair.Network] = true
		}
	}
	sort.Strings(assets)
	sort.Strings(networks)

	routerEnabled := false
	hotWalletFirst := []string{}
	if s != nil && s.cfg != nil {
		routerEnabled = s.cfg.LiquidityRouterEnabled
		hotWalletFirst = splitCSVUpper(s.cfg.LiquidityHotWalletFirstAssets)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pairs":             pairs,
		"assets":            assets,
		"networks":          networks,
		"routerEnabled":     routerEnabled,
		"hotWalletFirst":    hotWalletFirst,
		"backendEnforced":   true,
		"selectionContract": "asset+network+contract+decimals",
	})
}

func (s *Server) executableBuyPairs() []liquidity.Pair {
	if s == nil || s.cfg == nil {
		return nil
	}
	policy := liquidity.NewPairPolicy(s.cfg.LiquidityAllowedPairs)
	var candidates []liquidity.Pair
	if !policy.Empty() {
		candidates = policy.Pairs()
	} else {
		for _, asset := range splitCSVUpper(s.cfg.LiquidityAllowedAssets) {
			for _, network := range splitCSVUpper(s.cfg.LiquidityAllowedNetworks) {
				candidates = append(candidates, liquidity.EnrichPair(liquidity.Pair{Asset: asset, Network: network}))
			}
		}
	}

	out := make([]liquidity.Pair, 0, len(candidates))
	seen := map[string]bool{}
	for _, pair := range candidates {
		resolved, ok := s.resolveExecutableBuyPair(pair.Asset, pair.Network)
		if !ok {
			continue
		}
		key := resolved.Asset + ":" + resolved.Network
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, resolved)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Asset == out[j].Asset {
			return out[i].Network < out[j].Network
		}
		return out[i].Asset < out[j].Asset
	})
	return out
}

func (s *Server) hydrateAndValidateBuyPair(pair liquidity.Pair) (liquidity.Pair, bool) {
	if s == nil || s.cfg == nil {
		return liquidity.Pair{}, false
	}
	pair.Asset = strings.ToUpper(strings.TrimSpace(pair.Asset))
	pair.Network = normalizeBuyDeliveryNetwork(pair.Network)
	pair.ContractAddress = strings.TrimSpace(pair.ContractAddress)
	if pair.Asset == "" || pair.Network == "" {
		return liquidity.Pair{}, false
	}
	if pair.Decimals <= 0 {
		pair.Decimals = liquidity.DefaultDecimals(pair.Asset, pair.Network)
	}
	if pair.ContractAddress == "" {
		switch pair.Asset + ":" + pair.Network {
		case "USDT:BSC":
			pair.ContractAddress = strings.TrimSpace(s.cfg.BscUsdtContract)
		case "USDT:POLYGON":
			pair.ContractAddress = strings.TrimSpace(s.cfg.PolygonUsdtContract)
			if pair.Decimals == 18 {
				pair.Decimals = 6
			}
		case "USDC:BASE":
			pair.ContractAddress = strings.TrimSpace(s.cfg.BaseUsdcContract)
			pair.Decimals = 6
		case "USDC:ARBITRUM":
			pair.ContractAddress = strings.TrimSpace(s.cfg.ArbitrumUsdcContract)
			pair.Decimals = 6
		case "USDC:ETHEREUM":
			pair.ContractAddress = strings.TrimSpace(s.cfg.EthereumUsdcContract)
			pair.Decimals = 6
		}
	}
	pair = liquidity.EnrichPair(pair)
	if buyPairIsNative(pair) {
		return pair, true
	}
	if liquidity.IsEVMNetwork(pair.Network) {
		return pair, isEVMDeliveryAddress(pair.ContractAddress)
	}
	if pair.Network == "SOLANA" {
		return pair, solana.ValidateAddress(pair.ContractAddress) == nil
	}
	if pair.Network == "APTOS" {
		return pair, looksLikeFixedHexAddress(pair.ContractAddress, 64)
	}
	return liquidity.Pair{}, false
}

func buyPairIsNative(pair liquidity.Pair) bool {
	return liquidity.IsNativeAsset(pair.Asset, pair.Network) ||
		(pair.Asset == "BTC" && pair.Network == "BITCOIN")
}

func validBuyDeliveryAddress(network, address string) bool {
	address = strings.TrimSpace(address)
	network = normalizeBuyDeliveryNetwork(network)
	if liquidity.IsEVMNetwork(network) {
		return isEVMDeliveryAddress(address)
	}
	switch network {
	case "BITCOIN":
		return bitcoin.ValidateAddress(address, "bc") == nil ||
			bitcoin.ValidateAddress(address, "tb") == nil ||
			bitcoin.ValidateAddress(address, "bcrt") == nil
	case "SOLANA":
		return solana.ValidateAddress(address) == nil
	case "APTOS":
		return looksLikeFixedHexAddress(address, 64)
	default:
		return false
	}
}

func (s *Server) buyNetworkEnabled(network string) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	network = normalizeBuyDeliveryNetwork(network)
	if _, ok := liquidity.NetworkMetadata(network); !ok {
		return false
	}
	raw := strings.TrimSpace(s.cfg.SupportedNetworks)
	if raw == "" {
		raw = s.cfg.LiquidityAllowedNetworks
	}
	return containsCSVFoldServer(raw, network)
}

func containsCSVFoldServer(raw, value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		if strings.ToUpper(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}

func splitCSVUpper(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		value := strings.ToUpper(strings.TrimSpace(item))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func looksLikeBase58Address(address string, minLen, maxLen int) bool {
	if len(address) < minLen || len(address) > maxLen {
		return false
	}
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, ch := range address {
		if !strings.ContainsRune(alphabet, ch) {
			return false
		}
	}
	return true
}

func looksLikeFixedHexAddress(address string, hexLen int) bool {
	address = strings.TrimSpace(address)
	if !strings.HasPrefix(address, "0x") || len(address) != hexLen+2 {
		return false
	}
	for _, ch := range address[2:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}
