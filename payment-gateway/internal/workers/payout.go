package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/models"
	"payment-gateway/internal/resilience"
)

const maxPayoutAttempts = 4

// PayoutWorker processes PIX sell-order payouts with retry and DLQ.
type PayoutWorker struct {
	bus    *EventBus
	db     *database.DB
	store  sellPayoutStore
	cfg    *config.Config
	client *http.Client
	dlq    *DeadLetterQueue
	sem    chan struct{}
}

type sellPayoutStore interface {
	GetOrder(context.Context, string) (*models.Order, error)
	ClaimOrderForPayout(context.Context, string) (bool, error)
	ClaimOrderForManualPayout(context.Context, string, map[string]any) (bool, error)
	UpdateOrderStatus(context.Context, string, string, map[string]any) error
	OpenOrderIncident(context.Context, string, string, string, string, any) error
	EnsureSellPayoutExecution(context.Context, string, string, string, int64, string) (*database.SellPayoutExecution, error)
	GetSellPayoutExecutionByOrder(context.Context, string) (*database.SellPayoutExecution, error)
	ListDueSellPayoutExecutions(context.Context, int) ([]database.SellPayoutExecution, error)
	MarkSellPayoutSubmitStarted(context.Context, string) error
	MarkSellPayoutSubmitted(context.Context, string, string, string, string) error
	MarkSellPayoutProviderUnknown(context.Context, string, string) error
	MarkSellPayoutFailed(context.Context, string, string, string, bool) error
	MarkSellPayoutReconcileNotFound(context.Context, string, string, time.Duration, int, time.Duration) error
	ApplySellPayoutProviderEvent(context.Context, string, string, string, string, map[string]any) (bool, *database.SellPayoutExecution, error)
}

func NewPayoutWorker(bus *EventBus, db *database.DB, cfg *config.Config) *PayoutWorker {
	return &PayoutWorker{
		bus:    bus,
		db:     db,
		store:  db,
		cfg:    cfg,
		client: httpclient.Default(),
		dlq:    NewPersistentDLQ(db, 1000),
		sem:    make(chan struct{}, 8),
	}
}

func (pw *PayoutWorker) Start(ctx context.Context) {
	payoutChan := pw.bus.Subscribe("payout.requested")
	slog.Info("PayoutWorker escutando eventos 'payout.requested'")
	pw.dlq.StartPeriodicLog(ctx, 5*time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando PayoutWorker")
			return
		case <-ticker.C:
			pw.recoverDueSellPayouts(ctx)
		case event, ok := <-payoutChan:
			if !ok {
				return
			}
			select {
			case pw.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func(e Event) {
				defer func() {
					<-pw.sem
					if r := recover(); r != nil {
						slog.Error("PayoutWorker: panic em processPayout", "recover", r)
					}
				}()
				pw.processPayoutHardened(ctx, e)
			}(event)
		}
	}
}

func (pw *PayoutWorker) recoverDueSellPayouts(ctx context.Context) {
	if strings.EqualFold(strings.TrimSpace(pw.cfg.SellPayoutMode), "manual") || strings.TrimSpace(pw.cfg.SellPayoutMode) == "" {
		return
	}
	execs, err := pw.payoutStore().ListDueSellPayoutExecutions(ctx, 50)
	if err != nil {
		slog.Error("PayoutWorker: erro ao listar payouts para recovery", "err", err)
		return
	}
	for _, exec := range execs {
		pw.processPayoutHardened(ctx, Event{Type: "payout.recovery", OrderID: exec.OrderID})
	}
}

