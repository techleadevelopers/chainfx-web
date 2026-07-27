package server

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"payment-gateway/internal/database"
	"payment-gateway/internal/models"
	"payment-gateway/internal/settlement"
	"payment-gateway/internal/workers"
)

func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		TxHash string  `json:"txHash"`
		Amount float64 `json:"amount"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.TxHash == "" || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	id := r.PathValue("id")
	if idem := r.Header.Get("x-idempotency-key"); idem != "" {
		exists, _ := s.db.HasEvent(r.Context(), id, "idempotency", "key", idem)
		if exists {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
			return
		}
		_ = s.db.AddEvent(r.Context(), id, "idempotency", map[string]any{"requestId": requestID(r), "key": idem, "endpoint": "deposit"})
	}
	order, err := s.db.GetOrder(r.Context(), id)
	if err != nil || order == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "ordem não encontrada"})
		return
	}
	if order.Status != models.StatusAguardandoDeposito {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "status atual não permite depósito"})
		return
	}
	usedElsewhere, err := s.db.HasDepositTxForOtherOrder(r.Context(), id, req.TxHash)
	if err != nil {
		writeError(w, err)
		return
	}
	if usedElsewhere {
		reason := "deposit tx ja vinculada a outra ordem"
		_ = s.db.UpdateOrderStatus(r.Context(), id, string(models.StatusIncidenteValidacao), map[string]any{"requestId": requestID(r), "depositTx": req.TxHash, "error": reason})
		_ = s.db.OpenOrderIncident(r.Context(), id, "sell_deposit_tx_replay", "critical", reason, map[string]any{"requestId": requestID(r), "depositTx": req.TxHash})
		writeJSON(w, http.StatusConflict, map[string]any{"error": reason})
		return
	}
	if err := s.db.UpdateOrderStatus(r.Context(), id, "pago", map[string]any{"requestId": requestID(r), "depositTx": req.TxHash, "depositAmount": req.Amount}); err != nil {
		writeError(w, err)
		return
	}
	s.workers.Bus.Publish(workers.Event{Type: "onchain.detected", OrderID: id, Payload: map[string]any{"tx_hash": req.TxHash, "amount_usdt": req.Amount}})
	s.workers.Bus.Publish(workers.Event{Type: "payout.requested", OrderID: id})
	s.email.NotifyOps("ChainFx: depósito detectado", fmt.Sprintf("Ordem %s recebeu depósito %s no valor %.8f USDT.", id, req.TxHash, req.Amount))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePayout(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		ProviderID string `json:"providerId"`
		Status     string `json:"status"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	status := "erro"
	extra := map[string]any{"requestId": requestID(r), "error": req.Error}
	if strings.HasPrefix(strings.ToLower(req.Status), "conclu") {
		status = "concluida"
		extra = map[string]any{"requestId": requestID(r), "txHash": req.ProviderID}
	}
	if err := s.db.UpdateOrderStatus(r.Context(), r.PathValue("id"), status, extra); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePixWebhook(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	secret := defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret)
	if secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "PIX_WEBHOOK_SECRET nao configurado — endpoint desabilitado"})
		return
	}
	if !validHMAC(secret, raw, r.Header.Get("x-efi-signature")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		OrderID    string `json:"orderId"`
		Status     string `json:"status"`
		ProviderID string `json:"providerId"`
		Error      string `json:"error"`
		PayerCPF   string `json:"payerCpf"`
		Pagador    struct {
			CPF string `json:"cpf"`
		} `json:"pagador"`
		GNExtras struct {
			Pagador struct {
				CPF string `json:"cpf"`
			} `json:"pagador"`
		} `json:"gnExtras"`
		Pix []struct {
			EndToEndID string `json:"endToEndId"`
			TxID       string `json:"txid"`
			Pagador    struct {
				CPF string `json:"cpf"`
			} `json:"pagador"`
			GNExtras struct {
				Pagador struct {
					CPF string `json:"cpf"`
				} `json:"pagador"`
			} `json:"gnExtras"`
		} `json:"pix"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.OrderID == "" || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	if len(req.Pix) > 0 {
		req.ProviderID = firstNonEmpty(req.ProviderID, req.Pix[0].EndToEndID, req.Pix[0].TxID)
		req.PayerCPF = firstNonEmpty(req.PayerCPF, req.Pix[0].Pagador.CPF, req.Pix[0].GNExtras.Pagador.CPF)
	}
	req.PayerCPF = firstNonEmpty(req.PayerCPF, req.Pagador.CPF, req.GNExtras.Pagador.CPF)
	if strings.HasPrefix(strings.ToLower(req.Status), "conclu") && req.PayerCPF != "" {
		matchStatus, err := s.db.OrderPixCPFMatchStatus(r.Context(), req.OrderID, req.PayerCPF)
		if err != nil {
			writeError(w, err)
			return
		}
		if matchStatus == database.DocumentMatchMismatch {
			reason := "CPF informado na ordem SELL diverge do CPF do pagador PIX"
			payload := map[string]any{
				"requestId":   requestID(r),
				"providerId":  req.ProviderID,
				"rule":        "sell_pix_cpf_mismatch",
				"matchStatus": string(matchStatus),
				"action":      "manual_review_required_no_auto_refund",
			}
			if err := s.db.UpdateOrderStatus(r.Context(), req.OrderID, string(models.StatusIncidenteValidacao), map[string]any{"requestId": requestID(r), "error": reason}); err != nil {
				writeError(w, err)
				return
			}
			if err := s.db.OpenOrderIncident(r.Context(), req.OrderID, "sell_pix_cpf_mismatch", "critical", reason, payload); err != nil {
				writeError(w, err)
				return
			}
			s.email.NotifyOps("ChainFX: incidente SELL CPF PIX", fmt.Sprintf("Ordem %s bloqueada para revisao manual. Motivo: %s. ProviderID: %s", req.OrderID, reason, req.ProviderID))
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "incident": true, "status": string(models.StatusIncidenteValidacao)})
			return
		}
	}
	status := "erro"
	extra := map[string]any{"requestId": requestID(r), "error": req.Error}
	if strings.HasPrefix(strings.ToLower(req.Status), "conclu") {
		status = "concluida"
		extra = map[string]any{"requestId": requestID(r), "txHash": req.ProviderID}
	}
	if err := s.db.UpdateOrderStatus(r.Context(), req.OrderID, status, extra); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePixWebhookBuy(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	secret := defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret)
	// C-02: fail-closed — if no secret is configured, the endpoint is disabled.
	// An unauthenticated PIX webhook can trigger free USDT settlement.
	if secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "PIX_WEBHOOK_SECRET nao configurado — endpoint desabilitado"})
		return
	}
	signature := firstNonEmpty(r.Header.Get("x-efi-signature"), r.Header.Get("x-chainfx-signature"))
	queryHMAC := r.URL.Query().Get("hmac")
	if secret != "" && signature != "" && !validHMAC(secret, raw, signature) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	if secret != "" && signature == "" && queryHMAC != "" && !hmac.Equal([]byte(queryHMAC), []byte(secret)) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "hmac invalido"})
		return
	}
	// SECURITY: once an operator has configured a Pix webhook secret, every
	// request must carry a valid signature or hmac param — in every
	// environment, not only production. Gating this on IsProduction() left
	// staging/dev/test deployments (which still hold real Efí credentials
	// and can move real settlement state) open to a fully unauthenticated
	// forged settlement callback. Leaving the secret unset (local-only,
	// never real credentials) remains the only way to run unauthenticated.
	if secret != "" && signature == "" && queryHMAC == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "webhook sem autenticacao adicional"})
		return
	}
	// PSP abstraction path: when a Router is wired (cmd/api/main.go), normalise
	// the body through the active provider adapter and process every PIX
	// payment event independently — Efí batches multiple events per POST, and
	// each one may settle a different buy order.
	if s.pspRouter != nil {
		statusCode, body := s.handlePixWebhookBuyViaPSP(r, raw, secret)
		writeJSON(w, statusCode, body)
		return
	}

	var req struct {
		BuyID      string `json:"buyId"`
		Status     string `json:"status"`
		ProviderID string `json:"providerId"`
		Error      string `json:"error"`
		Pix        []struct {
			EndToEndID string `json:"endToEndId"`
			TxID       string `json:"txid"`
			Valor      string `json:"valor"`
			Horario    string `json:"horario"`
		} `json:"pix"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	if len(req.Pix) > 0 {
		req.Status = firstNonEmpty(req.Status, "CONCLUIDA")
		req.ProviderID = firstNonEmpty(req.ProviderID, req.Pix[0].EndToEndID, req.Pix[0].TxID)
		req.BuyID = firstNonEmpty(req.BuyID, buyIDFromEfiTxID(req.Pix[0].TxID))
	}
	if req.BuyID == "" || req.ProviderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload sem buyId/txid"})
		return
	}
	status := settlement.PixWebhookStatus(firstNonEmpty(req.Status, "CONCLUIDA"))
	extra := map[string]any{"requestId": requestID(r), "error": req.Error}
	if settlement.ShouldPublishBuyPaid(status) {
		extra = map[string]any{"requestId": requestID(r), "providerPaymentId": req.ProviderID}
	}
	duplicate, err := s.db.ApplyBuyProviderWebhook(r.Context(), req.BuyID, req.ProviderID, req.Status, status, extra)
	if err != nil {
		writeError(w, err)
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if settlement.ShouldPublishBuyPaid(status) {
		s.workers.Bus.Publish(workers.Event{Type: "buy.paid", OrderID: req.BuyID, Payload: map[string]any{"providerId": req.ProviderID}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleEfiPixSendWebhook(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	secret := defaultString(s.cfg.PixWebhookSecret, s.cfg.WebhookSecret)
	if secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "PIX_WEBHOOK_SECRET nao configurado - endpoint desabilitado"})
		return
	}
	signature := firstNonEmpty(r.Header.Get("x-efi-signature"), r.Header.Get("x-chainfx-signature"))
	queryHMAC := r.URL.Query().Get("hmac")
	if signature != "" && !validHMAC(secret, raw, signature) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	if signature == "" && queryHMAC != "" && !hmac.Equal([]byte(queryHMAC), []byte(secret)) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "hmac invalido"})
		return
	}
	if signature == "" && queryHMAC == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "webhook sem autenticacao adicional"})
		return
	}

	var req struct {
		IDEnvio     string `json:"idEnvio"`
		E2EID       string `json:"e2eId"`
		EndToEndID  string `json:"endToEndId"`
		Status      string `json:"status"`
		Valor       string `json:"valor"`
		PixEnviados []struct {
			IDEnvio    string `json:"idEnvio"`
			E2EID      string `json:"e2eId"`
			EndToEndID string `json:"endToEndId"`
			Status     string `json:"status"`
			Valor      string `json:"valor"`
		} `json:"pixEnviados"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	if len(req.PixEnviados) > 0 {
		req.IDEnvio = firstNonEmpty(req.IDEnvio, req.PixEnviados[0].IDEnvio)
		req.E2EID = firstNonEmpty(req.E2EID, req.PixEnviados[0].E2EID, req.PixEnviados[0].EndToEndID)
		req.Status = firstNonEmpty(req.Status, req.PixEnviados[0].Status)
		req.Valor = firstNonEmpty(req.Valor, req.PixEnviados[0].Valor)
	}
	req.E2EID = firstNonEmpty(req.E2EID, req.EndToEndID)
	if req.IDEnvio == "" && req.E2EID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload sem idEnvio/e2eId"})
		return
	}
	if req.Status == "" {
		req.Status = "SUBMITTED"
	}
	eventPayload := map[string]any{
		"source":    "webhook",
		"idEnvio":   req.IDEnvio,
		"e2eId":     req.E2EID,
		"status":    req.Status,
		"requestId": requestID(r),
	}
	if amountMinor := parseWebhookBRLMinor(req.Valor); amountMinor > 0 {
		eventPayload["amount_brl_minor"] = amountMinor
	}
	duplicate, settlement, err := s.db.ApplyMerchantSettlementProviderEvent(r.Context(), "efi", req.IDEnvio, req.E2EID, req.Status, eventPayload)
	if err != nil {
		writeError(w, err)
		return
	}
	if settlement == nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "unmatched": true})
		return
	}
	if settlement.Status == database.MerchantSettlementStatusConfirmed && s.workers != nil && s.workers.Bus != nil {
		s.workers.Bus.Publish(workers.Event{Type: "nfc.settlement.confirmed", OrderID: settlement.AuthorizationID, Payload: map[string]any{
			"settlement_id":      settlement.ID,
			"provider_reference": settlement.ProviderReference,
			"provider_status":    settlement.ProviderStatus,
			"duplicate":          duplicate,
			"source":             "webhook",
		}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": duplicate, "settlement_id": settlement.ID, "status": settlement.Status})
}

func parseWebhookBRLMinor(value string) int64 {
	value = strings.TrimSpace(strings.ReplaceAll(value, ",", "."))
	if value == "" {
		return 0
	}
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil || amount <= 0 {
		return 0
	}
	return int64(math.Round(amount * 100))
}

// handlePixWebhookBuyViaPSP parses raw through the wired PSP Router and applies
// every normalised PixWebhookPayload as an independent buy-order settlement.
// It never mutates HTTP state itself — callers write the returned status/body —
// so it stays trivially testable without an httptest.Recorder.
func (s *Server) handlePixWebhookBuyViaPSP(r *http.Request, raw []byte, secret string) (statusCode int, body map[string]any) {
	payloads, err := s.pspRouter.ParseWebhookAll(r.Context(), raw, secret)
	if err != nil {
		return http.StatusBadRequest, map[string]any{"error": "payload invalido: " + err.Error()}
	}

	anyApplied := false
	for _, p := range payloads {
		buyID := buyIDFromEfiTxID(p.TXID)
		if strings.TrimSpace(buyID) == "" {
			buyID = p.EndToEndID
		}
		providerID := firstNonEmpty(p.EndToEndID, p.TXID)
		if buyID == "" || providerID == "" {
			continue // no way to correlate this event to an order; skip rather than fail the whole batch
		}

		status := settlement.PixWebhookStatus("CONCLUIDA")
		extra := map[string]any{
			"requestId":         requestID(r),
			"providerPaymentId": providerID,
			"pspProvider":       p.Provider,
			"amountBRL":         p.AmountBRL,
		}
		duplicate, err := s.db.ApplyBuyProviderWebhook(r.Context(), buyID, providerID, "CONCLUIDA", status, extra)
		if err != nil {
			return http.StatusInternalServerError, map[string]any{"error": err.Error()}
		}
		if duplicate {
			continue
		}
		anyApplied = true
		if settlement.ShouldPublishBuyPaid(status) {
			s.workers.Bus.Publish(workers.Event{Type: "buy.paid", OrderID: buyID, Payload: map[string]any{"providerId": providerID}})
		}
	}

	return http.StatusOK, map[string]any{"ok": true, "processed": len(payloads), "applied": anyApplied}
}
func (s *Server) handleEfiChargesWebhookBuy(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	token := ""
	var req struct {
		Notification string `json:"notification"`
		Token        string `json:"token"`
	}
	if len(raw) > 0 && strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(raw, &req)
		token = firstNonEmpty(req.Notification, req.Token)
	}
	if token == "" {
		form, _ := urlParseQueryLike(raw)
		token = firstNonEmpty(form.Get("notification"), form.Get("token"))
	}
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "notification token obrigatorio"})
		return
	}
	client, err := s.efiHTTPClient()
	if err != nil {
		writeError(w, err)
		return
	}
	accessToken, err := s.efiBillingAccessToken(r.Context(), client)
	if err != nil {
		writeError(w, err)
		return
	}
	notification, err := s.efiBillingRequest(r.Context(), client, accessToken, http.MethodGet, "/v1/notification/"+token, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	latest := latestEfiNotificationEvent(notification)
	buyID := mapString(latest, "custom_id")
	statusObj := nestedMap(latest, "status")
	efiStatus := mapString(statusObj, "current")
	chargeID := mapString(nestedMap(latest, "identifiers"), "charge_id")
	if buyID == "" || chargeID == "" || efiStatus == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "notificacao Efí sem custom_id/charge_id/status"})
		return
	}
	status := efiChargeStatusToBuyStatus(efiStatus)
	extra := map[string]any{
		"requestId":            requestID(r),
		"providerPaymentId":    chargeID,
		"efiChargeStatus":      efiStatus,
		"efiNotificationToken": token,
	}
	if status == "erro" {
		extra["error"] = "Efí charge status: " + efiStatus
	}
	duplicate, err := s.db.ApplyBuyProviderWebhook(r.Context(), buyID, chargeID+"-"+efiStatus, efiStatus, status, extra)
	if err != nil {
		writeError(w, err)
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
		return
	}
	if settlement.ShouldPublishBuyPaid(status) {
		s.workers.Bus.Publish(workers.Event{Type: "buy.paid", OrderID: buyID, Payload: map[string]any{"providerId": chargeID, "rail": "credit_card", "provider": "efi"}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "buyId": buyID, "status": status, "efiStatus": efiStatus})
}

func latestEfiNotificationEvent(notification map[string]any) map[string]any {
	data, _ := notification["data"].([]any)
	if len(data) == 0 {
		return nil
	}
	latest, _ := data[len(data)-1].(map[string]any)
	return latest
}

func efiChargeStatusToBuyStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "paid":
		return "pago_fiat"
	case "canceled", "cancelled", "unpaid", "refunded", "contested":
		return "erro"
	default:
		return "aguardando_credit_card"
	}
}

func urlParseQueryLike(raw []byte) (url.Values, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return url.Values{}, nil
	}
	return url.ParseQuery(text)
}
