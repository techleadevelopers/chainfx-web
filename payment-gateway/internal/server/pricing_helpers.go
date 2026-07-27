package server

import (
	"fmt"
	"math"
	"strings"

	"payment-gateway/internal/money"
)

func (s *Server) transactionFee(amountFiat float64, fiatCurrency string, rate float64) float64 {
	return s.transactionFeeMinor(money.MoneyFromFloat(amountFiat), fiatCurrency, money.RateFromFloat(rate)).Float64()
}

func (s *Server) transactionFeeMinor(amountFiat money.MoneyMinor, fiatCurrency string, rate money.RateDecimal) money.MoneyMinor {
	if strings.EqualFold(fiatCurrency, "BRL") && s.cfg.BuyTier1Bps+s.cfg.BuyTier2Bps+s.cfg.BuyTier3Bps > 0 {
		_, _, _, _, totalFee, _ := s.buyFeeBreakdownMinor(amountFiat)
		return totalFee
	}
	percentFee := money.FeeBps(amountFiat, s.cfg.FeeBps)
	fixedFee := money.MoneyFromFloat(s.cfg.FeeFixedUsd)
	perUSDTFee := money.FiatFromTokens(money.TokensFromFiat(amountFiat, rate), money.RateFromFloat(s.cfg.FeePerUsdtUsd))
	if strings.EqualFold(fiatCurrency, "BRL") {
		fixedFee = money.FiatFromTokens(money.TokenFromFloat(s.cfg.FeeFixedUsd), rate)
		perUSDTFee = money.MoneyMinor(roundDivInt64(int64(amountFiat)*int64(money.RateFromFloat(s.cfg.FeePerUsdtUsd)), money.RateScale))
	}
	fee := percentFee + fixedFee + perUSDTFee
	minFee := money.MoneyFromFloat(s.cfg.FeeMinBrl)
	if strings.EqualFold(fiatCurrency, "BRL") && minFee > fee {
		fee = minFee
	}
	return fee
}

func (s *Server) buyFeeBreakdown(amountBRL float64) buyFeeBreakdown {
	tier, bps, serviceFee, networkFee, totalFee, minFee := s.buyFeeBreakdownMinor(money.MoneyFromFloat(amountBRL))
	return buyFeeBreakdown{
		Tier:          tier,
		ServiceBps:    bps,
		ServiceFee:    serviceFee.Float64(),
		NetworkFee:    networkFee.Float64(),
		MinFee:        minFee.Float64(),
		TotalFee:      totalFee.Float64(),
		RateSpreadBps: s.cfg.BuyRateSpreadBps,
	}
}

type buyQuotePricing struct {
	Rate           float64
	MarketRate     float64
	FeeFiat        float64
	TotalFiat      float64
	PayoutFiat     float64
	CryptoAmount   float64
	ReceiveAmount  float64
	TotalMargin    float64
	EmbeddedSpread float64
	FeeBreakdown   buyFeeBreakdown
	ProviderFee    buyProviderWithdrawalFee
}

type buyProviderWithdrawalFee struct {
	Applies        bool
	Provider       string
	Asset          string
	Network        string
	MinBuyFiat     float64
	FeeAmount      float64
	MinAmount      float64
	GrossAmount    float64
	MinGross       float64
	FeeFiat        float64
	ChargedFeeFiat float64
	MinGrossFiat   float64
	ReceiveAmount  float64
}

func (f buyProviderWithdrawalFee) mapValue() map[string]any {
	if !f.Applies {
		return nil
	}
	return map[string]any{
		"provider":              f.Provider,
		"asset":                 f.Asset,
		"network":               f.Network,
		"min_buy_fiat":          f.MinBuyFiat,
		"min_buy_brl":           f.MinBuyFiat,
		"fee_amount":            f.FeeAmount,
		"fee_asset":             f.Asset,
		"min_amount":            f.MinAmount,
		"min_asset":             f.Asset,
		"gross_amount":          f.GrossAmount,
		"withdraw_gross_amount": f.GrossAmount,
		"min_gross_amount":      f.MinGross,
		"fee_fiat":              f.FeeFiat,
		"charged_fee_fiat":      f.ChargedFeeFiat,
		"charged_fee_brl":       f.ChargedFeeFiat,
		"min_gross_fiat":        f.MinGrossFiat,
		"receive_amount":        f.ReceiveAmount,
		"deducted_from_send":    true,
	}
}

