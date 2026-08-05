package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"payment-gateway/internal/email"
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
		slog.Error("Solana balance error", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_BALANCE_ERROR", "message": "Nao foi possivel buscar o saldo Solana agora."})
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
			slog.Error("Solana address error", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_ADDRESS_ERROR", "message": "Nao foi possivel obter o endereco Solana agora."})
			return
		}
		to = addr.Address
	}
	fee, err := svc.EstimateFee(r.Context(), userIDFromCtx(r), to, amount)
	if err != nil {
		slog.Warn("Solana fee estimate rejected", "err", err)
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "SOL_FEE_ERROR", "message": "Nao foi possivel estimar a fee Solana agora."})
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
		slog.Error("Solana transaction list error", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_TX_LIST_ERROR", "message": "Nao foi possivel listar transacoes Solana agora."})
		return
	}
	if txs == nil {
		txs = []solana.Transaction{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": txs, "count": len(txs)})
}

func (s *Server) handleSolanaTransaction(w http.ResponseWriter, r *http.Request) {
	svc := s.solanaSvcOrErr(w)
	if svc == nil {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_SOL_TX", "message": "id obrigatorio"})
		return
	}
	tx, err := svc.GetUserTransaction(r.Context(), userIDFromCtx(r), id)
	if err != nil {
		slog.Error("Solana transaction lookup error", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"code": "SOL_TX_ERROR", "message": "Nao foi possivel buscar a transacao Solana agora."})
		return
	}
	if tx == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "SOL_TX_NOT_FOUND", "message": "operacao SOL nao encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *Server) handleSolanaSend(w http.ResponseWriter, r *http.Request) {
	svc := s.solanaSvcOrErr(w)
	if svc == nil {
		return
	}
	var body struct {
		ToAddress      string          `json:"to_address"`
		To             string          `json:"to"`
		AmountLamports json.RawMessage `json:"amount_lamports"`
		AmountSOL      json.RawMessage `json:"amount_sol"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_BODY", "message": "corpo invalido"})
		return
	}
	to := strings.TrimSpace(firstNonEmptyStr(body.ToAddress, body.To))
	if len(body.AmountSOL) > 0 && strings.TrimSpace(string(body.AmountSOL)) != "null" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_SOL_AMOUNT", "message": "use amount_lamports inteiro; amount_sol decimal nao e aceito"})
		return
	}
	lamports, amountErr := parseSolLamportsJSON(body.AmountLamports)
	if to == "" || lamports <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_SOL_SEND", "message": "to_address e amount_lamports obrigatorios"})
		return
	}
	if amountErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": "INVALID_SOL_AMOUNT", "message": amountErr.Error()})
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
		slog.Warn("Solana send rejected", "code", code, "err", err)
		writeJSON(w, status, map[string]any{"code": code, "message": solanaUserMessage(code)})
		return
	}
	s.sendMobileTransactionEmailAsync(userIDFromCtx(r), "Solana enviada pela ChainFX", email.TransactionReceipt{
		Title:  "Solana enviada",
		Intro:  "Sua transferencia Solana foi transmitida com sucesso.",
		CTA:    "Ver Scan",
		CTAURL: mobileScanURL("SOLANA", result.Signature),
		Details: []email.TransactionDetail{
			{Label: "Ativo", Value: "SOL"},
			{Label: "Valor", Value: fmt.Sprintf("%.9f SOL", float64(result.AmountLamports)/1_000_000_000)},
			{Label: "Taxa", Value: fmt.Sprintf("%d lamports", result.FeeLamports)},
			{Label: "Destino", Value: to, CopyHint: true},
			{Label: "Assinatura", Value: result.Signature, CopyHint: true},
			{Label: "Status", Value: result.Status},
			{Label: "Enviado em", Value: mobileNowText()},
		},
	})
	writeJSON(w, http.StatusAccepted, result)
}

func parseSolLamportsJSON(raw json.RawMessage) (int64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, errors.New("amount_lamports obrigatorio")
	}
	var asString string
	if strings.HasPrefix(text, `"`) {
		if err := json.Unmarshal(raw, &asString); err != nil {
			return 0, err
		}
		text = strings.TrimSpace(asString)
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(text) {
		return 0, errors.New("amount_lamports deve ser inteiro em lamports")
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("amount_lamports invalido")
	}
	return value, nil
}

func solanaUserMessage(code string) string {
	switch code {
	case "SOL_WITHDRAWALS_DISABLED":
		return "Envio Solana indisponivel no momento."
	case "SOL_SIGNING_NOT_CONFIGURED":
		return "Envio Solana indisponivel no momento."
	case "INVALID_SOL_ADDRESS":
		return "Endereco Solana invalido."
	case "INSUFFICIENT_FUNDS":
		return "Saldo insuficiente."
	case "MAX_SEND_EXCEEDED":
		return "Valor acima do limite por transacao."
	case "IDEMPOTENCY_CONFLICT":
		return "Esta operacao ja foi enviada com dados diferentes."
	default:
		return "Nao foi possivel enviar Solana agora."
	}
}
