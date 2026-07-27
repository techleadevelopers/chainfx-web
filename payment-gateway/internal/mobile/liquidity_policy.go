package mobile

import (
	"strings"

	"payment-gateway/internal/liquidity"
	"payment-gateway/internal/solana"
)

func normalizeMobileBuyNetwork(network string) string {
	network = strings.TrimSpace(network)
	if network == "" {
		return "BSC"
	}
	normalized := liquidity.NormalizeNetwork(network)
	if _, ok := liquidity.NetworkMetadata(normalized); !ok {
		return ""
	}
	return normalized
}

func (s *Server) mobileLiquidityPairSupported(asset, network string) bool {
	_, ok := s.resolveMobileLiquidityPair(asset, network)
	return ok
}

func (s *Server) mobileBuyLiquidityPairSupported(asset, network string) bool {
	pair, ok := s.resolveMobileLiquidityPair(asset, network)
	return ok && s.mobileBuyPairHasExecutionRoute(pair)
}

func (s *Server) resolveMobileLiquidityPair(asset, network string) (liquidity.Pair, bool) {
	if s == nil || s.cfg == nil {
		return liquidity.Pair{}, false
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeMobileBuyNetwork(network)
	if asset == "" || network == "" {
		return liquidity.Pair{}, false
	}
	if !s.mobileNetworkEnabled(network) {
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
		if !containsCSVFoldMobile(s.cfg.LiquidityAllowedAssets, asset) ||
			!containsCSVFoldMobile(s.cfg.LiquidityAllowedNetworks, network) {
			return liquidity.Pair{}, false
		}
		pair, ok = liquidity.ParsePair(asset + ":" + network)
		if !ok {
			return liquidity.Pair{}, false
		}
	}
	pair, ok = s.hydrateAndValidateMobileLiquidityPair(pair)
	if !ok {
		return liquidity.Pair{}, false
	}
	return pair, true
}

func (s *Server) mobileLiquiditySupportedPairs() []map[string]any {
	if s == nil || s.cfg == nil {
		return nil
	}
	policy := liquidity.NewPairPolicy(s.cfg.LiquidityAllowedPairs)
	pairs := policy.Pairs()
	if policy.Empty() {
		for _, asset := range splitMobilePolicyItems(s.cfg.LiquidityAllowedAssets) {
			for _, network := range splitMobilePolicyItems(s.cfg.LiquidityAllowedNetworks) {
				pairs = append(pairs, liquidity.Pair{Asset: asset, Network: network})
			}
		}
	}
	if s.mobileHotWalletUSDTBSCAllowed(policy) {
		pairs = append(pairs, liquidity.Pair{
			Asset:           "USDT",
			Network:         "BSC",
			ContractAddress: strings.TrimSpace(s.cfg.BscUsdtContract),
			Decimals:        18,
		})
	}
	out := make([]map[string]any, 0, len(pairs)+2)
	seen := map[string]bool{}
	for _, pair := range pairs {
		resolved, ok := s.resolveMobileLiquidityPair(pair.Asset, pair.Network)
		if !ok && s.mobilePairIsUSDTBSC(pair) && s.mobileHotWalletUSDTBSCAllowed(policy) {
			resolved, ok = s.hydrateAndValidateMobileLiquidityPair(pair)
		}
		if !ok {
			continue
		}
		pair = resolved
		key := pair.Asset + ":" + pair.Network
		if seen[key] {
			continue
		}
		seen[key] = true
		hotWalletEnabled := s.mobileBuyPairExecutableWithoutRouter(pair.Asset, pair.Network)
		buyRouteEnabled := s.mobileBuyPairHasExecutionRoute(pair)
		routerEnabled := buyRouteEnabled && !hotWalletEnabled
		buyEnabled := hotWalletEnabled || buyRouteEnabled
		sendEnabled := s.mobilePairSendEnabled(pair)
		networkMeta, _ := liquidity.NetworkMetadata(pair.Network)
		receiveEnabled := networkMeta.ReceiveEnabled
		if pair.Network == "SOLANA" {
			receiveEnabled = s.solSvc != nil
		}
		if !buyEnabled && !sendEnabled && !receiveEnabled {
			continue
		}
		out = append(out, map[string]any{
			"asset":                    pair.Asset,
			"network":                  pair.Network,
			"family":                   pair.Family,
			"contract_address":         pair.ContractAddress,
			"decimals":                 pair.Decimals,
			"token_standard":           pair.TokenStandard,
			"receive_enabled":          receiveEnabled,
			"send_enabled":             networkMeta.SendEnabled && sendEnabled,
			"buy_enabled":              networkMeta.BuyEnabled && buyEnabled,
			"dca_enabled":              networkMeta.DCAEnabled && buyEnabled,
			"hot_wallet_enabled":       hotWalletEnabled,
			"liquidity_router_enabled": routerEnabled,
		})
	}

	// BTC — always included when the Bitcoin service is configured.
	// It is never in LiquidityAllowedPairs (which governs the EVM/Solana buy pipeline)
	// so it would otherwise be invisible to the frontend for Receive and Send filtering.
	if s.btcSvc != nil && !seen["BTC:BITCOIN"] {
		btcMeta, _ := liquidity.NetworkMetadata("BITCOIN")
		btcPair := liquidity.Pair{Asset: "BTC", Network: "BITCOIN", Decimals: 8}
		btcSendEnabled := s.mobilePairSendEnabled(btcPair)
		btcRouterEnabled := s.mobileBuyPairHasExecutionRoute(btcPair)
		out = append(out, map[string]any{
			"asset":                    "BTC",
			"network":                  "BITCOIN",
			"family":                   "BITCOIN",
			"contract_address":         "",
			"decimals":                 8,
			"token_standard":           "BTC",
			"receive_enabled":          true,
			"send_enabled":             btcMeta.SendEnabled && btcSendEnabled,
			"buy_enabled":              btcMeta.BuyEnabled && btcRouterEnabled,
			"dca_enabled":              btcMeta.DCAEnabled && btcRouterEnabled,
			"hot_wallet_enabled":       false,
			"liquidity_router_enabled": btcRouterEnabled,
		})
	}

	// SOL — always included when the Solana service is configured.
	if s.solSvc != nil && !seen["SOL:SOLANA"] {
		solMeta, _ := liquidity.NetworkMetadata("SOLANA")
		solPair := liquidity.Pair{Asset: "SOL", Network: "SOLANA", Decimals: 9}
		solSendEnabled := s.cfg.SolanaWithdrawalsEnabled
		solRouterEnabled := s.mobileBuyPairHasExecutionRoute(solPair)
		out = append(out, map[string]any{
			"asset":                    "SOL",
			"network":                  "SOLANA",
			"family":                   "SOLANA",
			"contract_address":         "",
			"decimals":                 9,
			"token_standard":           "NATIVE",
			"receive_enabled":          true,
			"send_enabled":             solMeta.SendEnabled && solSendEnabled,
			"buy_enabled":              solMeta.BuyEnabled && solRouterEnabled,
			"dca_enabled":              solMeta.DCAEnabled && solRouterEnabled,
			"hot_wallet_enabled":       false,
			"liquidity_router_enabled": solRouterEnabled,
		})
	}

	return out
}

func (s *Server) mobilePairIsUSDTBSC(pair liquidity.Pair) bool {
	return strings.EqualFold(pair.Asset, "USDT") && normalizeMobileBuyNetwork(pair.Network) == "BSC"
}

func (s *Server) mobileHotWalletUSDTBSCAllowed(policy liquidity.PairPolicy) bool {
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.BscUsdtContract) == "" || !s.mobileNetworkEnabled("BSC") {
		return false
	}
	if _, ok := policy.Resolve("USDT", "BSC"); ok {
		return true
	}
	if policy.Empty() {
		return containsCSVFoldMobile(s.cfg.LiquidityAllowedAssets, "USDT") &&
			containsCSVFoldMobile(s.cfg.LiquidityAllowedNetworks, "BSC")
	}
	for _, item := range splitMobilePolicyItems(s.cfg.LiquidityAllowedPairs) {
		pair, ok := liquidity.ParsePair(item)
		if ok && pair.Asset == "USDT" && pair.Network == "BSC" {
			return true
		}
	}
	return false
}