func (s *Server) buyProviderWithdrawalFee(asset, network string, rate float64, receiveAmount float64) buyProviderWithdrawalFee {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeBuyDeliveryNetwork(network)
	if asset != "BTC" || network != "BITCOIN" {
		return buyProviderWithdrawalFee{ReceiveAmount: receiveAmount, GrossAmount: receiveAmount}
	}
	fee := 0.00005
	minAmount := 0.000056
	minBuyFiat := 20.0
	chargedFeeFiat := 20.0
	if s != nil && s.cfg != nil {
		minBuyFiat = s.cfg.BuyBTCBitcoinMinBrl
		if s.cfg.BuyBTCBitcoinNetworkFeeBrl > 0 {
			chargedFeeFiat = s.cfg.BuyBTCBitcoinNetworkFeeBrl
		}
		if s.cfg.BingXBTCWithdrawFeeBTC > 0 {
			fee = s.cfg.BingXBTCWithdrawFeeBTC
		}
		if s.cfg.BingXBTCWithdrawMinBTC > 0 {
			minAmount = s.cfg.BingXBTCWithdrawMinBTC
		}
	}
	receive := roundCryptoAmount(receiveAmount)
	if receive < 0 {
		receive = 0
	}
	feeFiat := round2(fee * rate)
	if chargedFeeFiat < feeFiat {
		chargedFeeFiat = feeFiat
	}
	withdrawGross := roundCryptoAmount(receive + fee)
	minGross := roundCryptoAmount(fee + minAmount)
	return buyProviderWithdrawalFee{
		Applies:        true,
		Provider:       "bingx",
		Asset:          asset,
		Network:        network,
		MinBuyFiat:     minBuyFiat,
		FeeAmount:      fee,
		MinAmount:      minAmount,
		GrossAmount:    withdrawGross,
		MinGross:       minGross,
		FeeFiat:        feeFiat,
		ChargedFeeFiat: chargedFeeFiat,
		MinGrossFiat:   round2(minGross * rate),
		ReceiveAmount:  receive,
	}
}

func (s *Server) validateBuyProviderWithdrawal(asset, network string, amountFiat float64, rate float64, grossAmount float64) (buyProviderWithdrawalFee, error) {
	fee := s.buyProviderWithdrawalFee(asset, network, rate, grossAmount)
	if !fee.Applies {
		return fee, nil
	}
	if fee.MinBuyFiat > 0 && amountFiat < fee.MinBuyFiat {
		return fee, fmt.Errorf("compra minima de BTC na rede Bitcoin: %.2f BRL. Para valores menores, use USDT BEP-20, USDT Polygon ou outra rede com taxa baixa", fee.MinBuyFiat)
	}
	if fee.ReceiveAmount < fee.MinAmount || fee.ReceiveAmount <= 0 {
		return fee, fmt.Errorf("valor minimo para BTC na rede Bitcoin: %.8f BTC bruto (%.8f BTC taxa BingX + %.8f BTC minimo de retirada), cerca de %.2f BRL antes da taxa Pix", fee.MinGross, fee.FeeAmount, fee.MinAmount, fee.MinGrossFiat)
	}
	return fee, nil
}