func (pw *PayoutWorker) processPayoutHardened(ctx context.Context, event Event) {
	start := time.Now()
	orderID := event.OrderID
	store := pw.payoutStore()
	slog.Info("PayoutWorker: iniciando", "order_id", orderID)

	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	order, err := store.GetOrder(fetchCtx, orderID)
	cancel()
	if err != nil {
		slog.Error("PayoutWorker: erro ao buscar ordem", "order_id", orderID, "err", err)
		pw.dlq.Push(event, 1, "db fetch error: "+err.Error())
		return
	}
	if order == nil {
		return
	}
	if order.Status != models.StatusPago && order.Status != models.StatusProcessandoPayout {
		return
	}

	if strings.EqualFold(strings.TrimSpace(pw.cfg.SellPayoutMode), "manual") || strings.TrimSpace(pw.cfg.SellPayoutMode) == "" {
		if order.Status != models.StatusPago {
			return
		}
		claimed, err := store.ClaimOrderForManualPayout(ctx, orderID, map[string]any{
			"mode":          "manual",
			"depositTx":     stringValue(order.DepositTx),
			"depositAmount": floatValue(order.DepositAmount),
			"pixKeyPresent": order.PixKey != "",
			"payoutBRL":     order.PayoutBRL,
		})
		if err != nil {
			pw.dlq.Push(event, 1, "manual payout queue error: "+err.Error())
			return
		}
		if !claimed {
			return
		}
		pw.bus.Publish(Event{Type: "payout.manual_required", OrderID: orderID, Payload: map[string]any{"status": string(models.StatusAguardandoPixManual), "payout_brl": order.PayoutBRL}})
		return
	}

	if order.PixKey == "" {
		reason := "PixKey vazia; payout automatico bloqueado"
		_ = store.UpdateOrderStatus(ctx, orderID, string(models.StatusIncidenteValidacao), map[string]any{"error": reason})
		_ = store.OpenOrderIncident(ctx, orderID, "sell_payout_validation", "critical", reason, map[string]any{"rule": "no_auto_refund_manual_review_required"})
		pw.dlq.Push(event, 1, "empty pix key")
		return
	}
	if order.PayoutBRL <= 0 {
		_ = store.UpdateOrderStatus(ctx, orderID, "erro", map[string]any{"error": "PayoutBRL invalido"})
		pw.dlq.Push(event, 1, "invalid payout amount")
		return
	}
	if pw.cfg.AllowSimulations && !pw.cfg.IsProduction() {
		txHash := fmt.Sprintf("pix-sim-%s", orderID)
		if err := store.UpdateOrderStatus(ctx, orderID, "concluida", map[string]any{"txHash": txHash}); err != nil {
			return
		}
		pw.bus.Publish(Event{Type: "payout.settled", OrderID: orderID, Payload: map[string]any{"status": "concluida", "tx_hash_pix": txHash}})
		return
	}
	if pw.cfg.EfiClientID == "" || pw.cfg.EfiClientSecret == "" || pw.cfg.EfiPixKey == "" {
		_ = store.UpdateOrderStatus(ctx, orderID, "erro", map[string]any{"error": "Efi Pix Send nao configurado"})
		pw.dlq.Push(event, 1, "efi not configured")
		return
	}

	var exec *database.SellPayoutExecution
	if order.Status == models.StatusPago {
		claimed, err := store.ClaimOrderForPayout(ctx, orderID)
		if err != nil {
			pw.dlq.Push(event, 1, "claim error: "+err.Error())
			return
		}
		if !claimed {
			return
		}
		exec, err = store.EnsureSellPayoutExecution(ctx, orderID, "efi", stableSellPayoutIDEnvio(orderID), brlMinor(order.PayoutBRL), order.PixKey)
		if err != nil {
			pw.dlq.Push(event, 1, "payout execution error: "+err.Error())
			return
		}
	} else {
		exec, err = store.GetSellPayoutExecutionByOrder(ctx, orderID)
		if err != nil {
			pw.dlq.Push(event, 1, "payout execution fetch error: "+err.Error())
			return
		}
		if exec == nil {
			reason := "sell payout em processando_payout sem execution canonica; revisao manual requerida"
			_ = store.UpdateOrderStatus(ctx, orderID, string(models.StatusIncidenteValidacao), map[string]any{"error": reason})
			_ = store.OpenOrderIncident(ctx, orderID, "sell_payout_missing_execution", "critical", reason, map[string]any{"rule": "no_auto_resubmit_without_execution"})
			return
		}
	}

	switch exec.Status {
	case "completed", "failed", "manual_review":
		return
	case "provider_unknown", "provider_pending", "submitted":
		pw.reconcileSellPayout(ctx, exec, start)
		return
	case "submit_started":
		if exec.AttemptCount > 0 {
			pw.reconcileSellPayout(ctx, exec, start)
			return
		}
	}
	pw.submitSellPayout(ctx, order, exec, start)
}

