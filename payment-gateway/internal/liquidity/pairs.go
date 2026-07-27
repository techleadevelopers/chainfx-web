package liquidity

import (
	"strings"
)

type Pair struct {
	Asset           string `json:"asset"`
	Network         string `json:"network"`
	ContractAddress string `json:"contract_address"`
	Decimals        int    `json:"decimals"`
	Family          string `json:"family"`
	TokenStandard   string `json:"token_standard"`
}

type PairPolicy struct {
	pairs map[string]Pair
}

func NewPairPolicy(raw string) PairPolicy {
	policy := PairPolicy{pairs: map[string]Pair{}}
	for _, item := range splitPolicyItems(raw) {
		pair, ok := ParsePair(item)
		if !ok {
			continue
		}
		policy.pairs[pairKey(pair.Asset, pair.Network)] = pair
	}
	return policy
}

func ParsePair(raw string) (Pair, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 {
		return Pair{}, false
	}
	pair := Pair{
		Asset:           strings.ToUpper(cleanPairPart(parts[0])),
		Network:         normalizeNetwork(cleanPairPart(parts[1])),
		ContractAddress: "",
		Decimals:        0,
	}
	if pair.Asset == "" || pair.Network == "" {
		return Pair{}, false
	}
	if len(parts) >= 3 {
		pair.ContractAddress = cleanPairPart(parts[2])
	}
	if len(parts) >= 4 {
		pair.Decimals = atoiDefault(cleanPairPart(parts[3]), pair.Decimals)
	}
	pair = EnrichPair(pair)
	return pair, true
}

