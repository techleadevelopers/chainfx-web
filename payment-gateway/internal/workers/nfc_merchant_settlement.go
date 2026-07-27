package workers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/certutil"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
)

const (
	nfcSettlementPollSec = 5
	nfcSettlementMaxRuns = 5
)

type NFCMerchantSettlementWorker struct {
	bus    *EventBus
	db     *database.DB
	cfg    *config.Config
	client *http.Client
	dlq    *DeadLetterQueue
	sem    chan struct{}
}

type efiPixSendResult struct {
	IDEnvio        string
	E2EID          string
	Status         string
	AmountBRLMinor int64
}

func NewNFCMerchantSettlementWorker(bus *EventBus, db *database.DB, cfg *config.Config) *NFCMerchantSettlementWorker {
	return &NFCMerchantSettlementWorker{
		bus:    bus,
		db:     db,
		cfg:    cfg,
		client: nfcSettlementHTTPClient(cfg),
		dlq:    NewPersistentDLQ(db, 1000),
		sem:    make(chan struct{}, 4),
	}
}

func (w *NFCMerchantSettlementWorker) Start(ctx context.Context) {
	captureChan := w.bus.Subscribe("nfc.capture.completed")
	ticker := time.NewTicker(nfcSettlementPollSec * time.Second)
	defer ticker.Stop()
	w.dlq.StartPeriodicLog(ctx, 5*time.Minute)
	slog.Info("NFCMerchantSettlementWorker iniciado", "mode", w.mode())

	for {
		select {
		case <-ctx.Done():
			slog.Info("NFCMerchantSettlementWorker: encerrando")
			return
		case ev, ok := <-captureChan:
			if !ok {
				return
			}
			w.handleCaptureEvent(ctx, ev)
		case <-ticker.C:
			if w.automatic() {
				w.sweepDue(ctx)
			}
		}
	}
}

func (w *NFCMerchantSettlementWorker) handleCaptureEvent(ctx context.Context, ev Event) {
	settlementID, _ := ev.Payload["settlement_id"].(string)
	if settlementID == "" {
		return
	}
	if !w.automatic() {
		_ = w.db.MarkMerchantSettlementManualRequired(ctx, settlementID, "NFC_SETTLEMENT_MODE=manual")
		w.bus.Publish(Event{
			Type:    "nfc.settlement.manual_required",
			OrderID: ev.OrderID,
			Payload: map[string]any{
				"authorization_id": ev.OrderID,
				"settlement_id":    settlementID,
				"mode":             "manual",
			},
		})
		slog.Info("NFC settlement aguardando payout manual", "authorization_id", ev.OrderID, "settlement_id", settlementID)
		return
	}
	w.dispatch(ctx, settlementID)
}

func (w *NFCMerchantSettlementWorker) sweepDue(ctx context.Context) {
	settlements, err := w.db.GetDueMerchantSettlements(ctx, 50)
	if err != nil {
		slog.Error("NFC settlement: erro ao listar pendentes", "err", err)
		return
	}
	for _, settlement := range settlements {
		w.dispatch(ctx, settlement.ID)
	}
}

func (w *NFCMerchantSettlementWorker) dispatch(ctx context.Context, settlementID string) {
	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	go func() {
		defer func() {
			<-w.sem
			if r := recover(); r != nil {
				slog.Error("NFC settlement: panic", "recover", r, "settlement_id", settlementID)
			}
		}()
		w.processOne(ctx, settlementID)
	}()
}

func (w *NFCMerchantSettlementWorker) processOne(ctx context.Context, settlementID string) {
	start := time.Now()
	settlement, claimed, err := w.db.ClaimMerchantSettlement(ctx, settlementID)
	if err != nil {
		slog.Error("NFC settlement: claim falhou", "settlement_id", settlementID, "err", err)
		w.dlq.Push(Event{Type: "nfc.settlement.failed", OrderID: settlementID}, 1, err.Error())
		return
	}
	if !claimed || settlement == nil {
		return
	}
	if settlement.RetryCount >= nfcSettlementMaxRuns {
		_ = w.db.MarkMerchantSettlementManualReview(ctx, settlement.ID, "max settlement attempts exceeded")
		return
	}
	if strings.TrimSpace(settlement.TargetPixKey) == "" {
		_ = w.db.MarkMerchantSettlementManualReview(ctx, settlement.ID, "merchant settlement Pix key missing")
		w.publishSettlementFailure(settlement, true, "merchant settlement Pix key missing")
		return
	}

	switch settlement.Status {
	case database.MerchantSettlementStatusProcessing:
		if settlement.ProviderIDEnvio != "" || settlement.ProviderE2EID != "" {
			w.reconcileOne(ctx, settlement, start)
			return
		}
		w.submitOne(ctx, settlement, start)
	default:
		w.submitOne(ctx, settlement, start)
	}
}

