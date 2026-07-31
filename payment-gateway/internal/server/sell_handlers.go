package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/models"
	"payment-gateway/internal/transactions"
	"payment-gateway/internal/workers"

	"github.com/ethereum/go-ethereum/common"
)

const sellDepositTTL = 5 * time.Minute

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	markLegacyRoute(w, r, "/sell")
	var req struct {
		AmountBRL    float64 `json:"amountBRL"`
		AmountUSDT   float64 `json:"amountUSDT"`
		CryptoAmount float64 `json:"cryptoAmount"`
		Address      string  `json:"address"`
		Network      string  `json:"network"`
		Asset        string  `json:"asset"`
		PixCpf       string  `json:"pixCpf"`
		PixPhone     string  `json:"pixPhone"`
		Email        string  `json:"email"`
		RateLocked   float64 `json:"rateLocked"`
		QuoteID      string  `json:"quoteId"`
		Surface      string `json:"surface"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido"})
		return
	}
	if req.PixCpf == "" || req.PixPhone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "CPF e chave PIX sao obrigatorios"})
		return
	}
	network := normalizeSellNetwork(defaultString(req.Network, "BSC"))
	asset := strings.ToUpper(defaultString(req.Asset, "USDT"))
	if asset != "USDT" && asset != "BTC" && asset != "ETH" && asset != "BNB" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset de sell nao suportado"})
		return
	}
	if !sellAssetNetworkAllowed(asset, network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rede invalida para asset de sell", "asset": asset, "network": network})
		return
	}
	if !s.sellNetworkEnabled(network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rede de sell nao suportada ou nao configurada", "network": network, "supportedNetworks": s.supportedSellNetworks()})
		return
	}
	ctx := r.Context()
	var idx *int
	var depositAddress string
	var btcFunding *database.BTCSellFundingInput
	if network == "BITCOIN" {
	depositAddress = strings.TrimSpace(s.cfg.SellBTCWalletAddress)

	if depositAddress == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "carteira BTC de deposito nao configurada",
		})
		return
	}

	// SELL WEB:
	// usa a wallet BTC operacional da ChainFX.
	// NÃO chama scanner.
	// NÃO exige wallet cadastrada.
	if strings.EqualFold(strings.TrimSpace(req.Surface), "web") {
		btcFunding = nil
	} else {
		if s.workers == nil || s.workers.BTCSvc == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "scanner BTC nao esta habilitado",
			})
			return
		}

		btcNetwork := string(s.workers.BTCSvc.Config().Network)

		walletAddress, err := s.db.FindBTCWalletAddressByAddress(
			ctx,
			btcNetwork,
			depositAddress,
		)
		if err != nil {
			writeError(w, err)
			return
		}

		if walletAddress == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error":   "wallet BTC de SELL nao esta cadastrada no scanner",
				"address": depositAddress,
				"network": btcNetwork,
			})
			return
		}

		btcFunding = &database.BTCSellFundingInput{
			UserID:          walletAddress.UserID,
			WalletAddressID: walletAddress.ID,
			BTCAddress:      walletAddress.Address,
			BTCNetwork:      walletAddress.Network,
			QuoteID:         req.QuoteID,
		}
	}
} else {
		depositAddress = strings.TrimSpace(firstNonEmpty(s.cfg.SellWalletAddress, req.Address))
		if depositAddress == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endereco EVM de deposito obrigatorio"})
			return
		}
		if !common.IsHexAddress(depositAddress) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endereco EVM invalido"})
			return
		}
	}
	rate := req.RateLocked
	if rate <= 0 {
		if asset == "BTC" {
			rate = s.buyAssetMarketRate("BRL", "BTC")
		} else {
			rate = s.workers.PriceWorker.GetCurrentPrice()
		}
	}
	if rate <= 0 {
		if req.AmountBRL > 0 && req.AmountUSDT > 0 {
			rate = req.AmountBRL / req.AmountUSDT
		} else {
			rate = 5.16
		}
	}
	marketRate := rate
	sourceAmount := req.AmountUSDT
	if sourceAmount <= 0 {
		sourceAmount = req.CryptoAmount
	}
	if sourceAmount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cryptoAmount deve ser maior que zero"})
		return
	}
	quoteAmountUSDT := sourceAmount
	if asset == "BTC" {
		expectedSats, err := btcSatsFromAmount(sourceAmount)
		if err != nil || expectedSats <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valor BTC invalido"})
			return
		}
		if btcFunding != nil {
			btcFunding.ExpectedSats = expectedSats
		}
		quoteAmountUSDT = sourceAmount
	} else if asset != "USDT" {
		if req.AmountBRL <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amountBRL deve ser maior que zero para sell BTC/ETH"})
			return
		}
		quoteAmountUSDT = req.AmountBRL / marketRate
	} else if quoteAmountUSDT <= 0 && req.AmountBRL > 0 {
		quoteAmountUSDT = req.AmountBRL / s.sellRate(marketRate)
	}
	rate, payout, spread := s.sellQuoteForAsset(asset, quoteAmountUSDT, marketRate)
	fee := spread
	totalBRL := payout
	order, err := s.db.CreateOrder(ctx, database.OrderInput{
		Status:            string(models.StatusAguardandoDeposito),
		AmountBRL:         totalBRL,
		AmountUSDT:        sourceAmount,
		FeeBRL:            fee,
		PayoutBRL:         payout,
		Address:           depositAddress,
		Asset:             asset,
		Network:           network,
		RateLocked:        rate,
		RateLockExpiresAt: time.Now().Add(sellDepositTTL),
		RequestID:         requestID(r),
		PixCpf:            req.PixCpf,
		PixPhone:          req.PixPhone,
		Email:             req.Email,
		DerivationIndex:   idx,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if btcFunding != nil {
		btcFunding.OrderID = order.ID
		if err := s.db.CreateBTCSellFunding(ctx, *btcFunding); err != nil {
			writeError(w, err)
			return
		}
	}
	_ = s.db.AddEvent(ctx, order.ID, "order.meta", map[string]any{"requestId": requestID(r), "ip": clientIP(r), "userAgent": r.UserAgent()})
	s.workers.Bus.Publish(workers.Event{Type: "order.created", OrderID: order.ID, Payload: map[string]any{"requestId": requestID(r), "amountBRL": totalBRL}})
	go s.email.NotifyOps("ChainFx: nova ordem criada", fmt.Sprintf("Ordem %s criada para %.2f BRL. EndereÃ§o: %s", order.ID, totalBRL, depositAddress))
	contract := transactions.Build(transactions.BuildInput{
		Side:               transactions.SideSell,
		OrderID:            order.ID,
		SourceAsset:        asset,
		DestinationAsset:   "BRL",
		SourceNetwork:      network,
		DestinationNetwork: "PIX",
		SourceChainID:      transactions.ChainID(network),
		SourceAmount:       sourceAmount,
		DestinationAmount:  payout,
		ExchangeRate:       rate,
		FeeAmount:          fee,
		FeeAsset:           "BRL",
		WalletAddress:      depositAddress,
		TreasuryAddress:    s.cfg.TreasuryHot,
		PaymentMethod:      "pix",
		PSPProvider:        "efi",
		Status:             transactions.CanonicalSellStatus(string(order.Status)),
		Request:            r,
		Metadata: map[string]any{
			"surface":           "api",
			"spreadBRL":         spread,
			"rateLockExpiresAt": order.RateLockExpiresAt,
		},
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": order.ID, "orderId": order.ID, "accessToken": order.AccessToken, "status": order.Status, "address": depositAddress, "depositAddress": depositAddress,
		"amountBRL": totalBRL, "subtotalBRL": payout, "amountUSDT": sourceAmount, "btcAmount": sourceAmount, "cryptoAmount": sourceAmount, "feeBRL": fee, "spreadBRL": spread, "totalBRL": totalBRL, "payoutBRL": payout,
		"rate": rate, "marketRate": roundRate(marketRate), "network": network, "sellPolicy": s.sellPolicy(marketRate, rate),
		"tradeIntent":        contract.Trade,
		"settlementContract": contract.Settlement,
		"ledgerContract":     contract.Ledger,
		"statusUrl":          fmt.Sprintf("/api/order/%s?accessToken=%s", order.ID, order.AccessToken),
		"streamUrl":          fmt.Sprintf("/api/order/%s/stream?accessToken=%s", order.ID, order.AccessToken),
	})
}

func btcSatsFromAmount(amount float64) (int64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("valor BTC invalido")
	}
	value := strconv.FormatFloat(amount, 'f', 8, 64)
	parts := strings.SplitN(value, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) > 8 {
		frac = frac[:8]
	}
	frac += strings.Repeat("0", 8-len(frac))
	fracSats, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, err
	}
	return whole*100000000 + fracSats, nil
}

func sellAssetNetworkAllowed(asset, network string) bool {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "BTC":
		return normalizeSellNetwork(network) == "BITCOIN"
	case "ETH":
		network = normalizeSellNetwork(network)
		return network == "ETHEREUM" || network == "BSC"
	case "USDT", "BNB":
		network = normalizeSellNetwork(network)
		return network == "BSC" || network == "POLYGON"
	default:
		return false
	}
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeOrderRead(w, r, r.PathValue("id")) {
		return
	}
	order, err := s.db.GetOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if order == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "ordem não encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, order)
}

func (s *Server) handleOrderStream(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeOrderRead(w, r, r.PathValue("id")) {
		return
	}
	id := r.PathValue("id")
	streamSSE(w, r, func(ctx context.Context) (sseUpdate, bool) {
		order, _ := s.db.GetOrder(ctx, id)
		if order == nil {
			return sseUpdate{}, false
		}
		status := string(order.Status)
		depositTx := ""
		if order.DepositTx != nil {
			depositTx = *order.DepositTx
		}
		txHash := ""
		if order.TxHash != nil {
			txHash = *order.TxHash
		}
		return sseUpdate{
			Key: fmt.Sprintf("%s|%s|%s", status, depositTx, txHash),
			Payload: map[string]any{
				"status":        status,
				"txHash":        txHash,
				"depositTx":     depositTx,
				"depositAmount": order.DepositAmount,
				"payoutBRL":     order.PayoutBRL,
				"error":         order.Error,
			},
			Final: order.Status.IsFinal(),
		}, true
	})
}

func (s *Server) authorizeOrderRead(w http.ResponseWriter, r *http.Request, id string) bool {
	ok, err := s.db.ValidateOrderAccess(r.Context(), id, customerAccessToken(r))
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
