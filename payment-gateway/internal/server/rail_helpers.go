package server

import (
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

func (s *Server) deliveryNetwork() string {
	network := strings.ToUpper(strings.TrimSpace(s.cfg.SignerNetwork))
	switch network {
	case "", "EVM", "BINANCE", "BEP20":
		return "BSC"
	default:
		return network
	}
}

func normalizeSellNetwork(network string) string {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "", "BSC", "BINANCE", "BEP20":
		return "BSC"
	case "POL", "POLYGON", "MATIC":
		return "POLYGON"
	default:
		return strings.ToUpper(strings.TrimSpace(network))
	}
}

func normalizeStablecoinNetwork(network string) string {
	return normalizeSellNetwork(network)
}

func stablecoinNetworkChainID(network string) int64 {
	switch normalizeStablecoinNetwork(network) {
	case "POLYGON":
		return 137
	default:
		return 56
	}
}

func stablecoinNetworkRPCEnvName(network string) string {
	switch normalizeStablecoinNetwork(network) {
	case "POLYGON":
		return "POLYGON_RPC_URLS"
	default:
		return "BSC_RPC_URLS"
	}
}

func (s *Server) stablecoinRPCURLs(network string) string {
	if s.cfg == nil {
		return ""
	}
	switch normalizeStablecoinNetwork(network) {
	case "POLYGON":
		return strings.TrimSpace(s.cfg.PolygonRpcUrls)
	default:
		return strings.TrimSpace(s.cfg.BscRpcUrls)
	}
}

func (s *Server) stablecoinPaymentNetworks() []map[string]any {
	networks := []map[string]any{{
		"chain":   "BSC",
		"chainId": 56,
		"assets":  []string{"USDT", "USDC"},
	}}
	if s.cfg != nil && strings.TrimSpace(s.cfg.PolygonRpcUrls) != "" && strings.TrimSpace(s.cfg.PolygonUsdtContract) != "" {
		networks = append(networks, map[string]any{
			"chain":   "POLYGON",
			"chainId": 137,
			"assets":  []string{"USDT", "USDC"},
		})
	}
	return networks
}

func (s *Server) supportedSellNetworks() []string {
	networks := []string{}
	if strings.TrimSpace(s.cfg.BscRpcUrls) != "" && strings.TrimSpace(s.cfg.BscUsdtContract) != "" {
		networks = append(networks, "BSC")
	}
	if strings.TrimSpace(s.cfg.PolygonRpcUrls) != "" && strings.TrimSpace(s.cfg.PolygonUsdtContract) != "" {
		networks = append(networks, "POLYGON")
	}
	if len(networks) == 0 {
		networks = append(networks, "BSC")
	}
	return networks
}

func (s *Server) sellNetworkEnabled(network string) bool {
	switch normalizeSellNetwork(network) {
	case "BSC":
		return true
	case "POLYGON":
		return strings.TrimSpace(s.cfg.PolygonRpcUrls) != "" && strings.TrimSpace(s.cfg.PolygonUsdtContract) != ""
	default:
		return false
	}
}

func (s *Server) isDeliveryAddress(address string) bool {
	address = strings.TrimSpace(address)
	switch s.deliveryNetwork() {
	case "BSC", "EVM":
		return common.IsHexAddress(address)
	default:
		return false
	}
}

func isEVMDeliveryAddress(address string) bool {
	return common.IsHexAddress(strings.TrimSpace(address))
}

func normalizePaymentRail(currency, method string, amountFiat, amountBRL, amountUSD float64) (string, string, float64) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case "card", "cartao", "cartao_credito", "credit", "creditcard", "credit-card", "visa", "master", "mastercard":
		method = "credit_card"
	}
	if currency == "" {
		currency = "BRL"
	}
	if method == "" {
		method = "pix"
	}
	if amountFiat <= 0 {
		if currency == "USD" {
			amountFiat = amountUSD
		} else {
			amountFiat = amountBRL
		}
	}
	switch {
	case currency == "BRL" && method == "pix":
		return currency, method, amountFiat
	case currency == "BRL" && method == "credit_card":
		return currency, method, amountFiat
	default:
		return "", "", 0
	}
}
