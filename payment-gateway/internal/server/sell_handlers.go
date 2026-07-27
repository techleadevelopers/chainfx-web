package server

import (
	"context"
	"fmt"
	"net/http"
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
	if asset != "USDT" && asset != "BTC" && asset != "BNB" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "asset de sell nao suportado"})
		return
	}
	if !s.sellNetworkEnabled(network) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rede de sell nao suportada ou nao configurada", "network": network, "supportedNetworks": s.supportedSellNetworks()})
		return
	}
	ctx := r.Context()
	var idx *int
	depositAddress := strings.TrimSpace(firstNonEmpty(s.cfg.SellWalletAddress, req.Address))
	if depositAddress == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endereco EVM de deposito obrigatorio"})
		return
	}
	if !common.IsHexAddress(depositAddress) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endereco EVM invalido"})
		return
	}
	rate := req.RateLocked
	if rate <= 0 {
		rate = s.workers.PriceWorker.GetCurrentPrice()
	}
	if rate <= 0 {
		if req.AmountBRL > 0 && req.AmountUSDT > 0 {
			rate = req.AmountBRL / req.AmountUSDT
		} else {
			rate = 5.16
		}
	}
	marketRate := rate
	amountUSDT := req.AmountUSDT
	if amountUSDT <= 0 {
		amountUSDT = req.CryptoAmount
	}
	if amountUSDT <= 0 && req.AmountBRL > 0 {
		amountUSDT = req.AmountBRL / s.sellRate(marketRate)
	}
	if amountUSDT <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "amountUSDT deve ser maior que zero"})
		return
	}
	rate, payout, spread := s.sellQuote(amountUSDT, marketRate)
	fee := spread
	totalBRL := payout
	order, err := s.db.CreateOrder(ctx, database.OrderInput{
		Status:            string(models.StatusAguardandoDeposito),
		AmountBRL:         totalBRL,
		AmountUSDT:        amountUSDT,
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
		SourceAmount:       amountUSDT,
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
		"amountBRL": totalBRL, "subtotalBRL": payout, "amountUSDT": amountUSDT, "btcAmount": amountUSDT, "feeBRL": fee, "spreadBRL": spread, "totalBRL": totalBRL, "payoutBRL": payout,
		"rate": rate, "marketRate": roundRate(marketRate), "network": network, "sellPolicy": s.sellPolicy(marketRate, rate),
		"tradeIntent":        contract.Trade,
		"settlementContract": contract.Settlement,
		"ledgerContract":     contract.Ledger,
		"statusUrl":          fmt.Sprintf("/api/order/%s?accessToken=%s", order.ID, order.AccessToken),
		"streamUrl":          fmt.Sprintf("/api/order/%s/stream?accessToken=%s", order.ID, order.AccessToken),
	})
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
