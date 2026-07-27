package mobile

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"payment-gateway/internal/bitcoin"
)

// btcSvcOrErr retorna o serviço BTC ou escreve 503 e retorna nil.
func (s *Server) btcSvcOrErr(w http.ResponseWriter) *bitcoin.Service {
	if s.btcSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "BTC_DISABLED",
			"message": "suporte a Bitcoin não está habilitado neste servidor",
		})
		return nil
	}
	return s.btcSvc
}

// ─── GET /api/mobile/btc/address ─────────────────────────────────────────────
// Retorna o endereço BTC de recebimento do usuário (cria se necessário).

func (s *Server) handleBTCAddress(w http.ResponseWriter, r *http.Request) {
	svc := s.btcSvcOrErr(w)
	if svc == nil {
		return
	}
	uid := userIDFromCtx(r)
	addr, err := svc.GetOrCreateAddress(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    "BTC_ADDRESS_ERROR",
			"message": "erro ao obter endereço Bitcoin: " + err.Error(),
		})
		return
	}
	cfg := svc.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"address":               addr.Address,
		"network":               addr.Network,
		"bitcoin_network":       string(cfg.Network), // "mainnet" | "testnet" | "signet" | "regtest"
		"address_type":          addr.AddressType,
		"derivation_path":       addr.DerivationPath,
		"derivation_index":      addr.DerivationIndex,
		"minimum_confirmations": cfg.MinConfirmations,
		"created_at":            addr.CreatedAt,
	})
}

// ─── GET /api/mobile/btc/balance ─────────────────────────────────────────────
// Retorna saldo confirmado, pendente, reservado e disponível em satoshis.

func (s *Server) handleBTCBalance(w http.ResponseWriter, r *http.Request) {
	svc := s.btcSvcOrErr(w)
	if svc == nil {
		return
	}
	uid := userIDFromCtx(r)
	cfg := svc.Config()
	bal, err := svc.GetBalance(r.Context(), uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    "BTC_BALANCE_ERROR",
			"message": "erro ao buscar saldo Bitcoin: " + err.Error(),
		})
		return
	}
	// Enriquecer com campos de contexto para o mobile
	bal.Asset = "BTC"
	bal.Network = string(cfg.Network)
	bal.MinimumConfirmations = cfg.MinConfirmations
	writeJSON(w, http.StatusOK, bal)
}

// ─── GET /api/mobile/btc/fee-estimate ────────────────────────────────────────
// Retorna estimativa de fee para um envio. Query: amount_sats=<n>

func (s *Server) handleBTCFeeEstimate(w http.ResponseWriter, r *http.Request) {
	svc := s.btcSvcOrErr(w)
	if svc == nil {
		return
	}
	amountStr := strings.TrimSpace(r.URL.Query().Get("amount_sats"))
	amountSats := int64(1000) // valor padrão para estimativa
	if amountStr != "" {
		if v, err := strconv.ParseInt(amountStr, 10, 64); err == nil && v > 0 {
			amountSats = v
		}
	}
	est, err := svc.EstimateFee(r.Context(), amountSats)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    "BTC_FEE_ERROR",
			"message": "erro ao estimar fee: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, est)
}

// ─── GET /api/mobile/btc/transactions ────────────────────────────────────────
// Lista as transações BTC do usuário. Query: limit=<n>

func (s *Server) handleBTCTransactions(w http.ResponseWriter, r *http.Request) {
	svc := s.btcSvcOrErr(w)
	if svc == nil {
		return
	}
	uid := userIDFromCtx(r)
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 100 {
			limit = v
		}
	}
	txs, err := svc.ListUserTransactions(r.Context(), uid, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code":    "BTC_TX_LIST_ERROR",
			"message": "erro ao listar transações: " + err.Error(),
		})
		return
	}
	if txs == nil {
		txs = []bitcoin.BTCTransaction{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"transactions": txs,
		"count":        len(txs),
	})
}

// ─── GET /api/mobile/btc/transactions/{id} ───────────────────────────────────
// Busca uma transação BTC pelo txid.

