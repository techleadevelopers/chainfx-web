package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/liquidity"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/transactions"
)

// handleMobileBuy — POST /api/mobile/order/buy
// Delegates to the existing POST /api/buy handler internally.
func (s *Server) handleMobileBuyQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AmountBRL float64 `json:"amount_brl"`
		Asset     string  `json:"asset"`
		Network   string  `json:"network"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountBRL <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl obrigatorio"})
		return
	}
	asset := strings.ToUpper(firstNonEmptyStr(req.Asset, "USDT"))
	network := normalizeMobileBuyNetwork(firstNonEmptyStr(req.Network, "BSC"))
	if !s.mobileBuyLiquidityPairSupported(asset, network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "par asset/network nao suportado para compra"})
		return
	}
	if min := s.mobileBuyMinBRL(); min > 0 && req.AmountBRL < min {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor minimo %.2f BRL", min)})
		return
	}
	if s != nil && s.cfg != nil && s.cfg.OrderMaxBrl > 0 && req.AmountBRL > s.cfg.OrderMaxBrl {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("valor maximo %.2f BRL", s.cfg.OrderMaxBrl)})
		return
	}
	marketRate := mobileAssetPriceBRL(s.PriceCache(), asset)
	if marketRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao indisponivel"})
		return
	}
	rate := s.mobileBuyRate(marketRate)
	if rate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao indisponivel"})
		return
	}
	totalMargin, feeBreakdown := s.mobileBuyFee(req.AmountBRL)
	fee := s.mobileBuyVisibleFee(req.AmountBRL, totalMargin)
	embeddedSpread := roundMoney(totalMargin - fee)
	quoteBRL := req.AmountBRL - embeddedSpread
	if quoteBRL <= 0 {
		quoteBRL = req.AmountBRL
		embeddedSpread = 0
	}
	cryptoAmount := roundCrypto(quoteBRL / rate)
	if cryptoAmount <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao indisponivel"})
		return
	}
	withdrawal := s.mobileBuyProviderWithdrawalFee(asset, network, rate, cryptoAmount)
	if withdrawal["applies"] == true {
		minBuyBRL, _ := withdrawal["min_buy_brl"].(float64)
		receiveAmount, _ := withdrawal["receive_amount"].(float64)
		if minBuyBRL > 0 && req.AmountBRL < minBuyBRL {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":                   fmt.Sprintf("compra minima de BTC na rede Bitcoin: %.2f BRL. Para valores menores, use USDT BEP-20, USDT Polygon ou outra rede com taxa baixa", minBuyBRL),
				"code":                    "PROVIDER_WITHDRAW_MINIMUM",
				"providerWithdrawalFee":   withdrawal,
				"provider_withdrawal_fee": withdrawal,
				"suggestedAlternatives":   mobileBuyLowFeeAlternatives(),
				"suggested_alternatives":  mobileBuyLowFeeAlternatives(),
			})
			return
		}
		minAmount, _ := withdrawal["min_amount"].(float64)
		if receiveAmount < minAmount || receiveAmount <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":                     fmt.Sprintf("valor minimo para BTC na rede Bitcoin: %.8f BTC bruto (%.8f BTC taxa BingX + %.8f BTC minimo de retirada), cerca de %.2f BRL antes da taxa Pix", withdrawal["min_gross_amount"], withdrawal["fee_amount"], withdrawal["min_amount"], withdrawal["min_gross_fiat"]),
				"code":                      "PROVIDER_WITHDRAW_MINIMUM",
				"providerWithdrawalFee":     withdrawal,
				"provider_withdrawal_fee":   withdrawal,
				"minProviderSubtotalFiat":   withdrawal["min_gross_fiat"],
				"min_provider_subtotal_brl": withdrawal["min_gross_fiat"],
				"suggestedAlternatives":     mobileBuyLowFeeAlternatives(),
				"suggested_alternatives":    mobileBuyLowFeeAlternatives(),
			})
			return
		}
	}
	if withdrawal["applies"] == true {
		if chargedFee, ok := withdrawal["charged_fee_brl"].(float64); ok && chargedFee > 0 {
			fee = roundMoney(fee + chargedFee)
			totalMargin = roundMoney(totalMargin + chargedFee)
		}
	}
	displayRate := roundRateLocal(req.AmountBRL / cryptoAmount)
	totalFiat := roundMoney(req.AmountBRL + fee)
	feeBreakdown["display_fee_brl"] = fee
	feeBreakdown["embedded_spread_brl"] = embeddedSpread
	feeBreakdown["total_margin_brl"] = totalMargin
	feeBreakdown["provider_withdrawal_fee"] = withdrawal
	expiresAt := time.Now().UTC().Add(time.Duration(s.mobileRateLockSec()) * time.Second)
	quoteID, err := s.issueMobileQuote(mobileQuoteClaims{
		Side:      "buy",
		Asset:     asset,
		Network:   network,
		Amount:    req.AmountBRL,
		Rate:      displayRate,
		Fee:       fee,
		Total:     totalFiat,
		ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao assinar cotacao"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"quote_id":                quoteID,
		"side":                    "buy",
		"asset":                   asset,
		"network":                 network,
		"fiat":                    "BRL",
		"amount_brl":              req.AmountBRL,
		"subtotal_brl":            req.AmountBRL,
		"fee_brl":                 fee,
		"feeFiat":                 fee,
		"total_brl":               totalFiat,
		"totalFiat":               totalFiat,
		"crypto_amount":           cryptoAmount,
		"cryptoAmount":            cryptoAmount,
		"gross_crypto_amount":     withdrawal["gross_amount"],
		"grossCryptoAmount":       withdrawal["gross_amount"],
		"withdraw_gross_amount":   withdrawal["gross_amount"],
		"withdrawGrossAmount":     withdrawal["gross_amount"],
		"receive_amount":          withdrawal["receive_amount"],
		"receiveAmount":           withdrawal["receive_amount"],
		"providerWithdrawalFee":   withdrawal,
		"provider_withdrawal_fee": withdrawal,
		"rate":                    displayRate,
		"market_rate":             roundRateLocal(marketRate),
		"marketRate":              roundRateLocal(marketRate),
		"feeBreakdown":            feeBreakdown,
		"expires_at":              expiresAt,
		"expiresAt":               expiresAt,
	})
}

func (s *Server) handleMobileBuy(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		AmountBRL                   float64        `json:"amount_brl"`
		Asset                       string         `json:"asset"`
		DestAddress                 string         `json:"dest_address"`
		Network                     string         `json:"network"`
		PaymentMethod               string         `json:"payment_method"` // "pix" | "card"
		PaymentToken                string         `json:"payment_token"`
		CardBrand                   string         `json:"card_brand"`
		Installments                int            `json:"installments"`
		BillingAddress              map[string]any `json:"billing_address"`
		CPF                         string         `json:"cpf"`
		CustomerName                string         `json:"customer_name"`
		CustomerEmail               string         `json:"customer_email"`
		CustomerCPF                 string         `json:"customer_cpf"`
		CustomerPhone               string         `json:"customer_phone"`
		CustomerBirthDate           string         `json:"customer_birth_date"`
		CustomerAddress             map[string]any `json:"customer_address"`
		CustomerAddressPostalCode   string         `json:"customer_address_postal_code"`
		CustomerAddressStreet       string         `json:"customer_address_street"`
		CustomerAddressNumber       string         `json:"customer_address_number"`
		CustomerAddressNeighborhood string         `json:"customer_address_neighborhood"`
		CustomerAddressCity         string         `json:"customer_address_city"`
		CustomerAddressState        string         `json:"customer_address_state"`
		CustomerAddressCountry      string         `json:"customer_address_country"`
		Customer                    struct {
			Name      string         `json:"name"`
			Email     string         `json:"email"`
			CPF       string         `json:"cpf"`
			Phone     string         `json:"phone"`
			BirthDate string         `json:"birthDate"`
			Address   map[string]any `json:"address"`
		} `json:"customer"`
		Card struct {
			PaymentToken   string         `json:"paymentToken"`
			Brand          string         `json:"brand"`
			Installments   int            `json:"installments"`
			BillingAddress map[string]any `json:"billingAddress"`
		} `json:"card"`
		QuoteID string `json:"quote_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountBRL <= 0 || req.DestAddress == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_brl e dest_address obrigatórios"})
		return
	}
	if req.Asset == "" {
		req.Asset = "USDT"
	}
	if req.PaymentMethod == "" {
		req.PaymentMethod = "pix"
	}
	network := normalizeMobileBuyNetwork(firstNonEmptyStr(req.Network, "BSC"))
	if network == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network nao suportada para compra"})
		return
	}
	req.Asset = strings.ToUpper(firstNonEmptyStr(req.Asset, "USDT"))
	if !s.mobileBuyLiquidityPairSupported(req.Asset, network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "par asset/network nao suportado para compra"})
		return
	}
	// P0-02: Reject EVM-style 0x addresses for Bitcoin purchases. A Bitcoin
	// address is required when asset=BTC / network=BITCOIN; an EVM address
	// sent here would route funds to an unreachable on-chain address.
	if req.Asset == "BTC" && network == "BITCOIN" && looksLikeEVMAddress(req.DestAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":    "INVALID_BTC_ADDRESS",
			"message": "Endereço Bitcoin inválido: forneça um endereço BTC (bc1...) para compras de Bitcoin",
		})
		return
	}
	claims, err := s.verifyMobileQuote(req.QuoteID, "buy", req.Asset, req.AmountBRL, time.Now(), network)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "MOBILE_QUOTE_INVALID"})
		return
	}

	user, _ := mobileDB(s.db).GetUserByID(r.Context(), uid)
	customerName := strings.TrimSpace(firstNonEmptyStr(req.CustomerName, req.Customer.Name))
	customerEmail := strings.TrimSpace(firstNonEmptyStr(req.CustomerEmail, req.Customer.Email))
	customerCPF := onlyDigitsMobile(firstNonEmptyStr(req.CustomerCPF, req.CPF, req.Customer.CPF))
	customerPhone := onlyDigitsMobile(firstNonEmptyStr(req.CustomerPhone, req.Customer.Phone))
	customerBirthDate := strings.TrimSpace(firstNonEmptyStr(req.CustomerBirthDate, req.Customer.BirthDate))
	customerAddress := normalizeMobileCustomerAddress(firstNonNilAddress(req.CustomerAddress, req.Customer.Address))
	if len(customerAddress) == 0 {
		customerAddress = normalizeMobileCustomerAddress(map[string]any{
			"postal_code":  req.CustomerAddressPostalCode,
			"street":       req.CustomerAddressStreet,
			"number":       req.CustomerAddressNumber,
			"neighborhood": req.CustomerAddressNeighborhood,
			"city":         req.CustomerAddressCity,
			"state":        req.CustomerAddressState,
			"country":      req.CustomerAddressCountry,
		})
	}
	if user != nil {
		customerName = strings.TrimSpace(firstNonEmptyStr(customerName, mobileUserString(user.FullName)))
		customerEmail = strings.TrimSpace(firstNonEmptyStr(customerEmail, user.Email))
		customerCPF = onlyDigitsMobile(firstNonEmptyStr(customerCPF, mobileUserCPF(user)))
		customerPhone = onlyDigitsMobile(firstNonEmptyStr(customerPhone, mobileUserString(user.Phone)))
		customerBirthDate = strings.TrimSpace(firstNonEmptyStr(customerBirthDate, mobileUserString(user.BirthDate)))
		if len(customerAddress) == 0 {
			customerAddress = mobileUserAddress(user)
		}
	}
	if strings.EqualFold(req.PaymentMethod, "pix") {
		if customerName == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "nome do cliente obrigatorio no perfil"})
			return
		}
		if customerCPF == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cpf do cliente obrigatorio no cadastro"})
			return
		}
	}

	// Forward to existing /api/buy
	payload := map[string]any{
		"amountBRL":     req.AmountBRL,
		"asset":         req.Asset,
		"address":       req.DestAddress,
		"network":       network,
		"paymentMethod": req.PaymentMethod,
		"rateLocked":    claims.Rate,
		"feeBRL":        claims.Fee,
		"paymentToken":  firstNonEmptyStr(req.PaymentToken, req.Card.PaymentToken),
		"cardBrand":     firstNonEmptyStr(req.CardBrand, req.Card.Brand),
		"installments":  firstPositiveIntMobile(req.Installments, req.Card.Installments, 1),
		"billingAddress": firstNonNilAddress(
			req.BillingAddress,
			req.Card.BillingAddress,
			customerAddress,
		),
		"card": map[string]any{
			"paymentToken":   firstNonEmptyStr(req.PaymentToken, req.Card.PaymentToken),
			"brand":          firstNonEmptyStr(req.CardBrand, req.Card.Brand),
			"installments":   firstPositiveIntMobile(req.Installments, req.Card.Installments, 1),
			"billingAddress": firstNonNilAddress(req.BillingAddress, req.Card.BillingAddress, customerAddress),
		},
		"customer": map[string]any{
			"name":      customerName,
			"email":     customerEmail,
			"cpf":       customerCPF,
			"phone":     customerPhone,
			"birthDate": customerBirthDate,
			"address":   customerAddress,
		},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	internalReq := r.Clone(ctx)
	resp, err := forwardToInternal(internalReq, "POST", s.internalBase(r)+"/api/buy", payload, s.internalAPIKey())
	if err != nil {
		if strings.EqualFold(req.PaymentMethod, "pix") {
			if s.writeDegradedMobileBuy(w, r, req.AmountBRL, req.Asset, req.DestAddress, network, req.PaymentMethod, customerEmail, claims.Rate, "internal_request_failed") {
				return
			}
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "erro ao criar ordem: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 && strings.EqualFold(req.PaymentMethod, "pix") {
		if s.writeDegradedMobileBuy(w, r, req.AmountBRL, req.Asset, req.DestAddress, network, req.PaymentMethod, customerEmail, claims.Rate, "payment_provider_unavailable") {
			return
		}
	}

	// Tag order with user_id if we got an id back
	var result map[string]any
	if json.Unmarshal(body, &result) == nil {
		if id, ok := result["id"].(string); ok && id != "" {
			_ = mobileDB(s.db).TagBuyOrderUser(r.Context(), id, uid)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func (s *Server) writeDegradedMobileBuy(w http.ResponseWriter, r *http.Request, amountBRL float64, asset, destAddress, network, paymentMethod, customerEmail string, lockedRate float64, reason string) bool {
	uid := userIDFromCtx(r)
	asset = strings.ToUpper(strings.TrimSpace(firstNonEmptyStr(asset, "USDT")))
	paymentMethod = strings.ToLower(strings.TrimSpace(firstNonEmptyStr(paymentMethod, "pix")))
	destAddress = strings.TrimSpace(destAddress)
	network = normalizeMobileBuyNetwork(firstNonEmptyStr(network, "BSC"))
	if paymentMethod != "pix" || amountBRL <= 0 || !looksLikeEVMAddress(destAddress) {
		return false
	}
	if !liquidity.IsEVMNetwork(network) {
		return false
	}
	if !s.mobileBuyLiquidityPairSupported(asset, network) {
		return false
	}
	if min := s.mobileBuyMinBRL(); min > 0 && amountBRL < min {
		return false
	}
	if s != nil && s.cfg != nil && s.cfg.OrderMaxBrl > 0 && amountBRL > s.cfg.OrderMaxBrl {
		return false
	}
	marketRate := mobileAssetPriceBRL(s.PriceCache(), asset)
	if marketRate <= 0 {
		return false
	}
	rate := lockedRate
	if rate <= 0 {
		rate = s.mobileBuyRate(marketRate)
	}
	fee, feeBreakdown := s.mobileBuyFee(amountBRL)
	totalFiat := roundMoney(amountBRL + fee)
	cryptoAmount := roundCrypto(amountBRL / rate)
	buy, err := s.db.CreateBuyOrder(r.Context(), database.BuyOrderInput{
		Status:            "payment_provider_pending",
		AmountBRL:         totalFiat,
		AmountFiat:        totalFiat,
		FiatCurrency:      "BRL",
		PaymentMethod:     "pix",
		RequestID:         mobileRequestID(r),
		FeeBRL:            fee,
		PayoutBRL:         amountBRL,
		CryptoAmount:      cryptoAmount,
		Asset:             asset,
		Network:           network,
		DestAddress:       destAddress,
		RateLocked:        rate,
		RateLockExpiresAt: time.Now().Add(time.Duration(s.mobileRateLockSec()) * time.Second),
		PixPayload: map[string]any{
			"provider":             "degraded",
			"providerUnavailable":  true,
			"paymentAvailable":     false,
			"requiresPaymentRetry": true,
			"reason":               reason,
			"message":              "Provedor de pagamento indisponivel; ordem criada para retentativa de cobranca.",
		},
		CustomerEmail: customerEmail,
	})
	if err != nil {
		slog.Warn("mobile_buy_degraded_create_failed", "err", err, "reason", reason)
		return false
	}
	_ = mobileDB(s.db).TagBuyOrderUser(r.Context(), buy.ID, uid)
	if s.workers != nil && s.workers.Bus != nil {
		s.workers.Bus.Publish(workerEvent("buy.payment_provider_pending", map[string]any{
			"orderId":       buy.ID,
			"requestId":     mobileRequestID(r),
			"amountFiat":    totalFiat,
			"fiatCurrency":  "BRL",
			"paymentMethod": "pix",
			"reason":        reason,
		}))
	}
	contract := transactions.Build(transactions.BuildInput{
		Side:               transactions.SideBuy,
		OrderID:            buy.ID,
		SourceAsset:        "BRL",
		DestinationAsset:   asset,
		SourceNetwork:      "FIAT",
		DestinationNetwork: network,
		DestinationChainID: transactions.ChainID(network),
		SourceAmount:       totalFiat,
		DestinationAmount:  cryptoAmount,
		ExchangeRate:       rate,
		FeeAmount:          fee,
		FeeAsset:           "BRL",
		WalletAddress:      destAddress,
		TreasuryAddress:    s.cfg.TreasuryHot,
		PaymentMethod:      "pix",
		PSPProvider:        "degraded",
		Status:             transactions.CanonicalBuyStatus(buy.Status),
		Request:            r,
		Metadata: map[string]any{
			"surface": "mobile",
			"reason":  reason,
		},
	})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"buyId":                buy.ID,
		"id":                   buy.ID,
		"accessToken":          buy.AccessToken,
		"status":               buy.Status,
		"paymentAvailable":     false,
		"requiresPaymentRetry": true,
		"amountFiat":           totalFiat,
		"subtotalFiat":         amountBRL,
		"fiatCurrency":         "BRL",
		"paymentMethod":        "pix",
		"feeFiat":              fee,
		"totalFiat":            totalFiat,
		"payoutFiat":           amountBRL,
		"rate":                 rate,
		"marketRate":           roundRateLocal(marketRate),
		"cryptoAmount":         cryptoAmount,
		"asset":                asset,
		"network":              network,
		"destAddress":          destAddress,
		"feeBreakdown":         feeBreakdown,
		"payment": map[string]any{
			"provider":             "degraded",
			"paymentAvailable":     false,
			"requiresPaymentRetry": true,
			"reason":               reason,
		},
		"tradeIntent":        contract.Trade,
		"settlementContract": contract.Settlement,
		"ledgerContract":     contract.Ledger,
		"orderUrl":           fmt.Sprintf("/order/%s?accessToken=%s", buy.ID, buy.AccessToken),
		"statusUrl":          fmt.Sprintf("/api/buy/%s?accessToken=%s", buy.ID, buy.AccessToken),
	})
	return true
}

// handleMobileSell — POST /api/mobile/order/sell
// Delegates to existing POST /api/order handler.
func (s *Server) handleMobileSellQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AmountUSDT float64 `json:"amount_usdt"`
		Asset      string  `json:"asset"`
		QuoteID    string  `json:"quote_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountUSDT <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_usdt obrigatorio"})
		return
	}
	asset := strings.ToUpper(firstNonEmptyStr(req.Asset, "USDT"))
	if !mobileTradeAssetSupported(asset) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset nao suportado nesta fase"})
		return
	}
	marketRate := mobileAssetPriceBRL(s.PriceCache(), asset)
	if marketRate <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "cotacao indisponivel"})
		return
	}
	rate, payoutBRL, spreadBRL, spreadBps := s.mobileSellQuote(req.AmountUSDT, marketRate)
	if s != nil && s.cfg != nil && (payoutBRL < s.cfg.OrderMinBrl || payoutBRL > s.cfg.OrderMaxBrl) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("valor fora dos limites (%.2f - %.2f BRL)", s.cfg.OrderMinBrl, s.cfg.OrderMaxBrl),
		})
		return
	}
	expiresAt := time.Now().UTC().Add(time.Duration(s.mobileRateLockSec()) * time.Second)
	quoteID, err := s.issueMobileQuote(mobileQuoteClaims{
		Side:      "sell",
		Asset:     asset,
		Amount:    req.AmountUSDT,
		Rate:      rate,
		Fee:       spreadBRL,
		Total:     payoutBRL,
		ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao assinar cotacao"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"quote_id":      quoteID,
		"side":          "sell",
		"asset":         asset,
		"fiat":          "BRL",
		"amount_usdt":   req.AmountUSDT,
		"amount_crypto": req.AmountUSDT,
		"cryptoAmount":  req.AmountUSDT,
		"rate":          rate,
		"market_rate":   roundRateLocal(marketRate),
		"marketRate":    roundRateLocal(marketRate),
		"estimated_brl": payoutBRL,
		"amount_brl":    payoutBRL,
		"fee_brl":       spreadBRL,
		"feeFiat":       spreadBRL,
		"net_brl":       payoutBRL,
		"receive_brl":   payoutBRL,
		"payoutFiat":    payoutBRL,
		"totalFiat":     payoutBRL,
		"spread_bps":    spreadBps,
		"expires_at":    expiresAt,
		"expiresAt":     expiresAt,
	})
}

func (s *Server) handleMobileSell(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		AmountUSDT float64 `json:"amount_usdt"`
		PixKey     string  `json:"pix_key"`
		PixCpf     string  `json:"pix_cpf"`
		PixPhone   string  `json:"pix_phone"`
		Asset      string  `json:"asset"`
		QuoteID    string  `json:"quote_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.AmountUSDT <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amount_usdt obrigatório"})
		return
	}
	req.Asset = strings.ToUpper(firstNonEmptyStr(req.Asset, "USDT"))
	if !mobileTradeAssetSupported(req.Asset) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset nao suportado nesta fase"})
		return
	}
	claims, err := s.verifyMobileQuote(req.QuoteID, "sell", req.Asset, req.AmountUSDT, time.Now())
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "MOBILE_QUOTE_INVALID"})
		return
	}
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	if !mobileUserKYCApproved(user) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "KYC aprovado obrigatorio para vender cripto no app mobile",
			"code":  "MOBILE_SELL_KYC_REQUIRED",
		})
		return
	}
	pixKey := req.PixKey
	if pixKey == "" && req.PixPhone != "" {
		pixKey = req.PixPhone
	}
	if pixKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pix_key ou pix_phone obrigatório"})
		return
	}
	payload := map[string]any{
		"amountUSDT": req.AmountUSDT,
		"pixPhone":   pixKey,
		"pixCpf":     req.PixCpf,
		"asset":      req.Asset,
		"quoteId":    req.QuoteID,
		"rateLocked": claims.Rate,
	}
	resp, err := forwardToInternal(r, "POST", s.internalBase(r)+"/api/order", payload, s.internalAPIKey())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if json.Unmarshal(body, &result) == nil {
		if id, ok := result["id"].(string); ok && id != "" {
			_ = mobileDB(s.db).TagOrderUser(r.Context(), id, uid)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func mobileTradeAssetSupported(asset string) bool {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "USDT", "BTC", "BNB", "ETH", "LINK", "AVAX":
		return true
	default:
		return false
	}
}

// handleMobileSwap — POST /api/mobile/order/swap
// Stub: swap = sell → buy. Returns instructions for two-leg swap.
func (s *Server) handleMobileSwap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FromAsset string  `json:"from_asset"`
		ToAsset   string  `json:"to_asset"`
		Amount    float64 `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "from_asset, to_asset e amount obrigatórios"})
		return
	}
	price := mobileAssetPriceBRL(s.PriceCache(), strings.ToUpper(firstNonEmptyStr(req.FromAsset, "USDT")))
	writeJSON(w, http.StatusAccepted, map[string]any{
		"type":       "swap",
		"from_asset": req.FromAsset,
		"to_asset":   req.ToAsset,
		"amount":     req.Amount,
		"rate":       price,
		"status":     "quote_only",
		"hint":       "Swap direto em andamento. Use sell + buy para executar agora.",
	})
}

// handleMobileGetOrder — GET /api/mobile/order/{id}
func (s *Server) handleMobileGetOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	uid := userIDFromCtx(r)
	if buy, err := mobileDB(s.db).GetBuyOrderByUser(r.Context(), id, uid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	} else if buy != nil {
		writeJSON(w, http.StatusOK, buy)
		return
	}
	resp, err := forwardToInternal(r, "GET", s.internalBase(r)+"/api/order/"+id, nil, s.internalAPIKey())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// handleMobileListOrders — GET /api/mobile/orders
func (s *Server) handleMobileListOrders(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	orders, err := mobileDB(s.db).ListOrdersByUser(r.Context(), uid, 20)
	if err != nil {
		slog.Error("erro interno", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro interno"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders, "count": len(orders)})
}

// handleMobileCancelOrder — POST /api/mobile/order/cancel
func (s *Server) handleMobileCancelOrder(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	var req struct {
		OrderID string `json:"order_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.OrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "order_id obrigatório"})
		return
	}
	if err := mobileDB(s.db).CancelOrder(r.Context(), req.OrderID, uid); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func (s *Server) internalBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = strings.Split(forwardedHost, ",")[0]
	}
	if host == "" {
		host = fmt.Sprintf("localhost:%s", s.cfg.Port)
	}
	return scheme + "://" + host
}