func (pw *PayoutWorker) processPayout(ctx context.Context, event Event) {
	start := time.Now()
	orderID := event.OrderID
	slog.Info("PayoutWorker: iniciando", "order_id", orderID)

	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	order, err := pw.db.GetOrder(fetchCtx, orderID)
	cancel()
	if err != nil {
		slog.Error("PayoutWorker: erro ao buscar ordem", "order_id", orderID, "err", err)
		pw.dlq.Push(event, 1, "db fetch error: "+err.Error())
		return
	}
	if order == nil || order.Status != models.StatusPago {
		slog.Debug("PayoutWorker: ordem ignorada (status incompatível)",
			"order_id", orderID, "status", func() string {
				if order != nil {
					return string(order.Status)
				}
				return "nil"
			}())
		return
	}

	// ── Atomic claim: prevents double-payout across goroutines and replicas ──
	// UPDATE ... WHERE status='pago' RETURNING id guarantees only one worker
	// proceeds even if the event is delivered multiple times (re-delivery,
	// crash-loop or multi-replica fan-out). If another worker already claimed
	// the order, rows affected = 0 → we bail out silently.
	if strings.EqualFold(strings.TrimSpace(pw.cfg.SellPayoutMode), "manual") || strings.TrimSpace(pw.cfg.SellPayoutMode) == "" {
		claimCtx, claimCancel := context.WithTimeout(ctx, 5*time.Second)
		claimed, err := pw.db.ClaimOrderForManualPayout(claimCtx, orderID, map[string]any{
			"mode":          "manual",
			"depositTx":     stringValue(order.DepositTx),
			"depositAmount": floatValue(order.DepositAmount),
			"pixKeyPresent": order.PixKey != "",
			"payoutBRL":     order.PayoutBRL,
		})
		claimCancel()
		if err != nil {
			slog.Error("PayoutWorker: erro ao enfileirar payout manual", "order_id", orderID, "err", err)
			pw.dlq.Push(event, 1, "manual payout queue error: "+err.Error())
			return
		}
		if !claimed {
			slog.Debug("PayoutWorker: payout manual ja enfileirado ou processado", "order_id", orderID)
			return
		}
		pw.bus.Publish(Event{
			Type:    "payout.manual_required",
			OrderID: orderID,
			Payload: map[string]any{"status": string(models.StatusAguardandoPixManual), "payout_brl": order.PayoutBRL},
		})
		slog.Warn("PayoutWorker: payout PIX manual requerido",
			"order_id", orderID, "payout_brl", order.PayoutBRL, "duration_ms", time.Since(start).Milliseconds())
		return
	}

	claimCtx, claimCancel := context.WithTimeout(ctx, 5*time.Second)
	claimed, err := pw.db.ClaimOrderForPayout(claimCtx, orderID)
	claimCancel()
	if err != nil {
		slog.Error("PayoutWorker: erro ao tentar claim de payout", "order_id", orderID, "err", err)
		pw.dlq.Push(event, 1, "claim error: "+err.Error())
		return
	}
	if !claimed {
		slog.Debug("PayoutWorker: ordem já processada por outro worker", "order_id", orderID)
		return
	}

	// Validate payout destination before any external call
	if order.PixKey == "" {
		slog.Error("PayoutWorker: PixKey vazia, impossível fazer payout", "order_id", orderID)
		_ = pw.db.UpdateOrderStatus(ctx, orderID, string(models.StatusIncidenteValidacao),
			map[string]any{"error": "PixKey vazia — contato suporte"})
		_ = pw.db.OpenOrderIncident(ctx, orderID, "sell_payout_validation", "critical", "Payout PIX bloqueado para revisao manual: PixKey vazia", map[string]any{
			"rule": "no_auto_refund_manual_review_required",
		})
		pw.dlq.Push(event, 1, "empty pix key")
		return
	}
	if order.PayoutBRL <= 0 {
		slog.Error("PayoutWorker: PayoutBRL inválido", "order_id", orderID, "payout_brl", order.PayoutBRL)
		_ = pw.db.UpdateOrderStatus(ctx, orderID, "erro",
			map[string]any{"error": "PayoutBRL inválido"})
		pw.dlq.Push(event, 1, "invalid payout amount")
		return
	}

	if pw.cfg.AllowSimulations && !pw.cfg.IsProduction() {
		txHash := fmt.Sprintf("pix-sim-%s", orderID)
		if err := pw.db.UpdateOrderStatus(ctx, orderID, "concluida",
			map[string]any{"txHash": txHash}); err != nil {
			slog.Error("PayoutWorker: erro ao persistir payout simulado", "order_id", orderID, "err", err)
			return
		}
		pw.bus.Publish(Event{
			Type:    "payout.settled",
			OrderID: orderID,
			Payload: map[string]any{"status": "concluida", "tx_hash_pix": txHash},
		})
		slog.Warn("PayoutWorker: payout simulado concluído",
			"order_id", orderID, "duration_ms", time.Since(start).Milliseconds())
		return
	}

	// ── Production: Efí PIX payout with retry + exponential backoff ───────────
	if pw.cfg.EfiClientID == "" || pw.cfg.EfiClientSecret == "" {
		slog.Error("PayoutWorker: EFI_CLIENT_ID / EFI_CLIENT_SECRET não configurados", "order_id", orderID)
		_ = pw.db.UpdateOrderStatus(ctx, orderID, "erro",
			map[string]any{"error": "Efí não configurado"})
		pw.dlq.Push(event, 1, "efi not configured")
		return
	}

	retryCfg := resilience.RetryConfig{
		MaxAttempts: maxPayoutAttempts,
		BaseDelay:   3 * time.Second,
		MaxDelay:    30 * time.Second,
		Multiplier:  2.0,
		Jitter:      true,
	}
	var attempt int
	err = resilience.DoWithContext(ctx, retryCfg, "pix.payout."+orderID,
		func(e error) bool {
			if e == nil {
				return false
			}
			// Do not retry business-logic errors (bad key, blocked account, etc.)
			msg := strings.ToLower(e.Error())
			for _, perm := range []string{"chave_invalida", "cpf_invalido", "conta_bloqueada", "kyc", "status 4"} {
				if strings.Contains(msg, perm) {
					return false
				}
			}
			return true
		},
		func(ctx context.Context) error {
			attempt++
			return pw.callEfiPix(ctx, orderID, order.PixKey, order.PayoutBRL)
		},
	)

	if err != nil {
		slog.Error("PayoutWorker: payout falhou após retries",
			"order_id", orderID, "attempts", attempt, "err", err)
		if isPayoutValidationIncident(err.Error()) {
			reason := "Payout PIX bloqueado para revisao manual: " + err.Error()
			_ = pw.db.UpdateOrderStatus(ctx, orderID, string(models.StatusIncidenteValidacao),
				map[string]any{"error": reason, "attempts": attempt})
			_ = pw.db.OpenOrderIncident(ctx, orderID, "sell_payout_validation", "critical", reason, map[string]any{
				"attempts": attempt,
				"rule":     "no_auto_refund_manual_review_required",
			})
			pw.dlq.Push(event, attempt, reason)
			return
		}
		_ = pw.db.UpdateOrderStatus(ctx, orderID, "erro",
			map[string]any{"error": err.Error(), "attempts": attempt})
		pw.dlq.Push(event, attempt, err.Error())
		return
	}
	slog.Info("PayoutWorker: payout concluído",
		"order_id", orderID, "attempts", attempt,
		"duration_ms", time.Since(start).Milliseconds())
}

