package workers

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"payment-gateway/internal/money"
)

const dcaCryptoDisplayDecimals = 18

type dcaQuoteSnapshot struct {
	RateBRL        string
	CryptoAmount   string
	AmountBRL      money.MoneyMinor
	FeeBRL         money.MoneyMinor
	PayoutBRL      money.MoneyMinor
	RequiredUSDT   int64
	boundaryRate   float64
	boundaryCrypto float64
}

func dcaParseBRL(value string) (money.MoneyMinor, error) {
	if err := dcaValidateMaxDecimalPlaces(value, 2); err != nil {
		return 0, err
	}
	amount, err := money.ParseMoney(value)
	if err != nil {
		return 0, err
	}
	if amount <= 0 {
		return 0, fmt.Errorf("amount_brl deve ser positivo")
	}
	return amount, nil
}

func dcaValidateMaxDecimalPlaces(value string, max int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("decimal obrigatorio")
	}
	parts := strings.SplitN(value, ".", 2)
	if len(parts) == 2 && len(strings.TrimSpace(parts[1])) > max {
		return fmt.Errorf("decimal possui mais de %d casas", max)
	}
	return nil
}

func dcaBoundaryFloat(value string) (float64, error) {
	out, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || out <= 0 || math.IsNaN(out) || math.IsInf(out, 0) {
		return 0, fmt.Errorf("decimal invalido para borda float64")
	}
	return out, nil
}

func dcaDecimalFromBoundaryFloat(value float64, decimals int) (string, error) {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("valor float64 invalido na borda")
	}
	if decimals < 0 {
		decimals = 0
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(value, 'f', decimals, 64), "0"), "."), nil
}

func dcaAddBpsDecimal(value string, bps int, decimals int) (string, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok || r.Sign() <= 0 {
		return "", fmt.Errorf("rate invalida")
	}
	if bps > 0 {
		r.Mul(r, big.NewRat(int64(10_000+bps), 10_000))
	}
	return dcaFormatRat(r, decimals, dcaRoundHalfUp), nil
}

type dcaRoundingMode int

const (
	dcaRoundDown dcaRoundingMode = iota
	dcaRoundUp
	dcaRoundHalfUp
)

func dcaCryptoFromFiat(amount money.MoneyMinor, rateBRL string) (string, error) {
	if amount <= 0 {
		return "", fmt.Errorf("amount_brl invalido")
	}
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(rateBRL))
	if !ok || rate.Sign() <= 0 {
		return "", fmt.Errorf("rate_brl invalida")
	}
	brl := big.NewRat(int64(amount), money.FiatScale)
	crypto := new(big.Rat).Quo(brl, rate)
	return dcaFormatRat(crypto, dcaCryptoDisplayDecimals, dcaRoundDown), nil
}

func dcaUSDTMicroCeil(amount money.MoneyMinor, usdtBRL string) (int64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("amount_brl invalido")
	}
	rate, ok := new(big.Rat).SetString(strings.TrimSpace(usdtBRL))
	if !ok || rate.Sign() <= 0 {
		return 0, fmt.Errorf("cotacao USDT/BRL invalida")
	}
	required := new(big.Rat).Quo(big.NewRat(int64(amount), money.FiatScale), rate)
	required.Mul(required, big.NewRat(money.TokenScale, 1))
	units := dcaRatToInt(required, dcaRoundUp)
	if units <= 0 {
		return 0, fmt.Errorf("funding DCA invalido")
	}
	return units, nil
}

func dcaFormatRat(r *big.Rat, decimals int, mode dcaRoundingMode) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(scale))
	units := dcaRatToBigInt(scaled, mode)
	sign := ""
	if units.Sign() < 0 {
		sign = "-"
		units.Abs(units)
	}
	scaleInt := int64Pow10Big(decimals)
	whole := new(big.Int).Quo(units, scaleInt)
	frac := new(big.Int).Mod(units, scaleInt)
	if decimals == 0 {
		return sign + whole.String()
	}
	fracText := frac.String()
	if len(fracText) < decimals {
		fracText = strings.Repeat("0", decimals-len(fracText)) + fracText
	}
	out := fmt.Sprintf("%s%s.%s", sign, whole.String(), fracText)
	out = strings.TrimRight(out, "0")
	out = strings.TrimRight(out, ".")
	if out == "" || out == "-0" {
		return "0"
	}
	return out
}

func dcaRatToInt(r *big.Rat, mode dcaRoundingMode) int64 {
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() == 0 {
		return q.Int64()
	}
	switch mode {
	case dcaRoundUp:
		if r.Sign() > 0 {
			q.Add(q, big.NewInt(1))
		}
	case dcaRoundHalfUp:
		absRem := new(big.Int).Abs(rem)
		absRem.Mul(absRem, big.NewInt(2))
		if absRem.Cmp(den) >= 0 {
			if r.Sign() >= 0 {
				q.Add(q, big.NewInt(1))
			} else {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	return q.Int64()
}

func dcaRatToBigInt(r *big.Rat, mode dcaRoundingMode) *big.Int {
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() == 0 {
		return q
	}
	switch mode {
	case dcaRoundUp:
		if r.Sign() > 0 {
			q.Add(q, big.NewInt(1))
		}
	case dcaRoundHalfUp:
		absRem := new(big.Int).Abs(rem)
		absRem.Mul(absRem, big.NewInt(2))
		if absRem.Cmp(den) >= 0 {
			if r.Sign() >= 0 {
				q.Add(q, big.NewInt(1))
			} else {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	return q
}

func int64Pow10(decimals int) int64 {
	out := int64(1)
	for i := 0; i < decimals; i++ {
		out *= 10
	}
	return out
}

func int64Pow10Big(decimals int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
}