func (s *Server) hydrateAndValidateMobileLiquidityPair(pair liquidity.Pair) (liquidity.Pair, bool) {
	if s == nil || s.cfg == nil {
		return liquidity.Pair{}, false
	}
	pair.Asset = strings.ToUpper(strings.TrimSpace(pair.Asset))
	pair.Network = normalizeMobileBuyNetwork(pair.Network)
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
	if mobileLiquidityPairIsNative(pair) {
		return pair, true
	}
	if liquidity.IsEVMNetwork(pair.Network) {
		return pair, looksLikeMobileEVMAddress(pair.ContractAddress)
	}
	if pair.Network == "SOLANA" {
		return pair, solana.ValidateAddress(pair.ContractAddress) == nil
	}
	if pair.Network == "APTOS" {
		return pair, looksLikeMobileFixedHexAddress(pair.ContractAddress, 64)
	}
	return liquidity.Pair{}, false
}

func mobileLiquidityPairIsNative(pair liquidity.Pair) bool {
	return liquidity.IsNativeAsset(pair.Asset, pair.Network) ||
		(pair.Asset == "BTC" && pair.Network == "BITCOIN")
}

func (s *Server) mobileBuyPairExecutableWithoutRouter(asset, network string) bool {
	return strings.EqualFold(asset, "USDT") && normalizeMobileBuyNetwork(network) == "BSC"
}

