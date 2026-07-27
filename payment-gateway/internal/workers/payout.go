package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	cfg    *config.Config
	client *http.Client
	dlq    *DeadLetterQueue
	sem    chan struct{}
}

func NewPayoutWorker(bus *EventBus, db *database.DB, cfg *config.Config) *PayoutWorker {
	return &PayoutWorker{
		bus:    bus,
		db:     db,
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

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando PayoutWorker")
			return
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
				pw.processPayout(ctx, e)
			}(event)
		}
	}
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
		pw.cfg.EfiApiBaseURL+"/v1/authorize", body)
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
