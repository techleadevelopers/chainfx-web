package mobile

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

const (
	brlMinorScale  int64 = 100
	usdtMicroScale int64 = 1_000_000
)

func decimalToMinor(value any, scale int64) int64 {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return 0
	}
	text = strings.ReplaceAll(text, ",", ".")
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	parts := strings.SplitN(text, ".", 2)
	whole, _ := strconv.ParseInt(onlyDigits(parts[0]), 10, 64)
	fracText := ""
	if len(parts) == 2 {
		fracText = onlyDigits(parts[1])
	}
	decimals := scaleDecimals(scale)
	if len(fracText) > decimals {
		fracText = fracText[:decimals]
	}
	for len(fracText) < decimals {
		fracText += "0"
	}
	frac, _ := strconv.ParseInt(fracText, 10, 64)
	out := whole*scale + frac
	if negative {
		return -out
	}
	return out
}

func brlMinorString(minor int64) string {
	return minorString(minor, brlMinorScale)
}

func usdtMicroString(micros int64) string {
	return minorString(micros, usdtMicroScale)
}

func minorString(value, scale int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	decimals := scaleDecimals(scale)
	whole := value / scale
	frac := value % scale
	return fmt.Sprintf("%s%d.%0*d", sign, whole, decimals, frac)
}

func scaleDecimals(scale int64) int {
	decimals := 0
	for scale > 1 {
		decimals++
		scale /= 10
	}
	return decimals
}

func onlyDigits(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "0"
	}
	return out
}

func feeMinor(amountMinor int64, feeBps int) int64 {
	if amountMinor <= 0 || feeBps <= 0 {
		return 0
	}
	return (amountMinor*int64(feeBps) + 9_999) / 10_000
}

func usdtMicrosFromBRL(totalBRLMinor, rateMicros int64) int64 {
	if totalBRLMinor <= 0 || rateMicros <= 0 {
		return 0
	}
	numerator := big.NewInt(totalBRLMinor)
	numerator.Mul(numerator, big.NewInt(10_000))
	numerator.Mul(numerator, big.NewInt(usdtMicroScale))
	denominator := big.NewInt(rateMicros)
	quotient := new(big.Int).Quo(numerator, denominator)
	remainder := new(big.Int).Rem(numerator, denominator)
	if remainder.Sign() > 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return math.MaxInt64
	}
	return quotient.Int64()
}
