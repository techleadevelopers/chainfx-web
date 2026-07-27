package money

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	FiatScale  int64 = 100
	TokenScale int64 = 1_000_000
	RateScale  int64 = 1_000_000
)

type MoneyMinor int64
type TokenUnits int64
type RateDecimal int64

func MoneyFromFloat(value float64) MoneyMinor {
	if value <= 0 {
		return 0
	}
	return MoneyMinor(math.Round(value * float64(FiatScale)))
}

func TokenFromFloat(value float64) TokenUnits {
	if value <= 0 {
		return 0
	}
	return TokenUnits(math.Round(value * float64(TokenScale)))
}

func RateFromFloat(value float64) RateDecimal {
	if value <= 0 {
		return 0
	}
	return RateDecimal(math.Round(value * float64(RateScale)))
}

func (m MoneyMinor) Float64() float64 {
	return float64(m) / float64(FiatScale)
}

func (t TokenUnits) Float64() float64 {
	return float64(t) / float64(TokenScale)
}

func (r RateDecimal) Float64() float64 {
	return float64(r) / float64(RateScale)
}

func (m MoneyMinor) String() string {
	return fmt.Sprintf("%d.%02d", int64(m)/FiatScale, abs64(int64(m)%FiatScale))
}

func (t TokenUnits) String() string {
	return fixedString(int64(t), TokenScale, 6)
}

func (r RateDecimal) String() string {
	return trimFixedString(int64(r), RateScale, 6)
}

func FeeBps(amount MoneyMinor, bps int) MoneyMinor {
	if amount <= 0 || bps <= 0 {
		return 0
	}
	return MoneyMinor(roundDiv(int64(amount)*int64(bps), 10_000))
}

func AddBps(rate RateDecimal, bps int) RateDecimal {
	if rate <= 0 || bps <= 0 {
		return rate
	}
	return RateDecimal(roundDiv(int64(rate)*int64(10_000+bps), 10_000))
}

func SubtractBps(rate RateDecimal, bps int) RateDecimal {
	if rate <= 0 || bps <= 0 {
		return rate
	}
	if bps >= 10_000 {
		return 0
	}
	return RateDecimal(roundDiv(int64(rate)*int64(10_000-bps), 10_000))
}

func FiatFromTokens(tokens TokenUnits, rate RateDecimal) MoneyMinor {
	if tokens <= 0 || rate <= 0 {
		return 0
	}
	num := int64(tokens) * int64(rate) * FiatScale
	den := TokenScale * RateScale
	return MoneyMinor(roundDiv(num, den))
}

func TokensFromFiat(amount MoneyMinor, rate RateDecimal) TokenUnits {
	if amount <= 0 || rate <= 0 {
		return 0
	}
	num := int64(amount) * TokenScale * RateScale
	den := FiatScale * int64(rate)
	return TokenUnits(roundDiv(num, den))
}

func TokenFeeBps(amount TokenUnits, bps int) TokenUnits {
	if amount <= 0 || bps <= 0 {
		return 0
	}
	return TokenUnits(roundDiv(int64(amount)*int64(bps), 10_000))
}

func GrossForNetToken(net TokenUnits, bps int) TokenUnits {
	if net <= 0 {
		return 0
	}
	if bps <= 0 {
		return net
	}
	if bps >= 10_000 {
		return 0
	}
	return TokenUnits(roundDiv(int64(net)*10_000, int64(10_000-bps)))
}

func ParseMoney(value string) (MoneyMinor, error) {
	units, err := parseFixed(value, 2, FiatScale)
	return MoneyMinor(units), err
}

func ParseToken(value string) (TokenUnits, error) {
	units, err := parseFixed(value, 6, TokenScale)
	return TokenUnits(units), err
}

func roundDiv(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return (num - den/2) / den
}

func parseFixed(value string, decimals int, scale int64) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("amount is required")
	}
	parts := strings.SplitN(value, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > decimals {
		frac = frac[:decimals]
	}
	frac += strings.Repeat("0", decimals-len(frac))
	var fracUnits int64
	if strings.Trim(frac, "0") != "" {
		fracUnits, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, err
		}
	}
	if whole < 0 {
		return whole*scale - fracUnits, nil
	}
	return whole*scale + fracUnits, nil
}

func fixedString(value, scale int64, decimals int) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	format := "%s%d.%0" + strconv.Itoa(decimals) + "d"
	return fmt.Sprintf(format, sign, value/scale, value%scale)
}

func trimFixedString(value, scale int64, decimals int) string {
	out := fixedString(value, scale, decimals)
	out = strings.TrimRight(out, "0")
	out = strings.TrimRight(out, ".")
	if out == "-0" || out == "" {
		return "0"
	}
	return out
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