func (s *Server) buyQuotePricing(amountFiat float64, fiatCurrency string, rate float64, marketRate float64, asset string, network string) buyQuotePricing {
	baseMargin := s.transactionFee(amountFiat, fiatCurrency, rate)
	visibleFee := baseMargin
	if strings.EqualFold(fiatCurrency, "BRL") {
		visibleFee = s.buyVisibleFee(amountFiat, baseMargin)
	}
	embeddedSpread := round2(baseMargin - visibleFee)
	quoteFiat := amountFiat - embeddedSpread
	if quoteFiat <= 0 {
		quoteFiat = amountFiat
		embeddedSpread = 0
	}
	cryptoAmount := quoteFiat / rate
	displayRate := rate
	if cryptoAmount > 0 {
		displayRate = roundRate(amountFiat / cryptoAmount)
	}
	providerFee := s.buyProviderWithdrawalFee(asset, network, rate, cryptoAmount)
	receiveAmount := cryptoAmount
	if providerFee.Applies {
		receiveAmount = providerFee.ReceiveAmount
		visibleFee = round2(visibleFee + providerFee.ChargedFeeFiat)
	}
	totalMargin := round2(baseMargin + providerFee.ChargedFeeFiat)
	breakdown := s.buyFeeBreakdown(amountFiat)
	breakdown.DisplayFee = visibleFee
	breakdown.EmbeddedSpread = embeddedSpread
	breakdown.TotalMargin = totalMargin
	return buyQuotePricing{
		Rate:           displayRate,
		MarketRate:     marketRate,
		FeeFiat:        visibleFee,
		TotalFiat:      round2(amountFiat + visibleFee),
		PayoutFiat:     amountFiat,
		CryptoAmount:   cryptoAmount,
		ReceiveAmount:  receiveAmount,
		TotalMargin:    totalMargin,
		EmbeddedSpread: embeddedSpread,
		FeeBreakdown:   breakdown,
		ProviderFee:    providerFee,
	}
}

func (s *Server) buyVisibleFee(amountBRL, totalMarginBRL float64) float64 {
	if totalMarginBRL <= 0 {
		return 0
	}
	fee := math.Ceil((amountBRL*0.0185+1.99)*100) / 100
	if fee < 4.99 {
		fee = 4.99
	}
	if fee > totalMarginBRL {
		return round2(totalMarginBRL)
	}
	return fee
}

func (s *Server) buyFeeBreakdownMinor(amountBRL money.MoneyMinor) (string, int, money.MoneyMinor, money.MoneyMinor, money.MoneyMinor, money.MoneyMinor) {
	bps := s.cfg.BuyTier3Bps
	tier := "tier3"
	switch {
	case amountBRL < money.MoneyFromFloat(s.cfg.BuyTier1MaxBrl):
		bps = s.cfg.BuyTier1Bps
		tier = "tier1"
	case amountBRL < money.MoneyFromFloat(s.cfg.BuyTier2MaxBrl):
		bps = s.cfg.BuyTier2Bps
		tier = "tier2"
	}
	serviceFee := money.FeeBps(amountBRL, bps)
	networkFee := money.MoneyFromFloat(s.cfg.BuyNetworkFeeBrl)
	totalFee := serviceFee + networkFee
	minFee := money.MoneyFromFloat(s.cfg.BuyMinFeeBrl)
	if totalFee < minFee {
		totalFee = minFee
	}
	return tier, bps, serviceFee, networkFee, totalFee, minFee
}

func (s *Server) buyMinBRL() float64 {
	if s.cfg.BuyTier1MinBrl > s.cfg.OrderMinBrl {
		return s.cfg.BuyTier1MinBrl
	}
	return s.cfg.OrderMinBrl
}

func (s *Server) buyRate(marketRate float64) float64 {
	spreadBps := s.cfg.BuyRateSpreadBps
	if spreadBps < 0 {
		spreadBps = 0
	}
	return roundRate(money.AddBps(money.RateFromFloat(marketRate), spreadBps).Float64())
}

func buyAssetSupported(asset string) bool {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "USDT", "BTC", "BNB", "ETH", "SOL", "LINK", "AVAX":
		return true
	default:
		return false
	}
}