func (w *NFCMerchantSettlementWorker) submitOne(ctx context.Context, settlement *database.MerchantSettlement, start time.Time) {
	result, retryAfter, err := w.callEfiPixSend(ctx, settlement)
	if err != nil {
		if isAmbiguousSubmissionError(err) {
			_ = w.db.MarkMerchantSettlementSubmissionUnknown(ctx, settlement.ID, err.Error())
			w.bus.Publish(Event{Type: "nfc.settlement.submission_unknown", OrderID: settlement.AuthorizationID, Payload: map[string]any{
				"settlement_id": settlement.ID,
				"error":         err.Error(),
			}})
			return
		}
		if isPermanentNFCSettlementError(err) {
			_ = w.db.MarkMerchantSettlementManualReview(ctx, settlement.ID, err.Error())
			w.publishSettlementFailure(settlement, true, err.Error())
			return
		}
		_ = w.db.MarkMerchantSettlementRetryable(ctx, settlement.ID, err.Error(), retryAfter)
		w.publishSettlementFailure(settlement, false, err.Error())
		return
	}
	if err := w.db.MarkMerchantSettlementSubmitted(ctx, settlement.ID, firstNonEmpty(result.IDEnvio, settlement.IdempotencyKey), result.E2EID, result.Status); err != nil {
		slog.Error("NFC settlement: submission persist failed", "settlement_id", settlement.ID, "err", err)
		return
	}
	w.bus.Publish(Event{Type: "nfc.settlement.submitted", OrderID: settlement.AuthorizationID, Payload: map[string]any{
		"settlement_id":      settlement.ID,
		"provider":           settlement.Provider,
		"id_envio":           firstNonEmpty(result.IDEnvio, settlement.IdempotencyKey),
		"provider_reference": firstNonEmpty(result.E2EID, result.IDEnvio),
		"provider_status":    result.Status,
		"amount_brl_minor":   settlement.AmountBRLMinor,
	}})
	slog.Info("NFC settlement submetido, aguardando webhook/consulta", "settlement_id", settlement.ID, "duration_ms", time.Since(start).Milliseconds())
}

func (w *NFCMerchantSettlementWorker) reconcileOne(ctx context.Context, settlement *database.MerchantSettlement, start time.Time) {
	result, retryAfter, err := w.getEfiPixSent(ctx, settlement)
	if err != nil {
		if isPermanentNotFoundAfterUnknown(settlement, err) {
			_ = w.db.MarkMerchantSettlementRetryable(ctx, settlement.ID, err.Error(), 5*time.Second)
			return
		}
		if isPermanentNFCSettlementError(err) {
			_ = w.db.MarkMerchantSettlementManualReview(ctx, settlement.ID, err.Error())
			w.publishSettlementFailure(settlement, true, err.Error())
			return
		}
		_ = w.db.MarkMerchantSettlementRetryable(ctx, settlement.ID, err.Error(), retryAfter)
		w.publishSettlementFailure(settlement, false, err.Error())
		return
	}
	eventPayload := map[string]any{
		"source": "poll",
		"status": result.Status,
		"e2e_id": result.E2EID,
	}
	if result.AmountBRLMinor > 0 {
		eventPayload["amount_brl_minor"] = result.AmountBRLMinor
	}
	duplicate, updated, err := w.db.ApplyMerchantSettlementProviderEvent(ctx, settlement.Provider, firstNonEmpty(result.IDEnvio, settlement.IdempotencyKey), result.E2EID, result.Status, eventPayload)
	if err != nil {
		slog.Error("NFC settlement: reconciliação falhou", "settlement_id", settlement.ID, "err", err)
		return
	}
	if updated != nil && updated.Status == database.MerchantSettlementStatusConfirmed {
		w.bus.Publish(Event{Type: "nfc.settlement.confirmed", OrderID: settlement.AuthorizationID, Payload: map[string]any{
			"settlement_id":      settlement.ID,
			"provider":           settlement.Provider,
			"provider_reference": updated.ProviderReference,
			"provider_status":    updated.ProviderStatus,
			"duplicate":          duplicate,
		}})
	}
	slog.Info("NFC settlement reconciliado", "settlement_id", settlement.ID, "status", result.Status, "duration_ms", time.Since(start).Milliseconds())
}