// callEfiPix calls the Efí Bank PIX API to send a payment.
func (pw *PayoutWorker) callEfiPix(ctx context.Context, orderID, pixKey string, amountBRL float64) error {
	token, err := pw.getEfiToken(ctx)
	if err != nil {
		return fmt.Errorf("efí auth: %w", err)
	}

	payload := map[string]any{
		"valor":       fmt.Sprintf("%.2f", amountBRL),
		"chave":       pixKey,
		"infoPagador": fmt.Sprintf("ChainFX ordem %s", orderID),
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		pw.cfg.EfiApiBaseURL+"/v2/gn/pix/"+orderID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := pw.client.Do(req)
	if err != nil {
		return fmt.Errorf("efí pix request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("efí pix status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		EndToEndID string `json:"endToEndId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("efí response parse: %w", err)
	}

	if err := pw.db.UpdateOrderStatus(ctx, orderID, "concluida", map[string]any{
		"txHash":    result.EndToEndID,
		"pixStatus": result.Status,
	}); err != nil {
		return err
	}
	pw.bus.Publish(Event{
		Type:    "payout.settled",
		OrderID: orderID,
		Payload: map[string]any{"status": "concluida", "tx_hash_pix": result.EndToEndID, "pix_status": result.Status},
	})
	return nil
}

type efiSellPayoutResult struct {
	IDEnvio        string
	E2EID          string
	Status         string
	AmountBRLMinor int64
}

func (pw *PayoutWorker) submitSellPayout(ctx context.Context, order *models.Order, exec *database.SellPayoutExecution, start time.Time) {
	if err := pw.payoutStore().MarkSellPayoutSubmitStarted(ctx, exec.ID); err != nil {
		slog.Error("PayoutWorker: falha ao persistir submit_started", "order_id", exec.OrderID, "execution_id", exec.ID, "err", err)
		return
	}
	result, retryAfter, err := pw.callEfiPixSend(ctx, order, exec)
	if err != nil {
		if isAmbiguousSubmissionError(err) {
			_ = pw.payoutStore().MarkSellPayoutProviderUnknown(ctx, exec.ID, err.Error())
			pw.bus.Publish(Event{Type: "payout.provider_unknown", OrderID: exec.OrderID, Payload: map[string]any{"execution_id": exec.ID, "id_envio": exec.ProviderIDEnvio}})
			return
		}
		if isPermanentSellPayoutError(err) {
			reason := "Payout PIX bloqueado para revisao manual: " + err.Error()
			_ = pw.payoutStore().MarkSellPayoutFailed(ctx, exec.ID, exec.OrderID, reason, true)
			_ = pw.payoutStore().OpenOrderIncident(ctx, exec.OrderID, "sell_payout_validation", "critical", reason, map[string]any{"rule": "no_auto_refund_manual_review_required"})
			return
		}
		_ = pw.payoutStore().MarkSellPayoutProviderUnknown(ctx, exec.ID, err.Error())
		if retryAfter > 0 {
			slog.Warn("PayoutWorker: submit inconclusivo com retry-after; reconcilia antes de novo submit", "order_id", exec.OrderID, "retry_after", retryAfter)
		}
		return
	}
	if err := pw.payoutStore().MarkSellPayoutSubmitted(ctx, exec.ID, firstNonEmpty(result.E2EID, result.IDEnvio), result.E2EID, result.Status); err != nil {
		slog.Error("PayoutWorker: falha ao persistir submitted", "order_id", exec.OrderID, "execution_id", exec.ID, "err", err)
		return
	}
	_, updated, err := pw.payoutStore().ApplySellPayoutProviderEvent(ctx, firstNonEmpty(result.IDEnvio, exec.ProviderIDEnvio), firstNonEmpty(result.E2EID, result.IDEnvio), result.E2EID, result.Status, map[string]any{
		"source":           "submit",
		"status":           result.Status,
		"e2e_id":           result.E2EID,
		"amount_brl_minor": result.AmountBRLMinor,
	})
	if err != nil {
		slog.Error("PayoutWorker: falha ao aplicar status provider", "order_id", exec.OrderID, "execution_id", exec.ID, "err", err)
		return
	}
	if updated != nil && updated.Status == "completed" {
		pw.bus.Publish(Event{Type: "payout.settled", OrderID: exec.OrderID, Payload: map[string]any{"status": "concluida", "tx_hash_pix": updated.ProviderE2EID, "provider_status": result.Status}})
	}
	slog.Info("PayoutWorker: submit processado", "order_id", exec.OrderID, "status", result.Status, "duration_ms", time.Since(start).Milliseconds())
}

func (pw *PayoutWorker) reconcileSellPayout(ctx context.Context, exec *database.SellPayoutExecution, start time.Time) {
	result, retryAfter, err := pw.getEfiPixSent(ctx, exec)
	if err != nil {
		if isEfiPixSentNotFound(err) {
			_ = pw.payoutStore().MarkSellPayoutReconcileNotFound(ctx, exec.ID, "not_found_unconfirmed: "+err.Error(), firstNonZeroDuration(retryAfter, 30*time.Second), pw.notFoundMinReconciliations(), pw.ambiguousGrace())
			return
		}
		if isPermanentSellPayoutError(err) {
			_ = pw.payoutStore().MarkSellPayoutFailed(ctx, exec.ID, exec.OrderID, err.Error(), true)
			return
		}
		_ = pw.payoutStore().MarkSellPayoutProviderUnknown(ctx, exec.ID, err.Error())
		return
	}
	_, updated, err := pw.payoutStore().ApplySellPayoutProviderEvent(ctx, firstNonEmpty(result.IDEnvio, exec.ProviderIDEnvio), firstNonEmpty(result.E2EID, result.IDEnvio), result.E2EID, result.Status, map[string]any{
		"source":           "poll",
		"status":           result.Status,
		"e2e_id":           result.E2EID,
		"amount_brl_minor": result.AmountBRLMinor,
	})
	if err != nil {
		slog.Error("PayoutWorker: reconciliação falhou", "order_id", exec.OrderID, "execution_id", exec.ID, "err", err)
		return
	}
	if updated != nil && updated.Status == "completed" {
		pw.bus.Publish(Event{Type: "payout.settled", OrderID: exec.OrderID, Payload: map[string]any{"status": "concluida", "tx_hash_pix": updated.ProviderE2EID, "provider_status": result.Status}})
	}
	slog.Info("PayoutWorker: payout reconciliado", "order_id", exec.OrderID, "status", result.Status, "duration_ms", time.Since(start).Milliseconds())
}

func (pw *PayoutWorker) callEfiPixSend(ctx context.Context, order *models.Order, exec *database.SellPayoutExecution) (efiSellPayoutResult, time.Duration, error) {
	token, err := pw.getEfiToken(ctx)
	if err != nil {
		return efiSellPayoutResult{}, 0, fmt.Errorf("efi auth: %w", err)
	}
	payload := map[string]any{
		"valor": fmt.Sprintf("%.2f", float64(exec.AmountBRLMinor)/100),
		"pagador": map[string]any{
			"chave":       pw.cfg.EfiPixKey,
			"infoPagador": fmt.Sprintf("ChainFX ordem %s", order.ID),
		},
		"favorecido": map[string]any{
			"chave": exec.RecipientReference,
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimRight(pw.cfg.EfiApiBaseURL, "/")+"/v3/gn/pix/"+exec.ProviderIDEnvio, bytes.NewReader(body))
	if err != nil {
		return efiSellPayoutResult{}, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := pw.client.Do(req)
	if err != nil {
		return efiSellPayoutResult{}, 0, fmt.Errorf("efi pix send request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 400 {
		return efiSellPayoutResult{}, retryAfter(resp), fmt.Errorf("efi pix send status %d: %s", resp.StatusCode, string(respBody))
	}
	result, err := parseEfiSellPayoutResult(respBody)
	if err != nil {
		return efiSellPayoutResult{}, 0, err
	}
	result.IDEnvio = firstNonEmpty(result.IDEnvio, exec.ProviderIDEnvio)
	return result, 0, nil
}

func (pw *PayoutWorker) getEfiPixSent(ctx context.Context, exec *database.SellPayoutExecution) (efiSellPayoutResult, time.Duration, error) {
	token, err := pw.getEfiToken(ctx)
	if err != nil {
		return efiSellPayoutResult{}, 0, fmt.Errorf("efi auth: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(pw.cfg.EfiApiBaseURL, "/")+"/v2/gn/pix/enviados/id-envio/"+exec.ProviderIDEnvio, nil)
	if err != nil {
		return efiSellPayoutResult{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := pw.client.Do(req)
	if err != nil {
		return efiSellPayoutResult{}, 0, fmt.Errorf("efi pix sent lookup request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 400 {
		return efiSellPayoutResult{}, retryAfter(resp), fmt.Errorf("efi pix sent lookup status %d: %s", resp.StatusCode, string(respBody))
	}
	result, err := parseEfiSellPayoutResult(respBody)
	if err != nil {
		return efiSellPayoutResult{}, 0, err
	}
	result.IDEnvio = firstNonEmpty(result.IDEnvio, exec.ProviderIDEnvio)
	return result, 0, nil
}

func parseEfiSellPayoutResult(raw []byte) (efiSellPayoutResult, error) {
	var result struct {
		IDEnvio    string `json:"idEnvio"`
		E2EID      string `json:"e2eId"`
		EndToEndID string `json:"endToEndId"`
		Status     string `json:"status"`
		Valor      string `json:"valor"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return efiSellPayoutResult{}, fmt.Errorf("efi pix send response parse: %w", err)
	}
	ref := firstNonEmpty(result.E2EID, result.EndToEndID, result.IDEnvio)
	if ref == "" {
		return efiSellPayoutResult{}, fmt.Errorf("efi pix send: provider reference vazio")
	}
	return efiSellPayoutResult{
		IDEnvio:        result.IDEnvio,
		E2EID:          firstNonEmpty(result.E2EID, result.EndToEndID),
		Status:         firstNonEmpty(result.Status, "EM_PROCESSAMENTO"),
		AmountBRLMinor: parseBRLMinorPayout(result.Valor),
	}, nil
}

// getEfiToken fetches a short-lived Efí OAuth2 token.
func isPayoutValidationIncident(msg string) bool {
	msg = strings.ToLower(msg)
	for _, marker := range []string{"chave_invalida", "cpf_invalido", "conta_bloqueada", "kyc", "cpf", "titular", "beneficiario", "beneficiário"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func (pw *PayoutWorker) getEfiToken(ctx context.Context) (string, error) {
	body := strings.NewReader("grant_type=client_credentials")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(pw.cfg.EfiApiBaseURL, "/")+"/oauth/token", body)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(pw.cfg.EfiClientID, pw.cfg.EfiClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := pw.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("efí token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("efí token status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("efí: access_token vazio")
	}
	return result.AccessToken, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (pw *PayoutWorker) payoutStore() sellPayoutStore {
	if pw.store != nil {
		return pw.store
	}
	return pw.db
}

func stableSellPayoutIDEnvio(orderID string) string {
	return strings.TrimSpace(orderID)
}

func brlMinor(value float64) int64 {
	return int64(math.Round(value * 100))
}

func (pw *PayoutWorker) ambiguousGrace() time.Duration {
	if pw == nil || pw.cfg == nil || pw.cfg.SellPayoutAmbiguousGraceSec <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(pw.cfg.SellPayoutAmbiguousGraceSec) * time.Second
}

func (pw *PayoutWorker) notFoundMinReconciliations() int {
	if pw == nil || pw.cfg == nil || pw.cfg.SellPayoutNotFoundMinReconciliations <= 0 {
		return 3
	}
	return pw.cfg.SellPayoutNotFoundMinReconciliations
}

func isPermanentSellPayoutError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"chave_invalida", "cpf_invalido", "conta_bloqueada", "kyc", "cpf", "titular", "beneficiario", "beneficiário", "status 400", "status 401", "status 403", "status 422", "nao configurado", "não configurado"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func parseBRLMinorPayout(value string) int64 {
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

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func floatValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}
