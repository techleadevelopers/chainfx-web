package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/money"
	"payment-gateway/internal/transactions"
	"payment-gateway/internal/workers"
)

func (s *Server) handleCreateBuy(w http.ResponseWriter, r *http.Request) {
	markLegacyRoute(w, r, "/buy")
	var req struct {
		AmountBRL         float64 `json:"amountBRL"`
		AmountUSD         float64 `json:"amountUSD"`
		AmountFiat        float64 `json:"amountFiat"`
		FiatCurrency      string  `json:"fiatCurrency"`
		PaymentMethod     string  `json:"paymentMethod"`
		ProviderPaymentID string  `json:"providerPaymentId"`
		Asset             string  `json:"asset"`
		Network           string  `json:"network"`
		Address           string  `json:"address"`
		PixCpf            string  `json:"pixCpf"`
		PixPhone          string  `json:"pixPhone"`
		CPF               string  `json:"cpf"`
		Phone             string  `json:"phone"`
		Email             string  `json:"email"`
		CustomerEmail     string  `json:"customerEmail"`
		CustomerName      string  `json:"customerName"`
		Name              string  `json:"name"`
		BirthDate         string  `json:"birthDate"`
		Customer          struct {
			Name      string         `json:"name"`
			Email     string         `json:"email"`
			CPF       string         `json:"cpf"`
			Phone     string         `json:"phone"`
			BirthDate string         `json:"birthDate"`
			Address   map[string]any `json:"address"`
		} `json:"customer"`
		AddressPayload map[string]any `json:"addressPayload"`
		PaymentToken   string         `json:"paymentToken"`
		CardBrand      string         `json:"cardBrand"`
		Installments   int            `json:"installments"`
		BillingAddress map[string]any `json:"billingAddress"`
		QuoteID        string         `json:"quoteId"`
		RateLocked     float64        `json:"rateLocked"`
		FeeBRL         float64        `json:"feeBRL"`
		Surface        string         `json:"surface"`
		Card           struct {
			PaymentToken   string         `json:"paymentToken"`
			Brand          string         `json:"brand"`
			Installments   int            `json:"installments"`
			BillingAddress map[string]any `json:"billingAddress"`
		} `json:"card"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invÃƒÂ¡lido"})
		return
	}
	asset := strings.ToUpper(defaultString(req.Asset, "USDT"))
	deliveryNetwork := normalizeBuyDeliveryNetwork(defaultString(req.Network, s.deliveryNetwork()))
	if !s.buyLiquidityPairSupported(asset, deliveryNetwork) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "par asset/network nao suportado para compra"})
		return
	}
	if !validBuyDeliveryAddress(deliveryNetwork, req.Address) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("endereco %s invalido", deliveryNetwork)})
		return
	}
	fiatCurrency, paymentMethod, amountFiat := normalizePaymentRail(req.FiatCurrency, req.PaymentMethod, req.AmountFiat, req.AmountBRL, req.AmountUSD)
	if fiatCurrency == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rail de pagamento nao suportado"})
		return
	}
	if amountFiat <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amountFiat deve ser maior que zero"})
		return
	}
	if fiatCurrency == "BRL" && (amountFiat < s.buyMinBRL() || amountFiat > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor fora dos limites (%.2f - %.2f BRL)", s.buyMinBRL(), s.cfg.OrderMaxBrl)})
		return
	}
	var consumedQuote *database.Quote
	if strings.TrimSpace(req.QuoteID) != "" {
		if s.db == nil {
			writeAPIError(w, r, http.StatusServiceUnavailable, "QUOTE_PERSISTENCE_UNAVAILABLE", "Quote persistence unavailable.")
			return
		}
		quote, err := s.db.ConsumeQuote(r.Context(), database.QuoteConsumeInput{
			ID:            req.QuoteID,
			Side:          "buy",
			Asset:         asset,
			Network:       deliveryNetwork,
			FiatCurrency:  fiatCurrency,
			PaymentMethod: paymentMethod,
			AmountMinor:   int64(money.MoneyFromFloat(amountFiat)),
		})
		if err != nil {
			writeAPIError(w, r, http.StatusBadRequest, "QUOTE_LOCK_INVALID", "Quote lock expired, already used, or does not match this buy request.")
			return
		}
		consumedQuote = quote
	}
	marketRate := s.buyAssetMarketRate(fiatCurrency, asset)
	if marketRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao ainda nao carregada"})
		return
	}
	rate := 0.0
	if consumedQuote != nil {
		rate = consumedQuote.Rate
		marketRate = consumedQuote.MarketRate
	}
	if rate <= 0 {
		rate = req.RateLocked
	}
	if rate <= 0 {
		rate = s.buyRate(marketRate)
	}
	fee := s.transactionFee(amountFiat, fiatCurrency, rate)
	payout := amountFiat
	cryptoAmount := payout / rate
	if consumedQuote != nil {
		fee = money.MoneyMinor(consumedQuote.FeeMinor).Float64()
		if units, err := strconv.ParseInt(strings.TrimSpace(consumedQuote.CryptoAmountUnits), 10, 64); err == nil && units > 0 {
			cryptoAmount = money.TokenUnits(units).Float64()
		}
	}
	if req.RateLocked > 0 && req.FeeBRL > 0 && s.trustedMobileLoopback(r) {
		fee = req.FeeBRL
	}
	providerWithdrawal, err := s.validateBuyProviderWithdrawal(asset, deliveryNetwork, amountFiat, rate, cryptoAmount)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":                   err.Error(),
			"code":                    "PROVIDER_WITHDRAW_MINIMUM",
			"providerWithdrawalFee":   providerWithdrawal.mapValue(),
			"provider_withdrawal_fee": providerWithdrawal.mapValue(),
			"suggestedAlternatives":   buyLowFeeAlternatives(),
			"suggested_alternatives":  buyLowFeeAlternatives(),
		})
		return
	}
	if providerWithdrawal.Applies && !(req.RateLocked > 0 && req.FeeBRL > 0 && s.trustedMobileLoopback(r)) {
		fee = round2(fee + providerWithdrawal.ChargedFeeFiat)
	}
	if payout <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valor insuficiente apÃƒÂ³s taxa"})
		return
	}
	totalFiat := amountFiat + fee
	buyID := database.NewID()
	customerInput := paymentCustomerInput{
		Name:      firstNonEmpty(req.Customer.Name, req.CustomerName, req.Name),
		Email:     firstNonEmpty(req.Customer.Email, req.Email, req.CustomerEmail),
		CPF:       firstNonEmpty(req.Customer.CPF, req.PixCpf, req.CPF),
		Phone:     firstNonEmpty(req.Customer.Phone, req.PixPhone, req.Phone),
		BirthDate: firstNonEmpty(req.Customer.BirthDate, req.BirthDate),
		Address:   firstNonNilMap(req.Customer.Address, req.AddressPayload),
	}
	if fiatCurrency == "BRL" && paymentMethod == "pix" && s.efiPixConfigured() {
		if err := validateEfiPixCustomer(customerInput); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_CUSTOMER_DOCUMENT", "error": err.Error()})
			return
		}
	}
	cardInput := paymentCardInput{
		PaymentToken:   firstNonEmpty(req.Card.PaymentToken, req.PaymentToken),
		Brand:          firstNonEmpty(req.Card.Brand, req.CardBrand),
		Installments:   firstPositiveInt(req.Card.Installments, req.Installments, 1),
		BillingAddress: firstNonNilMap(req.Card.BillingAddress, req.BillingAddress, customerInput.Address),
	}
	paymentPayload, err := s.createPaymentIntent(r.Context(), buyID, totalFiat, fiatCurrency, paymentMethod, asset, deliveryNetwork, customerInput, cardInput)
	if err != nil {
		slog.Error("Erro ao criar cobranca", "buyId", buyID, "provider", "efi", "paymentMethod", paymentMethod, "error", err)
		writeAPIError(w, r, http.StatusBadGateway, "PAYMENT_PROVIDER_ERROR", "Payment provider unavailable.")
		return
	}
	customerAudit := buildCustomerAudit(s.cfg.LGPDSecret,
		customerInput.CPF,
		customerInput.Phone,
		customerInput.Email,
		customerInput.Name,
		customerInput.BirthDate,
		customerInput.Address,
	)
	if len(customerAudit) > 0 {
		paymentPayload["customer"] = customerAudit
	}
	if consumedQuote != nil {
		paymentPayload["quoteId"] = consumedQuote.ID
		paymentPayload["quoteRate"] = consumedQuote.Rate
		paymentPayload["quoteExpiresAt"] = consumedQuote.ExpiresAt
	}
	if providerWithdrawal.Applies {
		paymentPayload["providerWithdrawalFee"] = providerWithdrawal.mapValue()
	}
	amountBRL := totalFiat
	status := "aguardando_" + paymentMethod
	buy, err := s.db.CreateBuyOrder(r.Context(), database.BuyOrderInput{
		ID:                buyID,
		Channel:           req.Surface,
		Status:            status,
		AmountBRL:         amountBRL,
		AmountFiat:        totalFiat,
		FiatCurrency:      fiatCurrency,
		PaymentMethod:     paymentMethod,
		ProviderPaymentID: firstNonEmpty(req.ProviderPaymentID, mapString(paymentPayload, "providerPaymentId"), mapString(paymentPayload, "txid")),
		RequestID:         requestID(r),
		FeeBRL:            fee,
		PayoutBRL:         payout,
		CryptoAmount:      cryptoAmount,
		Asset:             asset,
		Network:           deliveryNetwork,
		DestAddress:       strings.TrimSpace(req.Address),
		RateLocked:        rate,
		RateLockExpiresAt: time.Now().Add(tradeRateLockTTL(req.Surface, s.cfg.RateLockSec)),
		PixPayload:        paymentPayload,
		CustomerEmail:     customerInput.Email,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	_ = s.db.AddBuyEvent(r.Context(), buy.ID, "buy.meta", map[string]any{"requestId": requestID(r), "ip": clientIP(r), "userAgent": r.UserAgent(), "customer": customerAudit, "quoteId": strings.TrimSpace(req.QuoteID)})
	s.workers.Bus.Publish(workers.Event{Type: "buy.created", OrderID: buy.ID, Payload: map[string]any{"requestId": requestID(r), "amountFiat": totalFiat, "fiatCurrency": fiatCurrency, "paymentMethod": paymentMethod}})
	go s.email.NotifyOpsOrderCreated(email.OpsOrderCreated{
		Subject:       "ChainFX: nova ordem de compra",
		Side:          "buy",
		Surface:       req.Surface,
		UserName:      customerInput.Name,
		OrderID:       buy.ID,
		AmountBRL:     totalFiat,
		CryptoAmount:  cryptoAmount,
		Asset:         asset,
		Network:       deliveryNetwork,
		Wallet:        strings.TrimSpace(req.Address),
		PixKey:        firstNonEmpty(req.PixPhone, req.PixCpf, customerInput.Phone, customerInput.CPF),
		PaymentMethod: paymentMethod,
	})
	contract := transactions.Build(transactions.BuildInput{
		Side:               transactions.SideBuy,
		OrderID:            buy.ID,
		SourceAsset:        fiatCurrency,
		DestinationAsset:   asset,
		SourceNetwork:      "FIAT",
		DestinationNetwork: deliveryNetwork,
		DestinationChainID: transactions.ChainID(deliveryNetwork),
		SourceAmount:       totalFiat,
		DestinationAmount:  cryptoAmount,
		ExchangeRate:       rate,
		SpreadBps:          s.cfg.BuyRateSpreadBps,
		FeeAmount:          fee,
		FeeAsset:           fiatCurrency,
		WalletAddress:      strings.TrimSpace(req.Address),
		TreasuryAddress:    s.cfg.TreasuryHot,
		PaymentMethod:      paymentMethod,
		PSPProvider:        "efi",
		Status:             transactions.CanonicalBuyStatus(status),
		Request:            r,
		Metadata: map[string]any{
			"surface":               defaultString(req.Surface, "api"),
			"rateLockExpiresAt":     buy.RateLockExpiresAt,
			"providerPaymentId":     firstNonEmpty(req.ProviderPaymentID, mapString(paymentPayload, "providerPaymentId"), mapString(paymentPayload, "txid")),
			"quoteId":               strings.TrimSpace(req.QuoteID),
			"providerWithdrawalFee": providerWithdrawal.mapValue(),
		},
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"buyId": buy.ID, "id": buy.ID, "accessToken": buy.AccessToken, "status": buy.Status, "amountFiat": totalFiat, "subtotalFiat": amountFiat, "fiatCurrency": fiatCurrency, "paymentMethod": paymentMethod, "feeFiat": fee, "totalFiat": totalFiat, "payoutFiat": payout,
		"quoteId": strings.TrimSpace(req.QuoteID), "quoteConsumed": consumedQuote != nil,
		"rate": rate, "marketRate": roundRate(marketRate), "cryptoAmount": cryptoAmount, "grossCryptoAmount": providerWithdrawal.GrossAmount, "withdrawGrossAmount": providerWithdrawal.GrossAmount, "withdraw_gross_amount": providerWithdrawal.GrossAmount, "receiveAmount": providerWithdrawal.ReceiveAmount, "receive_amount": providerWithdrawal.ReceiveAmount, "asset": asset, "network": deliveryNetwork, "destAddress": buy.DestAddress,
		"feePolicy": s.feePolicy(fiatCurrency, rate), "feeBreakdown": s.buyFeeBreakdown(amountFiat), "providerWithdrawalFee": providerWithdrawal.mapValue(), "provider_withdrawal_fee": providerWithdrawal.mapValue(),
		"pixKey": paymentPayload["pixKey"], "qrCodeUrl": paymentPayload["qrCodeUrl"], "payment": paymentPayload,
		"tradeIntent":        contract.Trade,
		"settlementContract": contract.Settlement,
		"ledgerContract":     contract.Ledger,
		"orderUrl":           fmt.Sprintf("/order/%s?accessToken=%s", buy.ID, buy.AccessToken),
		"statusUrl":          fmt.Sprintf("/api/buy/%s?accessToken=%s", buy.ID, buy.AccessToken),
		"streamUrl":          fmt.Sprintf("/api/buy/%s/stream?accessToken=%s", buy.ID, buy.AccessToken),
	})
}

func (s *Server) trustedMobileLoopback(r *http.Request) bool {
	if s == nil || s.cfg == nil || r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("X-ChainFX-Internal-Call")) != "mobile-loopback" {
		return false
	}
	key := chainFXAPIKeyFromHeader(r)
	if key == "" {
		return false
	}
	return csvContains(s.cfg.ChainFXLiveSecretKeys, key) || csvContains(s.cfg.ChainFXTestSecretKeys, key)
}

func (s *Server) handleGetBuy(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeBuyRead(w, r, r.PathValue("id")) {
		return
	}
	buy, err := s.db.GetBuyOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if buy == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "compra nÃƒÂ£o encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, buy)
}

func (s *Server) handleBuyStream(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeBuyRead(w, r, r.PathValue("id")) {
		return
	}
	id := r.PathValue("id")
	streamSSE(w, r, func(ctx context.Context) (sseUpdate, bool) {
		buy, _ := s.db.GetBuyOrder(ctx, id)
		if buy == nil {
			return sseUpdate{}, false
		}
		return sseUpdate{
			Key:     buy.Status,
			Payload: map[string]any{"status": buy.Status, "txHash": buy.TxHashOut},
			Final:   isFinalBuyStatus(buy.Status),
		}, true
	})
}

func (s *Server) authorizeBuyRead(w http.ResponseWriter, r *http.Request, id string) bool {
	ok, err := s.db.ValidateBuyAccess(r.Context(), id, customerAccessToken(r))
	if err != nil {
		writeError(w, err)
		return false
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "token de acesso invalido"})
		return false
	}
	return true
}