func cleanPairPart(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

func (p PairPolicy) Empty() bool {
	return len(p.pairs) == 0
}

func (p PairPolicy) Allows(asset, network string) bool {
	_, ok := p.Resolve(asset, network)
	return ok
}

func (p PairPolicy) Resolve(asset, network string) (Pair, bool) {
	if len(p.pairs) == 0 {
		return Pair{}, false
	}
	pair, ok := p.pairs[pairKey(asset, network)]
	return pair, ok
}

func (p PairPolicy) Pairs() []Pair {
	out := make([]Pair, 0, len(p.pairs))
	for _, pair := range p.pairs {
		out = append(out, pair)
	}
	return out
}

func pairKey(asset, network string) string {
	return strings.ToUpper(strings.TrimSpace(asset)) + ":" + normalizeNetwork(network)
}

func splitPolicyItems(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

func atoiDefault(raw string, fallback int) int {
	n := 0
	for _, ch := range strings.TrimSpace(raw) {
		if ch < '0' || ch > '9' {
			return fallback
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return fallback
	}
	return n
}

type NetworkMeta struct {
	Network        string `json:"network"`
	Family         string `json:"family"`
	ChainID        string `json:"chain_id,omitempty"`
	NativeAsset    string `json:"native_asset"`
	ExplorerURL    string `json:"explorer_url,omitempty"`
	AddressFormat  string `json:"address_format"`
	Enabled        bool   `json:"enabled"`
	ReceiveEnabled bool   `json:"receive_enabled"`
	SendEnabled    bool   `json:"send_enabled"`
	BuyEnabled     bool   `json:"buy_enabled"`
	DCAEnabled     bool   `json:"dca_enabled"`
}

func NetworkMetadata(network string) (NetworkMeta, bool) {
	switch NormalizeNetwork(network) {
	case "BSC":
		return evmNetwork("BSC", "56", "BNB", "https://bscscan.com"), true
	case "POLYGON":
		return evmNetwork("POLYGON", "137", "POL", "https://polygonscan.com"), true
	case "BASE":
		return evmNetwork("BASE", "8453", "ETH", "https://basescan.org"), true
	case "ARBITRUM":
		return evmNetwork("ARBITRUM", "42161", "ETH", "https://arbiscan.io"), true
	case "ETHEREUM":
		return evmNetwork("ETHEREUM", "1", "ETH", "https://etherscan.io"), true
	case "BITCOIN":
		return NetworkMeta{Network: "BITCOIN", Family: "BITCOIN", ChainID: "mainnet", NativeAsset: "BTC", ExplorerURL: "https://mempool.space", AddressFormat: "BITCOIN_BECH32", Enabled: true, ReceiveEnabled: true, SendEnabled: true, BuyEnabled: true, DCAEnabled: true}, true
	case "SOLANA":
		return NetworkMeta{Network: "SOLANA", Family: "SOLANA", ChainID: "mainnet", NativeAsset: "SOL", ExplorerURL: "https://solscan.io", AddressFormat: "SOLANA_BASE58", Enabled: true, ReceiveEnabled: true, SendEnabled: true, BuyEnabled: true, DCAEnabled: true}, true
	case "APTOS":
		return NetworkMeta{Network: "APTOS", Family: "APTOS", ChainID: "mainnet", NativeAsset: "APT", ExplorerURL: "https://explorer.aptoslabs.com", AddressFormat: "APTOS_0X32", Enabled: true, ReceiveEnabled: true, SendEnabled: true, BuyEnabled: true, DCAEnabled: true}, true
	default:
		return NetworkMeta{}, false
	}
}

func evmNetwork(network, chainID, nativeAsset, explorerURL string) NetworkMeta {
	return NetworkMeta{Network: network, Family: "EVM", ChainID: chainID, NativeAsset: nativeAsset, ExplorerURL: explorerURL, AddressFormat: "EVM_0X", Enabled: true, ReceiveEnabled: true, SendEnabled: true, BuyEnabled: true, DCAEnabled: true}
}

func EnrichPair(pair Pair) Pair {
	pair.Asset = strings.ToUpper(strings.TrimSpace(pair.Asset))
	pair.Network = NormalizeNetwork(pair.Network)
	pair.ContractAddress = strings.TrimSpace(pair.ContractAddress)
	if pair.Decimals <= 0 {
		pair.Decimals = DefaultDecimals(pair.Asset, pair.Network)
	}
	if meta, ok := NetworkMetadata(pair.Network); ok {
		pair.Family = meta.Family
	}
	pair.TokenStandard = TokenStandard(pair.Asset, pair.Network, pair.ContractAddress)
	return pair
}

func DefaultDecimals(asset, network string) int {
	switch strings.ToUpper(strings.TrimSpace(asset)) + ":" + NormalizeNetwork(network) {
	case "BTC:BITCOIN":
		return 8
	case "SOL:SOLANA":
		return 9
	case "APT:APTOS":
		return 8
	case "USDT:POLYGON", "USDC:POLYGON", "USDT:SOLANA", "USDC:SOLANA", "USDC:BASE", "USDC:ARBITRUM", "USDC:ETHEREUM":
		return 6
	default:
		return 18
	}
}

func TokenStandard(asset, network, contractAddress string) string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = NormalizeNetwork(network)
	if asset == "BTC" && network == "BITCOIN" {
		return "BTC"
	}
	if IsNativeAsset(asset, network) {
		return "NATIVE"
	}
	switch family, _ := Family(network); family {
	case "EVM":
		return "ERC20"
	case "SOLANA":
		if strings.TrimSpace(contractAddress) == "" {
			return "NATIVE"
		}
		return "SPL"
	case "APTOS":
		if strings.TrimSpace(contractAddress) == "" {
			return "NATIVE"
		}
		return "APTOS_FA"
	default:
		return ""
	}
}

func Family(network string) (string, bool) {
	meta, ok := NetworkMetadata(network)
	return meta.Family, ok
}

func IsEVMNetwork(network string) bool {
	family, ok := Family(network)
	return ok && family == "EVM"
}

func IsNativeAsset(asset, network string) bool {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	meta, ok := NetworkMetadata(network)
	return ok && asset == meta.NativeAsset
}