func (s *Server) handleBTCGetTransaction(w http.ResponseWriter, r *http.Request) {
	svc := s.btcSvcOrErr(w)
	if svc == nil {
		return
	}
	txid := strings.TrimSpace(r.PathValue("id"))
	if txid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": "MISSING_TXID", "message": "txid obrigatório",
		})
		return
	}
	tx, err := svc.GetTransactionByTxid(r.Context(), txid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"code": "BTC_TX_ERROR", "message": err.Error(),
		})
		return
	}
	if tx == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"code": "TX_NOT_FOUND", "message": "transação não encontrada",
		})
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

// ─── POST /api/mobile/btc/send ───────────────────────────────────────────────
// Envia BTC para um endereço externo. Requer idempotency key no header.
//
// Body:
//
//	{
//	  "to_address":    "tb1q...",
//	  "amount_sats":  10000,
//	  "fee_rate":     5        // opcional, sat/vbyte
//	}

func (s *Server) handleBTCSend(w http.ResponseWriter, r *http.Request) {
	svc := s.btcSvcOrErr(w)
	if svc == nil {
		return
	}

	// ── Feature flags de segurança operacional ────────────────────────────────
	cfg := svc.Config()
	if cfg.EmergencyLockdown {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "BTC_EMERGENCY_LOCKDOWN",
			"message": "envios Bitcoin temporariamente suspensos por lockdown operacional",
		})
		return
	}
	if !cfg.WithdrawalsEnabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "BTC_WITHDRAWALS_DISABLED",
			"message": "saques Bitcoin não estão habilitados neste ambiente (BTC_WITHDRAWALS_ENABLED=false)",
		})
		return
	}

	var body struct {
		ToAddress  string `json:"to_address"`
		AmountSats int64  `json:"amount_sats"`
		FeeRate    int64  `json:"fee_rate"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": "INVALID_BODY", "message": "corpo inválido: " + err.Error(),
		})
		return
	}

	if strings.TrimSpace(body.ToAddress) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": "MISSING_ADDRESS", "message": "to_address é obrigatório",
		})
		return
	}
	if body.AmountSats <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code": "INVALID_AMOUNT", "message": "amount_sats deve ser > 0",
		})
		return
	}

	uid := userIDFromCtx(r)
	idemKey := idempotencyKeyFromCtx(r.Context())

	req := bitcoin.SendRequest{
		UserID:         uid,
		ToAddress:      strings.TrimSpace(body.ToAddress),
		AmountSats:     body.AmountSats,
		FeeRateSatVB:   body.FeeRate,
		IdempotencyKey: idemKey,
	}

	result, err := svc.Send(r.Context(), req)
	if err != nil {
		status := http.StatusInternalServerError
		code := "BTC_SEND_ERROR"
		switch {
		case errors.Is(err, bitcoin.ErrInsufficientFunds):
			status = http.StatusUnprocessableEntity
			code = "INSUFFICIENT_FUNDS"
		case errors.Is(err, bitcoin.ErrDustOutput):
			status = http.StatusUnprocessableEntity
			code = "DUST_OUTPUT"
		case errors.Is(err, bitcoin.ErrInvalidAddress), errors.Is(err, bitcoin.ErrWrongNetwork):
			status = http.StatusBadRequest
			code = "INVALID_ADDRESS"
		case errors.Is(err, bitcoin.ErrNoSeed):
			status = http.StatusServiceUnavailable
			code = "SIGNING_NOT_CONFIGURED"
		case errors.Is(err, bitcoin.ErrMaxSendExceeded):
			status = http.StatusUnprocessableEntity
			code = "MAX_SEND_EXCEEDED"
		case errors.Is(err, bitcoin.ErrDailyLimitExceeded):
			status = http.StatusUnprocessableEntity
			code = "DAILY_LIMIT_EXCEEDED"
		case errors.Is(err, bitcoin.ErrIdempotencyConflict):
			// Mesma chave de idempotência mas payload diferente — conflito real
			status = http.StatusConflict
			code = "IDEMPOTENCY_CONFLICT"
		}
		writeJSON(w, status, map[string]any{
			"code":    code,
			"message": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, result)
}