func (s *Server) buyAssetMarketRate(fiatCurrency, asset string) float64 {
	if s == nil || s.workers == nil || s.workers.PriceWorker == nil {
		return 0
	}
	fiatCurrency = strings.ToUpper(strings.TrimSpace(defaultString(fiatCurrency, "BRL")))
	asset = strings.ToUpper(strings.TrimSpace(defaultString(asset, "USDT")))
	if fiatCurrency != "BRL" {
		return s.workers.PriceWorker.GetPrice(fiatCurrency)
	}
	usdtBRL := s.workers.PriceWorker.GetPrice("BRL")
	switch asset {
	case "USDT":
		return usdtBRL
	case "BTC":
		btcUSD := s.workers.PriceWorker.GetPrice("BTCUSDT_SOURCE")
		if btcUSD <= 0 {
			btcUSD = s.workers.PriceWorker.GetPrice("BTCUSDT")
		}
		if btcUSD > 0 && usdtBRL > 0 {
			return btcUSD * usdtBRL
		}
	case "BNB":
		bnbUSD := s.workers.PriceWorker.GetPrice("BNBUSDT_SOURCE")
		if bnbUSD <= 0 {
			bnbUSD = s.workers.PriceWorker.GetPrice("BNBUSDT")
		}
		if bnbUSD > 0 && usdtBRL > 0 {
			return bnbUSD * usdtBRL
		}
	case "ETH":
		ethUSD := s.workers.PriceWorker.GetPrice("ETHUSDT_SOURCE")
		if ethUSD <= 0 {
			ethUSD = s.workers.PriceWorker.GetPrice("ETHUSDT")
		}
		if ethUSD > 0 && usdtBRL > 0 {
			return ethUSD * usdtBRL
		}
	case "SOL":
		solUSD := s.workers.PriceWorker.GetPrice("SOLUSDT_SOURCE")
		if solUSD <= 0 {
			solUSD = s.workers.PriceWorker.GetPrice("SOLUSDT")
		}
		if solUSD > 0 && usdtBRL > 0 {
			return solUSD * usdtBRL
		}
	case "LINK":
		linkUSD := s.workers.PriceWorker.GetPrice("LINKUSDT_SOURCE")
		if linkUSD <= 0 {
			linkUSD = s.workers.PriceWorker.GetPrice("LINKUSDT")
		}
		if linkUSD > 0 && usdtBRL > 0 {
			return linkUSD * usdtBRL
		}
	case "AVAX":
		avaxUSD := s.workers.PriceWorker.GetPrice("AVAXUSDT_SOURCE")
		if avaxUSD <= 0 {
			avaxUSD = s.workers.PriceWorker.GetPrice("AVAXUSDT")
		}
		if avaxUSD > 0 && usdtBRL > 0 {
			return avaxUSD * usdtBRL
		}
	}
	return 0
}

