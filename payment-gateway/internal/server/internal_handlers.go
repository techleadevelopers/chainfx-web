package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	"payment-gateway/internal/email"
	"payment-gateway/internal/workers"
)

func (s *Server) handleInternalSweep(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura inválida"})
		return
	}
	var req struct {
		ChildIndex int     `json:"childIndex"`
		ToAddr     string  `json:"toAddr"`
		Amount     float64 `json:"amount"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Amount <= 0 || req.ToAddr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload inválido"})
		return
	}
	sweep, err := s.db.CreateSweep(r.Context(), req.ChildIndex, s.cfg.TreasuryHot, req.ToAddr, req.Amount, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "sweepId": sweep.ID})
}

func (s *Server) handleInternalM2MSettled(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	var req struct {
		IntentID     string `json:"intent_id"`
		SettlementID string `json:"settlement_id"`
		ProviderID   string `json:"provider_id"`
		Status       string `json:"status"`
		ReceiptURL   string `json:"receipt_url"`
		ReceiptNote  string `json:"receipt_note"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	req.IntentID = strings.TrimSpace(req.IntentID)
	settlementID := firstNonEmpty(strings.TrimSpace(req.SettlementID), strings.TrimSpace(req.ProviderID))
	status := firstNonEmpty(strings.TrimSpace(req.Status), "paid")
	receiptURL := strings.TrimSpace(req.ReceiptURL)
	receiptNote := strings.TrimSpace(req.ReceiptNote)
	if req.IntentID == "" || settlementID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "intent_id e settlement_id/provider_id sao obrigatorios"})
		return
	}
	locked, tx, err := s.db.AcquireM2MSettlementLock(r.Context(), req.IntentID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !locked || tx == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "intent em processamento por outro worker"})
		return
	}
	if err := s.db.MarkM2MSettledWithReceipt(r.Context(), tx, req.IntentID, settlementID, status, receiptURL, receiptNote); err != nil {
		writeError(w, err)
		return
	}
	if s.workers != nil && s.workers.Bus != nil {
		s.workers.Bus.Publish(workers.Event{
			Type:    "m2m.settlement.done",
			OrderID: req.IntentID,
			Payload: map[string]any{
				"intent_id":     req.IntentID,
				"settlement_id": settlementID,
				"status":        status,
				"receipt_url":   receiptURL,
				"receipt_note":  receiptNote,
				"rail":          "credit_card",
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "intent_id": req.IntentID, "status": "settled"})
}

func (s *Server) handleEmailTest(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	var req struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
		HTML    string `json:"html"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON inválido"})
		return
	}
	if req.Subject == "" {
		req.Subject = "ChainFx Payments - teste SMTP"
	}
	if req.Body == "" {
		req.Body = "Serviço de email operacional ativo."
	}
	if err := s.email.Send(email.Message{To: req.To, Subject: req.Subject, Body: req.Body, HTMLBody: req.HTML}); err != nil {
		slog.Error("internal email test failed", "request_id", requestID(r), "error", err)
		writeAPIError(w, r, http.StatusBadGateway, "EMAIL_PROVIDER_ERROR", "Email provider unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMarketingEmail(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !validHMAC(s.cfg.SignerHmacSecret, raw, r.Header.Get("x-internal-hmac")) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "assinatura invalida"})
		return
	}
	var req struct {
		Emails         []string `json:"emails"`
		Subject        string   `json:"subject"`
		Headline       string   `json:"headline"`
		Intro          string   `json:"intro"`
		Body           string   `json:"body"`
		CTA            string   `json:"cta"`
		CTAURL         string   `json:"ctaUrl"`
		UnsubscribeURL string   `json:"unsubscribeUrl"`
		Source         string   `json:"source"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	if len(req.Emails) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "lista de emails vazia"})
		return
	}
	if len(req.Emails) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "limite de 500 emails por envio"})
		return
	}
	campaign := email.MarketingCampaign{
		Subject:     req.Subject,
		Headline:    req.Headline,
		Intro:       req.Intro,
		Body:        req.Body,
		CTA:         req.CTA,
		CTAURL:      req.CTAURL,
		Unsubscribe: req.UnsubscribeURL,
	}
	var sent, skipped int
	var failures []string
	seen := map[string]bool{}
	for _, rawEmail := range req.Emails {
		addr, err := mail.ParseAddress(strings.TrimSpace(rawEmail))
		if err != nil || addr.Address == "" {
			skipped++
			continue
		}
		to := strings.ToLower(addr.Address)
		if seen[to] {
			skipped++
			continue
		}
		seen[to] = true
		if err := s.db.UpsertMarketingContact(r.Context(), to, firstNonEmpty(req.Source, "internal_campaign")); err != nil {
			failures = append(failures, to+": "+err.Error())
			continue
		}
		if err := s.email.SendMarketing(to, campaign); err != nil {
			failures = append(failures, to+": "+err.Error())
			continue
		}
		sent++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       len(failures) == 0,
		"sent":     sent,
		"skipped":  skipped,
		"failed":   len(failures),
		"failures": failures,
	})
}