func (w *NFCMerchantSettlementWorker) callEfiPixSend(ctx context.Context, settlement *database.MerchantSettlement) (efiPixSendResult, time.Duration, error) {
	if w.cfg.EfiClientID == "" || w.cfg.EfiClientSecret == "" || w.cfg.EfiPixKey == "" {
		return efiPixSendResult{}, 0, fmt.Errorf("Efí Pix Send nao configurado")
	}
	token, err := w.getEfiToken(ctx)
	if err != nil {
		return efiPixSendResult{}, 0, fmt.Errorf("efi auth: %w", err)
	}
	return w.doEfiPixSend(ctx, settlement, token, true)
}

func (w *NFCMerchantSettlementWorker) doEfiPixSend(ctx context.Context, settlement *database.MerchantSettlement, token string, retryAuth bool) (efiPixSendResult, time.Duration, error) {
	payload := buildEfiPixSendPayload(w.cfg.EfiPixKey, settlement)
	body, _ := json.Marshal(payload)
	idEnvio := settlement.IdempotencyKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimRight(w.cfg.EfiApiBaseURL, "/")+"/v3/gn/pix/"+idEnvio, bytes.NewReader(body))
	if err != nil {
		return efiPixSendResult{}, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := w.client.Do(req)
	if err != nil {
		return efiPixSendResult{}, 0, fmt.Errorf("efi pix send request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == http.StatusUnauthorized && retryAuth {
		fresh, tokenErr := w.getEfiToken(ctx)
		if tokenErr != nil {
			return efiPixSendResult{}, 0, tokenErr
		}
		return w.doEfiPixSend(ctx, settlement, fresh, false)
	}
	if resp.StatusCode >= 400 {
		return efiPixSendResult{}, retryAfter(resp), fmt.Errorf("efi pix send status %d: %s", resp.StatusCode, string(respBody))
	}
	result, err := parseEfiPixSendResult(respBody)
	if err != nil {
		return efiPixSendResult{}, 0, err
	}
	result.IDEnvio = firstNonEmpty(result.IDEnvio, idEnvio)
	return result, 0, nil
}

func (w *NFCMerchantSettlementWorker) getEfiPixSent(ctx context.Context, settlement *database.MerchantSettlement) (efiPixSendResult, time.Duration, error) {
	token, err := w.getEfiToken(ctx)
	if err != nil {
		return efiPixSendResult{}, 0, fmt.Errorf("efi auth: %w", err)
	}
	idEnvio := firstNonEmpty(settlement.ProviderIDEnvio, settlement.IdempotencyKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(w.cfg.EfiApiBaseURL, "/")+"/v2/gn/pix/enviados/id-envio/"+idEnvio, nil)
	if err != nil {
		return efiPixSendResult{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := w.client.Do(req)
	if err != nil {
		return efiPixSendResult{}, 0, fmt.Errorf("efi pix sent lookup request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 400 {
		return efiPixSendResult{}, retryAfter(resp), fmt.Errorf("efi pix sent lookup status %d: %s", resp.StatusCode, string(respBody))
	}
	result, err := parseEfiPixSendResult(respBody)
	if err != nil {
		return efiPixSendResult{}, 0, err
	}
	result.IDEnvio = firstNonEmpty(result.IDEnvio, idEnvio)
	return result, 0, nil
}

func buildEfiPixSendPayload(payerPixKey string, settlement *database.MerchantSettlement) map[string]any {
	payload := map[string]any{
		"valor": fmt.Sprintf("%.2f", float64(settlement.AmountBRLMinor)/100),
		"pagador": map[string]any{
			"chave":       payerPixKey,
			"infoPagador": fmt.Sprintf("ChainFX Tap %s", settlement.AuthorizationID),
		},
		"favorecido": map[string]any{
			"chave": settlement.TargetPixKey,
		},
	}
	if doc := onlyDigitsWorker(settlement.TargetDocument); doc != "" {
		payload["favorecido"].(map[string]any)["cpf"] = doc
	}
	return payload
}

func parseEfiPixSendResult(raw []byte) (efiPixSendResult, error) {
	var result struct {
		IDEnvio    string `json:"idEnvio"`
		E2EID      string `json:"e2eId"`
		EndToEndID string `json:"endToEndId"`
		Status     string `json:"status"`
		Valor      string `json:"valor"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return efiPixSendResult{}, fmt.Errorf("efi pix send response parse: %w", err)
	}
	ref := firstNonEmpty(result.E2EID, result.EndToEndID, result.IDEnvio)
	if ref == "" {
		return efiPixSendResult{}, fmt.Errorf("efi pix send: provider reference vazio")
	}
	return efiPixSendResult{
		IDEnvio:        result.IDEnvio,
		E2EID:          firstNonEmpty(result.E2EID, result.EndToEndID),
		Status:         firstNonEmpty(result.Status, "SUBMITTED"),
		AmountBRLMinor: parseBRLMinorWorker(result.Valor),
	}, nil
}

func (w *NFCMerchantSettlementWorker) getEfiToken(ctx context.Context) (string, error) {
	raw := []byte(`{"grant_type":"client_credentials"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(w.cfg.EfiApiBaseURL, "/")+"/oauth/token", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(w.cfg.EfiClientID, w.cfg.EfiClientSecret)
	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("efi token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("efi token status %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("efi: access_token vazio")
	}
	return result.AccessToken, nil
}

func (w *NFCMerchantSettlementWorker) publishSettlementFailure(settlement *database.MerchantSettlement, permanent bool, errMsg string) {
	w.bus.Publish(Event{Type: "nfc.settlement.failed", OrderID: settlement.AuthorizationID, Payload: map[string]any{
		"settlement_id": settlement.ID,
		"permanent":     permanent,
		"error":         errMsg,
		"attempts":      settlement.RetryCount,
	}})
	if !permanent {
		w.dlq.Push(Event{Type: "nfc.settlement.failed", OrderID: settlement.AuthorizationID}, settlement.RetryCount, errMsg)
	}
}

func (w *NFCMerchantSettlementWorker) automatic() bool {
	mode := w.mode()
	return mode == "efi" || mode == "automatic" || mode == "auto"
}

func (w *NFCMerchantSettlementWorker) mode() string {
	if w == nil || w.cfg == nil {
		return "manual"
	}
	return strings.ToLower(strings.TrimSpace(w.cfg.NFCSettlementMode))
}

func nfcSettlementHTTPClient(cfg *config.Config) *http.Client {
	if cfg == nil {
		return httpclient.Default()
	}
	cert, err := certutil.LoadCertificate(cfg.EfiCertificatePath, cfg.EfiCertificateKey, cfg.EfiCertificateP12, cfg.EfiCertificatePass)
	if err != nil {
		return httpclient.Default()
	}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}},
	}
}

func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

func isAmbiguousSubmissionError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded") || (errors.As(err, &netErr) && netErr.Timeout())
}

func isPermanentNotFoundAfterUnknown(settlement *database.MerchantSettlement, err error) bool {
	if settlement == nil || err == nil {
		return false
	}
	return settlement.Status == database.MerchantSettlementStatusSubmissionUnknown &&
		strings.Contains(strings.ToLower(err.Error()), "status 404")
}

func isPermanentNFCSettlementError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"chave_invalida", "cpf_invalido", "conta_bloqueada", "kyc", "nao configurado", "pix key missing", "status 400", "status 401", "status 403", "status 422"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func onlyDigitsWorker(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseBRLMinorWorker(value string) int64 {
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
