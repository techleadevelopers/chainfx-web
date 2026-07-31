package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/money"
)

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	markLegacyRoute(w, r, "/quote")
	var amountBRL, amountUSD, amountFiat float64
	mode := "buy"
	asset := "USDT"
	network := ""
	fiatCurrency := "BRL"
	paymentMethod := "pix"
	if r.Method == http.MethodGet {
		amountBRL, _ = strconv.ParseFloat(r.URL.Query().Get("amountBRL"), 64)
		amountUSD, _ = strconv.ParseFloat(r.URL.Query().Get("amountUSD"), 64)
		amountFiat, _ = strconv.ParseFloat(r.URL.Query().Get("amountFiat"), 64)
		mode = defaultString(r.URL.Query().Get("mode"), mode)
		asset = defaultString(r.URL.Query().Get("asset"), asset)
		network = r.URL.Query().Get("network")
		fiatCurrency = defaultString(r.URL.Query().Get("fiatCurrency"), fiatCurrency)
		paymentMethod = defaultString(r.URL.Query().Get("paymentMethod"), paymentMethod)
	} else {
		var req struct {
			AmountBRL     float64 `json:"amountBRL"`
			AmountUSD     float64 `json:"amountUSD"`
			AmountFiat    float64 `json:"amountFiat"`
			FiatCurrency  string  `json:"fiatCurrency"`
			PaymentMethod string  `json:"paymentMethod"`
			Mode          string  `json:"mode"`
			Asset         string  `json:"asset"`
			Network       string  `json:"network"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
			return
		}
		amountBRL = req.AmountBRL
		amountUSD = req.AmountUSD
		amountFiat = req.AmountFiat
		mode = defaultString(req.Mode, mode)
		asset = defaultString(req.Asset, asset)
		network = req.Network
		fiatCurrency = defaultString(req.FiatCurrency, fiatCurrency)
		paymentMethod = defaultString(req.PaymentMethod, paymentMethod)
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if mode == "sell" {
		network = normalizeSellNetwork(defaultString(network, "BSC"))
	} else {
		network = normalizeBuyDeliveryNetwork(defaultString(network, s.deliveryNetwork()))
	}
	if mode != "buy" && mode != "sell" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "modo invalido"})
		return
	}
	if mode == "sell" && asset != "USDT" && asset != "BTC" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset nao suportado para venda nesta fase"})
		return
	}
	if mode == "sell" && !sellAssetNetworkAllowed(asset, network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rede invalida para asset de sell", "asset": asset, "network": network})
		return
	}
	if mode == "buy" && !s.buyLiquidityPairSupported(asset, network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "par asset/network nao suportado para compra"})
		return
	}
	fiatCurrency, paymentMethod, amountFiat = normalizePaymentRail(fiatCurrency, paymentMethod, amountFiat, amountBRL, amountUSD)
	if mode == "sell" {
		fiatCurrency, paymentMethod = "BRL", "pix"
	}
	if fiatCurrency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rail de pagamento nao suportado"})
		return
	}
	if amountFiat <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amountFiat deve ser maior que zero"})
		return
	}
	if mode != "sell" && fiatCurrency == "BRL" && (amountFiat < s.buyMinBRL() || amountFiat > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor fora dos limites (%.2f - %.2f BRL)", s.buyMinBRL(), s.cfg.OrderMaxBrl)})
		return
	}
	marketRate := s.workers.PriceWorker.GetPrice(fiatCurrency)
	if mode == "buy" {
		marketRate = s.buyAssetMarketRate(fiatCurrency, asset)
	} else if asset == "BTC" {
		marketRate = s.buyAssetMarketRate("BRL", "BTC")
	}
	if marketRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao ainda nao carregada"})
		return
	}
	if mode == "sell" {
		cryptoAmount := amountFiat
		rate, payoutBRL, spreadBRL := s.sellQuoteForAsset(asset, cryptoAmount, marketRate)
		if payoutBRL < s.cfg.OrderMinBrl || payoutBRL > s.cfg.OrderMaxBrl {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("payout fora dos limites (%.2f - %.2f BRL)", s.cfg.OrderMinBrl, s.cfg.OrderMaxBrl)})
			return
		}
		expiresAt := time.Now().Add(time.Duration(s.cfg.RateLockSec) * time.Second).UTC()
		quoteID, persisted, err := s.persistPublicQuote(r, publicQuoteInput{
			Side:          mode,
			Asset:         asset,
			Network:       network,
			FiatCurrency:  "BRL",
			PaymentMethod: paymentMethod,
			AmountFiat:    amountFiat,
			CryptoAmount:  cryptoAmount,
			Rate:          rate,
			MarketRate:    marketRate,
			FeeFiat:       spreadBRL,
			ExpiresAt:     expiresAt,
		})
		if err != nil {
			writeAPIError(w, r, http.StatusServiceUnavailable, "QUOTE_PERSISTENCE_UNAVAILABLE", "Quote persistence unavailable.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"quoteId":           quoteID,
			"quotePersisted":    persisted,
			"quoteLockContract": "quoteId+side+asset+network+fiatCurrency+paymentMethod+amountFiat",
			"mode":              mode,
			"asset":             asset,
			"network":           network,
			"amountFiat":        payoutBRL,
			"subtotalFiat":      payoutBRL,
			"fiatCurrency":      "BRL",
			"paymentMethod":     paymentMethod,
			"feeFiat":           spreadBRL,
			"spreadFiat":        spreadBRL,
			"totalFiat":         payoutBRL,
			"payoutFiat":        payoutBRL,
			"sellPolicy":        s.sellPolicy(marketRate, rate),
			"rate":              rate,
			"marketRate":        roundRate(marketRate),
			"cryptoAmount":      cryptoAmount,
			"rateLockExpiresAt": expiresAt,
		})
		return
	}
	rate := s.buyRate(marketRate)
	pricing := s.buyQuotePricing(amountFiat, fiatCurrency, rate, marketRate, asset, network)
	if pricing.PayoutFiat <= 0 || pricing.CryptoAmount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valor insuficiente apos taxa"})
		return
	}
	if _, err := s.validateBuyProviderWithdrawal(asset, network, amountFiat, rate, pricing.CryptoAmount); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":                     err.Error(),
			"code":                      "PROVIDER_WITHDRAW_MINIMUM",
			"providerWithdrawalFee":     pricing.ProviderFee.mapValue(),
			"provider_withdrawal_fee":   pricing.ProviderFee.mapValue(),
			"minProviderSubtotalFiat":   pricing.ProviderFee.MinGrossFiat,
			"min_provider_subtotal_brl": pricing.ProviderFee.MinGrossFiat,
			"suggestedAlternatives":     buyLowFeeAlternatives(),
			"suggested_alternatives":    buyLowFeeAlternatives(),
		})
		return
	}
	expiresAt := time.Now().Add(time.Duration(s.cfg.RateLockSec) * time.Second).UTC()
	quoteID, persisted, err := s.persistPublicQuote(r, publicQuoteInput{
		Side:          mode,
		Asset:         asset,
		Network:       network,
		FiatCurrency:  fiatCurrency,
		PaymentMethod: paymentMethod,
		AmountFiat:    amountFiat,
		CryptoAmount:  pricing.CryptoAmount,
		Rate:          pricing.Rate,
		MarketRate:    marketRate,
		FeeFiat:       pricing.FeeFiat,
		ExpiresAt:     expiresAt,
	})
	if err != nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "QUOTE_PERSISTENCE_UNAVAILABLE", "Quote persistence unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"quoteId":                 quoteID,
		"quotePersisted":          persisted,
		"quoteLockContract":       "quoteId+side+asset+network+fiatCurrency+paymentMethod+amountFiat",
		"mode":                    mode,
		"asset":                   asset,
		"network":                 network,
		"amountFiat":              pricing.TotalFiat,
		"subtotalFiat":            amountFiat,
		"fiatCurrency":            fiatCurrency,
		"paymentMethod":           paymentMethod,
		"feeFiat":                 pricing.FeeFiat,
		"totalFiat":               pricing.TotalFiat,
		"payoutFiat":              pricing.PayoutFiat,
		"feePolicy":               s.feePolicy(fiatCurrency, pricing.Rate),
		"feeBreakdown":            pricing.FeeBreakdown,
		"rate":                    pricing.Rate,
		"marketRate":              roundRate(marketRate),
		"cryptoAmount":            pricing.CryptoAmount,
		"grossCryptoAmount":       pricing.ProviderFee.GrossAmount,
		"withdrawGrossAmount":     pricing.ProviderFee.GrossAmount,
		"withdraw_gross_amount":   pricing.ProviderFee.GrossAmount,
		"receiveAmount":           pricing.ReceiveAmount,
		"receive_amount":          pricing.ReceiveAmount,
		"providerWithdrawalFee":   pricing.ProviderFee.mapValue(),
		"provider_withdrawal_fee": pricing.ProviderFee.mapValue(),
		"rateLockExpiresAt":       expiresAt,
	})
}

type publicQuoteInput struct {
	Side          string
	Asset         string
	Network       string
	FiatCurrency  string
	PaymentMethod string
	AmountFiat    float64
	CryptoAmount  float64
	Rate          float64
	MarketRate    float64
	FeeFiat       float64
	ExpiresAt     time.Time
}

func (s *Server) persistPublicQuote(r *http.Request, in publicQuoteInput) (string, bool, error) {
	quoteID := "qt_" + strings.ReplaceAll(database.NewID(), "-", "")
	if s == nil || s.db == nil {
		return quoteID, false, nil
	}
	hash := database.CanonicalRequestHash(
		strings.ToLower(strings.TrimSpace(in.Side)),
		strings.ToUpper(strings.TrimSpace(in.Asset)),
		normalizeBuyDeliveryNetwork(in.Network),
		strings.ToUpper(strings.TrimSpace(in.FiatCurrency)),
		strings.ToLower(strings.TrimSpace(in.PaymentMethod)),
		strconv.FormatInt(int64(money.MoneyFromFloat(in.AmountFiat)), 10),
		strings.TrimSpace(r.UserAgent()),
	)
	q, err := s.db.CreateQuote(r.Context(), database.QuoteInput{
		ID:                quoteID,
		Side:              in.Side,
		Asset:             in.Asset,
		Network:           normalizeBuyDeliveryNetwork(in.Network),
		FiatCurrency:      in.FiatCurrency,
		PaymentMethod:     in.PaymentMethod,
		AmountMinor:       int64(money.MoneyFromFloat(in.AmountFiat)),
		CryptoAmountUnits: money.TokenFromFloat(in.CryptoAmount).String(),
		Rate:              in.Rate,
		MarketRate:        in.MarketRate,
		FeeMinor:          int64(money.MoneyFromFloat(in.FeeFiat)),
		ExpiresAt:         in.ExpiresAt,
		BodyHash:          hash,
	})
	if err != nil {
		return "", false, err
	}
	return q.ID, true, nil
}