func (s *Server) feePolicy(fiatCurrency string, rate float64) map[string]any {
	fixedFiat := s.cfg.FeeFixedUsd
	perUsdtFiat := s.cfg.FeePerUsdtUsd
	if strings.EqualFold(fiatCurrency, "BRL") {
		fixedFiat = s.cfg.FeeFixedUsd * rate
		perUsdtFiat = s.cfg.FeePerUsdtUsd * rate
	}
	return map[string]any{
		"bps":                     s.cfg.FeeBps,
		"percent":                 float64(s.cfg.FeeBps) / 100,
		"fixedUsd":                s.cfg.FeeFixedUsd,
		"fixedFiat":               fixedFiat,
		"perUsdtUsd":              s.cfg.FeePerUsdtUsd,
		"perUsdtFiat":             perUsdtFiat,
		"buyMinBRL":               s.buyMinBRL(),
		"buyTier1Bps":             s.cfg.BuyTier1Bps,
		"buyTier1MaxBRL":          s.cfg.BuyTier1MaxBrl,
		"buyTier2Bps":             s.cfg.BuyTier2Bps,
		"buyTier2MaxBRL":          s.cfg.BuyTier2MaxBrl,
		"buyTier3Bps":             s.cfg.BuyTier3Bps,
		"networkFeeBRL":           s.cfg.BuyNetworkFeeBrl,
		"minFeeBRL":               s.cfg.BuyMinFeeBrl,
		"btcBitcoinMinBRL":        s.cfg.BuyBTCBitcoinMinBrl,
		"btcBitcoinNetworkFeeBRL": s.cfg.BuyBTCBitcoinNetworkFeeBrl,
		"rateSpreadBps":           s.cfg.BuyRateSpreadBps,
		"fiatCurrency":            strings.ToUpper(fiatCurrency),
		"description":             "Tiered BUY fee + network fee + minimum fee + rate spread",
		"backendEnforced":         true,
	}
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

func roundRate(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func roundCryptoAmount(value float64) float64 {
	return math.Round(value*1e8) / 1e8
}

func buyLowFeeAlternatives() []map[string]string {
	return []map[string]string{
		{"asset": "USDT", "network": "BSC", "label": "USDT (BEP-20)"},
		{"asset": "USDT", "network": "POLYGON", "label": "USDT (Polygon)"},
		{"asset": "USDC", "network": "BASE", "label": "USDC (Base)"},
		{"asset": "SOL", "network": "SOLANA", "label": "Solana"},
	}
}

func roundDivInt64(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	if num >= 0 {
		return (num + den/2) / den
	}
	return (num - den/2) / den
}

func (s *Server) sellRate(marketRate float64) float64 {
	if s.cfg.SellUsdtBrlRate > 0 {
		return roundRate(s.cfg.SellUsdtBrlRate)
	}
	if s.cfg.SellRateBps > 0 {
		bps := s.cfg.SellRateBps
		if bps > 10000 {
			bps = 10000
		}
		return roundRate(marketRate * float64(bps) / 10000)
	}
	return s.sellRateForAmount(0, marketRate)
}

func (s *Server) sellRateForAmount(amountUSDT, marketRate float64) float64 {
	spreadBps := s.sellSpreadBps(amountUSDT, marketRate)
	return roundRate(money.SubtractBps(money.RateFromFloat(marketRate), spreadBps).Float64())
}

func (s *Server) sellSpreadBps(amountUSDT, marketRate float64) int {
	if s.cfg.SellUsdtBrlRate > 0 && marketRate > 0 {
		spread := int(math.Round((1 - s.cfg.SellUsdtBrlRate/marketRate) * 10000))
		if spread < 0 {
			return 0
		}
		return spread
	}
	if s.cfg.SellRateBps > 0 {
		spread := 10000 - s.cfg.SellRateBps
		if spread < 0 {
			return 0
		}
		return spread
	}
	minBps := s.cfg.SellSpreadMinBps
	maxBps := s.cfg.SellSpreadMaxBps
	if minBps < 0 {
		minBps = 0
	}
	if maxBps < minBps {
		maxBps = minBps
	}
	marketValue := amountUSDT * marketRate
	if s.cfg.SellSpreadHighValueBrl > 0 && marketValue >= s.cfg.SellSpreadHighValueBrl {
		return minBps
	}
	return maxBps
}

func (s *Server) sellQuote(amountUSDT, marketRate float64) (sellRate, payoutBRL, spreadBRL float64) {
	sellRateDecimal, payout, spread := s.sellQuoteUnits(money.TokenFromFloat(amountUSDT), money.RateFromFloat(marketRate))
	return roundRate(sellRateDecimal.Float64()), payout.Float64(), spread.Float64()
}

func (s *Server) sellQuoteUnits(amount money.TokenUnits, marketRate money.RateDecimal) (money.RateDecimal, money.MoneyMinor, money.MoneyMinor) {
	sellRate := money.RateFromFloat(s.sellRateForAmount(amount.Float64(), marketRate.Float64()))
	payout := money.FiatFromTokens(amount, sellRate)
	marketValue := money.FiatFromTokens(amount, marketRate)
	spread := money.MoneyMinor(0)
	if marketValue > payout {
		spread = marketValue - payout
	}
	return sellRate, payout, spread
}

func (s *Server) sellPolicy(marketRate, sellRate float64) map[string]any {
	spreadBps := 0
	if marketRate > 0 && sellRate > 0 && sellRate < marketRate {
		spreadBps = int(math.Round((1 - sellRate/marketRate) * 10000))
	}
	return map[string]any{
		"marketRate":       roundRate(marketRate),
		"rate":             sellRate,
		"sellRateBps":      s.cfg.SellRateBps,
		"spreadBps":        spreadBps,
		"fixedSellRateBRL": s.cfg.SellUsdtBrlRate > 0,
		"fiatCurrency":     "BRL",
		"description":      "Cotacao de venda USDT para PIX BRL",
		"backendEnforced":  true,
	}
}
