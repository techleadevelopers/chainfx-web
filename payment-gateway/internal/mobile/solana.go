package mobile

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"payment-gateway/internal/solana"
)

func (s *Server) solanaSvcOrErr(w http.ResponseWriter) *solana.Service {
	if s == nil || s.solSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "SOLANA_DISABLED",
			"message": "suporte a Solana nao esta habilitado neste servidor",
		})
		return nil
	}
	return s.solSvc
}

func (s *Server) handleSolanaBalance(w http.ResponseWriter, r *http.Request) {
	svc := s.solanaSvcOrErr(w)
	if svc == nil {
		return
	}
	bal, err := svc.GetBalance(r.Context(), userIDFromCtx(r))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_BALANCE_ERROR", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, bal)
}

func (s *Server) handleSolanaFeeEstimate(w http.ResponseWriter, r *http.Request) {
	svc := s.solanaSvcOrErr(w)
	if svc == nil {
		return
	}
	to := strings.TrimSpace(r.URL.Query().Get("to_address"))
	if to == "" {
		to = strings.TrimSpace(r.URL.Query().Get("to"))
	}
	amount := int64(1)
	if raw := strings.TrimSpace(r.URL.Query().Get("amount_lamports")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			amount = parsed
		}
	}
	if to == "" {
		addr, err := svc.GetOrCreateAddress(r.Context(), userIDFromCtx(r))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_ADDRESS_ERROR", "message": err.Error()})
			return
		}
		to = addr.Address
	}
	fee, err := svc.EstimateFee(r.Context(), userIDFromCtx(r), to, amount)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "SOL_FEE_ERROR", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, fee)
}

func (s *Server) handleSolanaTransactions(w http.ResponseWriter, r *http.Request) {
	svc := s.solanaSvcOrErr(w)
	if svc == nil {
		return
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	txs, err := svc.ListUserTransactions(r.Context(), userIDFromCtx(r), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_TX_LIST_ERROR", "message": err.Error()})
		return
	}
	if txs == nil {
		txs = []solana.Transaction{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": txs, "count": len(txs)})
}

func (s *Server) handleSolanaSend(w http.ResponseWriter, r *http.Request) {
	svc := s.solanaSvcOrErr(w)
	if svc == nil {
		return
	}
	var body struct {
		ToAddress      string  `json:"to_address"`
		To             string  `json:"to"`
		AmountLamports int64   `json:"amount_lamports"`
		AmountSOL      float64 `json:"amount_sol"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_BODY", "message": err.Error()})
		return
	}
	to := strings.TrimSpace(firstNonEmptyStr(body.ToAddress, body.To))
	lamports := body.AmountLamports
	if lamports <= 0 && body.AmountSOL > 0 {
		lamports = int64(body.AmountSOL * float64(solana.LamportsPerSOL))
	}
	if to == "" || lamports <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_SOL_SEND", "message": "to_address e amount_lamports obrigatorios"})
		return
	}
	result, err := svc.Send(r.Context(), solana.SendRequest{
		UserID:         userIDFromCtx(r),
		ToAddress:      to,
		AmountLamports: lamports,
		IdempotencyKey: idempotencyKeyFromCtx(r.Context()),
	})
	if err != nil {
		status := http.StatusInternalServerError
		code := "SOL_SEND_ERROR"
		switch {
		case errors.Is(err, solana.ErrWithdrawalsDisabled):
			status = http.StatusServiceUnavailable
			code = "SOL_WITHDRAWALS_DISABLED"
		case errors.Is(err, solana.ErrSigningNotConfigured):
			status = http.StatusServiceUnavailable
			code = "SOL_SIGNING_NOT_CONFIGURED"
		case errors.Is(err, solana.ErrInvalidAddress):
			status = http.StatusBadRequest
			code = "INVALID_SOL_ADDRESS"
		case errors.Is(err, solana.ErrInsufficientFunds):
			status = http.StatusUnprocessableEntity
			code = "INSUFFICIENT_FUNDS"
		case errors.Is(err, solana.ErrMaxSendExceeded):
			status = http.StatusUnprocessableEntity
			code = "MAX_SEND_EXCEEDED"
		case errors.Is(err, solana.ErrIdempotencyConflict):
			status = http.StatusConflict
			code = "IDEMPOTENCY_CONFLICT"
		}
		writeJSON(w, status, map[string]any{"code": code, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