func (s *Server) internalAPIKey() string {
	if s == nil || s.cfg == nil {
		return ""
	}
	if key := strings.TrimSpace(s.cfg.ChainFXLiveSecretKeys); key != "" {
		return key
	}
	return strings.TrimSpace(s.cfg.ChainFXTestSecretKeys)
}

func forwardToInternal(r *http.Request, method, url string, payload any, apiKey string) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ChainFX-Internal-Call", "mobile-loopback")
	if apiKey != "" {
		first := strings.Split(apiKey, ",")[0]
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(first))
	}
	for _, header := range []string{"X-Request-Id", "X-Correlation-Id", "X-Trace-Id", "Traceparent", "Idempotency-Key", "X-Idempotency-Key"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			req.Header.Set(header, value)
		}
	}
	metrics.IncInternalHTTPLoopback("mobile", metrics.RoutePattern(method, req.URL.Path, ""))
	return http.DefaultClient.Do(req)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeMobileCustomerAddress(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := map[string]any{}
	add := func(outKey string, keys ...string) {
		for _, key := range keys {
			if raw, ok := input[key]; ok {
				value := strings.TrimSpace(valueToString(raw))
				if value != "" {
					out[outKey] = value
					return
				}
			}
		}
	}
	add("postal_code", "postal_code", "postalCode", "zipcode", "zipCode", "cep")
	add("street", "street", "logradouro", "addressLine1")
	add("number", "number", "numero")
	add("neighborhood", "neighborhood", "bairro")
	add("city", "city", "cidade")
	add("state", "state", "uf", "province")
	add("country", "country", "pais")
	if postalCode, ok := out["postal_code"].(string); ok {
		out["postal_code"] = onlyDigitsMobile(postalCode)
	}
	if state, ok := out["state"].(string); ok {
		out["state"] = strings.ToUpper(strings.TrimSpace(state))
	}
	if country, ok := out["country"].(string); ok {
		out["country"] = strings.ToUpper(strings.TrimSpace(country))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonNilAddress(values ...map[string]any) map[string]any {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func looksLikeEVMAddress(address string) bool {
	address = strings.TrimSpace(address)
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return false
	}
	for _, ch := range address[2:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

func (s *Server) mobileBuyMinBRL() float64 {
	if s == nil || s.cfg == nil {
		return 0
	}
	if s.cfg.BuyTier1MinBrl > s.cfg.OrderMinBrl {
		return s.cfg.BuyTier1MinBrl
	}
	return s.cfg.OrderMinBrl
}

func (s *Server) mobileRateLockSec() int {
	if s == nil || s.cfg == nil || s.cfg.RateLockSec <= 0 {
		return 600
	}
	return s.cfg.RateLockSec
}

func (s *Server) mobileBuyRate(marketRate float64) float64 {
	if marketRate <= 0 {
		return 0
	}
	spreadBPS := 0
	if s != nil && s.cfg != nil && s.cfg.BuyRateSpreadBps > 0 {
		spreadBPS = s.cfg.BuyRateSpreadBps
	}
	return roundRateLocal(marketRate * (1 + float64(spreadBPS)/10000))
}

func (s *Server) mobileBuyFee(amountBRL float64) (float64, map[string]any) {
	if s == nil || s.cfg == nil {
		return 0, map[string]any{"tier": "none", "service_bps": 0, "network_fee_brl": 0, "min_fee_brl": 0}
	}
	bps := s.cfg.BuyTier3Bps
	tier := "tier3"
	if amountBRL < s.cfg.BuyTier1MaxBrl {
		bps = s.cfg.BuyTier1Bps
		tier = "tier1"
	} else if amountBRL < s.cfg.BuyTier2MaxBrl {
		bps = s.cfg.BuyTier2Bps
		tier = "tier2"
	}
	if bps == 0 {
		bps = s.cfg.FeeBps
		tier = "default"
	}
	serviceFee := roundMoney(amountBRL * float64(bps) / 10000)
	networkFee := roundMoney(s.cfg.BuyNetworkFeeBrl)
	totalFee := roundMoney(serviceFee + networkFee)
	minFee := roundMoney(s.cfg.BuyMinFeeBrl)
	if minFee <= 0 {
		minFee = roundMoney(s.cfg.FeeMinBrl)
	}
	if totalFee < minFee {
		totalFee = minFee
	}
	return totalFee, map[string]any{
		"tier":            tier,
		"service_bps":     bps,
		"service_fee_brl": serviceFee,
		"network_fee_brl": networkFee,
		"min_fee_brl":     minFee,
		"total_fee_brl":   totalFee,
	}
}

func (s *Server) mobileBuyVisibleFee(amountBRL, totalMarginBRL float64) float64 {
	if totalMarginBRL <= 0 {
		return 0
	}
	fee := math.Ceil((amountBRL*0.0185+1.99)*100) / 100
	if fee < 4.99 {
		fee = 4.99
	}
	if fee > totalMarginBRL {
		return roundMoney(totalMarginBRL)
	}
	return fee
}

func (s *Server) mobileBuyProviderWithdrawalFee(asset, network string, rate float64, receiveAmount float64) map[string]any {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeMobileBuyNetwork(network)
	if asset != "BTC" || network != "BITCOIN" {
		return map[string]any{
			"applies":        false,
			"gross_amount":   receiveAmount,
			"receive_amount": receiveAmount,
		}
	}
	fee := 0.00005
	minAmount := 0.000056
	minBuyBRL := 20.0
	chargedFeeBRL := 20.0
	if s != nil && s.cfg != nil {
		if s.cfg.BuyBTCBitcoinMinBrl > 0 {
			minBuyBRL = s.cfg.BuyBTCBitcoinMinBrl
		}
		if s.cfg.BuyBTCBitcoinNetworkFeeBrl > 0 {
			chargedFeeBRL = s.cfg.BuyBTCBitcoinNetworkFeeBrl
		}
		if s.cfg.BingXBTCWithdrawFeeBTC > 0 {
			fee = s.cfg.BingXBTCWithdrawFeeBTC
		}
		if s.cfg.BingXBTCWithdrawMinBTC > 0 {
			minAmount = s.cfg.BingXBTCWithdrawMinBTC
		}
	}
	receive := roundCrypto(receiveAmount)
	if receive < 0 {
		receive = 0
	}
	feeFiat := roundMoney(fee * rate)
	if chargedFeeBRL < feeFiat {
		chargedFeeBRL = feeFiat
	}
	withdrawGross := roundCrypto(receive + fee)
	minGross := roundCrypto(fee + minAmount)
	return map[string]any{
		"applies":               true,
		"provider":              "bingx",
		"asset":                 asset,
		"network":               network,
		"min_buy_fiat":          minBuyBRL,
		"min_buy_brl":           minBuyBRL,
		"fee_amount":            fee,
		"fee_asset":             asset,
		"min_amount":            minAmount,
		"min_asset":             asset,
		"gross_amount":          withdrawGross,
		"withdraw_gross_amount": withdrawGross,
		"min_gross_amount":      minGross,
		"fee_fiat":              feeFiat,
		"charged_fee_fiat":      chargedFeeBRL,
		"charged_fee_brl":       chargedFeeBRL,
		"min_gross_fiat":        roundMoney(minGross * rate),
		"receive_amount":        receive,
		"deducted_from_send":    true,
	}
}

func mobileBuyLowFeeAlternatives() []map[string]string {
	return []map[string]string{
		{"asset": "USDT", "network": "BSC", "label": "USDT (BEP-20)"},
		{"asset": "USDT", "network": "POLYGON", "label": "USDT (Polygon)"},
		{"asset": "USDC", "network": "BASE", "label": "USDC (Base)"},
		{"asset": "SOL", "network": "SOLANA", "label": "Solana"},
	}
}

func (s *Server) mobileSellQuote(amountUSDT, marketRate float64) (sellRate, payoutBRL, spreadBRL float64, spreadBps int) {
	spreadBps = s.mobileSellSpreadBps(amountUSDT, marketRate)
	sellRate = roundRateLocal(marketRate * (1 - float64(spreadBps)/10000))
	if sellRate < 0 {
		sellRate = 0
	}
	payoutBRL = roundMoney(amountUSDT * sellRate)
	marketValue := roundMoney(amountUSDT * marketRate)
	spreadBRL = roundMoney(marketValue - payoutBRL)
	if spreadBRL < 0 {
		spreadBRL = 0
	}
	return sellRate, payoutBRL, spreadBRL, spreadBps
}

func (s *Server) mobileSellSpreadBps(amountUSDT, marketRate float64) int {
	if s == nil || s.cfg == nil {
		return 0
	}
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

func mobileRequestID(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, header := range []string{"X-Request-Id", "X-Correlation-Id", "X-Trace-Id"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
			return value
		}
	}
	return ""
}

func roundMoney(value float64) float64 {
	return math.Round(value*100) / 100
}

func roundCrypto(value float64) float64 {
	return math.Round(value*1e8) / 1e8
}

func roundRateLocal(value float64) float64 {
	return math.Round(value*1e6) / 1e6
}
