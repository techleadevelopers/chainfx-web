package transactions

import (
	"fmt"
	"math/big"
	"strings"
)

func TokenDecimals(asset, network string) int {
	asset = normalizeAsset(asset)
	network = normalizeNetwork(network)
	switch asset + ":" + network {
	case "USDT:BSC", "USDC:BSC":
		return 18
	case "USDT:POLYGON", "USDC:POLYGON":
		return 6
	default:
		return 6
	}
}

func TokenAmountRaw(asset, network, amount string) (*big.Int, error) {
	return decimalToRaw(amount, TokenDecimals(asset, network))
}

func decimalToRaw(amount string, decimals int) (*big.Int, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return nil, fmt.Errorf("amount is required")
	}
	if strings.HasPrefix(amount, "-") {
		return nil, fmt.Errorf("amount must be positive")
	}
	parts := strings.SplitN(amount, ".", 2)
	whole := parts[0]
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > decimals {
		frac = frac[:decimals]
	}
	frac += strings.Repeat("0", decimals-len(frac))
	rawText := strings.TrimLeft(whole+frac, "0")
	if rawText == "" {
		rawText = "0"
	}
	raw, ok := new(big.Int).SetString(rawText, 10)
	if !ok || raw.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return raw, nil
}