func (s *Server) mobileBuyPairHasExecutionRoute(pair liquidity.Pair) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	pair = liquidity.EnrichPair(pair)
	if s.mobileBuyPairExecutableWithoutRouter(pair.Asset, pair.Network) {
		return true
	}
	if !s.cfg.LiquidityRouterEnabled {
		return false
	}
	if strings.EqualFold(pair.Asset, "USDT") {
		return strings.TrimSpace(s.cfg.LiquidityProviderURLs) != ""
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
	return containsCSVFoldMobile(s.cfg.BingXAllowedAssets, pair.Asset) &&
		containsCSVFoldMobile(s.cfg.BingXAllowedNetworks, pair.Network)
}

func (s *Server) mobilePairSendEnabled(pair liquidity.Pair) bool {
	if s == nil || s.cfg == nil {
		return false
	}
	pair = liquidity.EnrichPair(pair)
	if !liquidity.IsEVMNetwork(pair.Network) {
		if pair.Asset == "BTC" && pair.Network == "BITCOIN" && s.btcSvc != nil {
			btcCfg := s.btcSvc.Config()
			return btcCfg != nil && btcCfg.WithdrawalsEnabled && !btcCfg.EmergencyLockdown
		}
		return pair.Asset == "SOL" && pair.Network == "SOLANA" && s.solSvc != nil && s.cfg.SolanaWithdrawalsEnabled
	}
	if len(s.mobileTransferRPCURLs(pair.Network)) == 0 {
		return false
	}
	_, _, _, err := s.mobileTransferToken(pair.Asset, pair.Network)
	return err == nil
}

func (s *Server) mobileSupportedNetworks() []liquidity.NetworkMeta {
	if s == nil || s.cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []liquidity.NetworkMeta
	raw := s.cfg.SupportedNetworks
	if strings.TrimSpace(raw) == "" {
		raw = s.cfg.LiquidityAllowedNetworks
	}
	for _, item := range splitMobilePolicyItems(raw) {
		network := normalizeMobileBuyNetwork(item)
		if network == "" || seen[network] {
			continue
		}
		meta, ok := liquidity.NetworkMetadata(network)
		if !ok {
			continue
		}
		seen[network] = true
		out = append(out, meta)
	}
	return out
}

func (s *Server) mobileNetworkEnabled(network string) bool {
	network = normalizeMobileBuyNetwork(network)
	if network == "" {
		return false
	}
	for _, meta := range s.mobileSupportedNetworks() {
		if meta.Network == network && meta.Enabled {
			return true
		}
	}
	return false
}

func (s *Server) mobileLiquiditySupportedTokens() []string {
	seen := map[string]bool{}
	var out []string
	for _, pair := range s.mobileLiquiditySupportedPairs() {
		asset, _ := pair["asset"].(string)
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset != "" && !seen[asset] {
			seen[asset] = true
			out = append(out, asset)
		}
	}
	return out
}

func (s *Server) mobileLiquiditySupportedNetworks() []string {
	seen := map[string]bool{}
	var out []string
	for _, pair := range s.mobileLiquiditySupportedPairs() {
		network, _ := pair["network"].(string)
		network = strings.ToUpper(strings.TrimSpace(network))
		if network != "" && !seen[network] {
			seen[network] = true
			out = append(out, network)
		}
	}
	return out
}

func containsCSVFoldMobile(raw, value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, item := range splitMobilePolicyItems(raw) {
		if strings.ToUpper(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}

func looksLikeMobileEVMAddress(address string) bool {
	address = strings.TrimSpace(address)
	if !strings.HasPrefix(address, "0x") || len(address) != 42 {
		return false
	}
	for _, ch := range address[2:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

func looksLikeMobileFixedHexAddress(address string, hexLen int) bool {
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

func splitMobilePolicyItems(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}
