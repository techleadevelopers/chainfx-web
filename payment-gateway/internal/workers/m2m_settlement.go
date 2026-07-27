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
	"payment-gateway/internal/resilience"
)

const (
	m2mMaxAttempts   = 4
	m2mSettlePollSec = 5
)

// M2MSettlementWorker listens for confirmed M2M crypto deposits and executes
// the corresponding fiat payout (PIX or credit-card) through Efí Bank.
// It is the authoritative settlement engine for agent-initiated payments.
//
// Concurrency safety: each intent is claimed with a PostgreSQL advisory xact
// lock (pg_try_advisory_xact_lock) before any external call is made, so
// multiple running replicas will not double-settle.
type M2MSettlementWorker struct {
	bus    *EventBus
	db     *database.DB
	cfg    *config.Config
	client *http.Client
	dlq    *DeadLetterQueue
	sem    chan struct{}
}

func NewM2MSettlementWorker(bus *EventBus, db *database.DB, cfg *config.Config) *M2MSettlementWorker {
	return &M2MSettlementWorker{
		bus:    bus,
		db:     db,
		cfg:    cfg,
		client: httpclient.Default(),
		dlq:    NewPersistentDLQ(db, 1000),
		sem:    make(chan struct{}, 8),
	}
}

// Start runs the worker. It responds to `m2m.deposit.confirmed` bus events
// for low-latency processing AND polls the DB every 5 s as a catch-all for
// intents that may have been missed (e.g., on restart).
func (w *M2MSettlementWorker) Start(ctx context.Context) {
	depositChan := w.bus.Subscribe("m2m.deposit.confirmed")
	ticker := time.NewTicker(m2mSettlePollSec * time.Second)
	defer ticker.Stop()
	w.dlq.StartPeriodicLog(ctx, 5*time.Minute)
	slog.Info("M2MSettlementWorker iniciado")

	for {
		select {
		case <-ctx.Done():
			slog.Info("M2MSettlementWorker: encerrando")
			return

		case ev, ok := <-depositChan:
			if !ok {
				return
			}
			w.dispatchSettle(ctx, ev.OrderID) // OrderID field reused as intentID

		case <-ticker.C:
			w.sweepPaidCrypto(ctx)
		}
	}
}

func (w *M2MSettlementWorker) dispatchSettle(ctx context.Context, intentID string) {
	select {
	case w.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	go func() {
		defer func() {
			<-w.sem
			if r := recover(); r != nil {
				slog.Error("M2MSettlement: panic em settleOne", "recover", r, "intent_id", intentID)
			}
		}()
		w.settleOne(ctx, intentID)
	}()
}

// sweepPaidCrypto fetches all paid_crypto intents and settles each one.
func (w *M2MSettlementWorker) sweepPaidCrypto(ctx context.Context) {
	intents, err := w.db.GetPaidCryptoIntents(ctx)
	if err != nil {
		slog.Error("M2MSettlement: erro ao buscar paid_crypto intents", "err", err)
		return
	}
	for _, intent := range intents {
		w.dispatchSettle(ctx, intent.ID)
	}
}

// settleOne claims the advisory lock on an intent and, if successful, runs
// the full settlement flow: daily-limit check → mark settling → Efí call.
func (w *M2MSettlementWorker) settleOne(ctx context.Context, intentID string) {
	start := time.Now()

	// ── 1. Advisory lock (multi-replica safe) ────────────────────────────────
	locked, lockTx, err := w.db.AcquireM2MSettlementLock(ctx, intentID)
	if err != nil {
		slog.Error("M2MSettlement: lock error", "intent_id", intentID, "err", err)
		return
	}
	if !locked {
		slog.Debug("M2MSettlement: intent locked by another replica, skipping", "intent_id", intentID)
		return
	}
	// lockTx stays open until MarkM2MSettled/MarkM2MFailed commit it.

	// ── 2. Transition paid_crypto → settling (inside the lock tx) ───────────
	if err := w.db.MarkM2MSettling(ctx, lockTx, intentID); err != nil {
		// Not in paid_crypto state — skip silently (may be settled by event+poll race).
		slog.Debug("M2MSettlement: skip (not paid_crypto)", "intent_id", intentID, "err", err)
		_ = lockTx.Rollback()
		return
	}
	// Commit the status change so it is visible to other replicas immediately.
	if err := lockTx.Commit(); err != nil {
		slog.Error("M2MSettlement: commit settling failed", "intent_id", intentID, "err", err)
		return
	}

	// ── 3. Reload intent for settlement data ─────────────────────────────────
	intent, err := w.db.GetAgentPaymentIntent(ctx, intentID)
	if err != nil || intent == nil {
		slog.Error("M2MSettlement: intent not found after claiming", "intent_id", intentID)
		return
	}

	slog.Info("M2MSettlement: iniciando liquidacao",
		"intent_id", intentID,
		"type", intent.PaymentType,
		"amount_brl", intent.AmountBRL,
		"fee_bps", intent.FeeBps)

	// ── 4. Daily outflow safety cap ───────────────────────────────────────────
	if err := w.checkDailyOutflow(ctx, intent.AmountBRL); err != nil {
		slog.Error("M2MSettlement: daily outflow cap reached", "intent_id", intentID, "err", err)
		_, lTx, _ := w.db.AcquireM2MSettlementLock(ctx, intentID)
		if lTx != nil {
			_ = w.db.MarkM2MFailed(ctx, lTx, intentID, err.Error(), false)
		}
		return
	}

	// ── 5. Efí PIX payout with exponential back-off ───────────────────────────
	if !w.cfg.AllowSimulations && w.cfg.IsProduction() {
		if err := w.runSettlement(ctx, intent); err != nil {
			slog.Error("M2MSettlement: liquidacao falhou",
				"intent_id", intentID, "attempts", intent.Attempts, "err", err)
			return
		}
	} else {
		// Simulation / dev mode
		slog.Warn("M2MSettlement: modo simulacao — sem chamada Efí real",
			"intent_id", intentID)
		fakeTxID := fmt.Sprintf("m2m-sim-%s", intentID)
		_, lTx, _ := w.db.AcquireM2MSettlementLock(ctx, intentID)
		if lTx != nil {
			_ = w.db.MarkM2MSettled(ctx, lTx, intentID, fakeTxID, "DEVTEST")
		}
		w.bus.Publish(Event{
			Type:    "m2m.settlement.done",
			OrderID: intentID,
			Payload: map[string]any{"mode": "simulation", "tx_id": fakeTxID},
		})
	}

	slog.Info("M2MSettlement: liquidacao concluida",
		"intent_id", intentID,
		"duration_ms", time.Since(start).Milliseconds())
}

// runSettlement calls the configured fiat settlement adapter with retry + exponential back-off.
func (w *M2MSettlementWorker) runSettlement(ctx context.Context, intent *database.AgentPaymentIntent) error {
	if intent.PaymentType == database.M2MTypeCreditCard {
		if err := w.queueCardPayment(ctx, intent); err != nil {
			_, lTx, lErr := w.db.AcquireM2MSettlementLock(ctx, intent.ID)
			if lErr == nil && lTx != nil {
				_ = w.db.MarkM2MFailed(ctx, lTx, intent.ID, err.Error(), true)
			}
			w.bus.Publish(Event{
				Type:    "m2m.settlement.failed",
				OrderID: intent.ID,
				Payload: map[string]any{
					"intent_id":    intent.ID,
					"permanent":    true,
					"error":        err.Error(),
					"attempts":     intent.Attempts,
					"payment_type": string(intent.PaymentType),
					"amount_brl":   intent.AmountBRL,
				},
			})
			return err
		}
		return nil
	}

	retryCfg := resilience.RetryConfig{
		MaxAttempts: m2mMaxAttempts,
		BaseDelay:   3 * time.Second,
		MaxDelay:    30 * time.Second,
		Multiplier:  2.0,
		Jitter:      true,
	}

	var endToEndID, efiStatus string
	err := resilience.DoWithContext(ctx, retryCfg, "m2m.settlement."+intent.ID,
		func(e error) bool {
			if e == nil {
				return false
			}
			msg := strings.ToLower(e.Error())
			// Never retry hard business-logic/configuration failures.
			for _, perm := range []string{"chave_invalida", "cpf_invalido", "conta_bloqueada", "status 4", "nao configurado", "destino de cartao ausente"} {
				if strings.Contains(msg, perm) {
					return false
				}
			}
			return true
		},
		func(ctx context.Context) error {
			id, st, callErr := w.callEfiSettlement(ctx, intent)
			if callErr != nil {
				return callErr
			}
			endToEndID = id
			efiStatus = st
			return nil
		},
	)

	permanent := false
	if err != nil {
		// Determine if this is a permanent business/configuration failure.
		msg := strings.ToLower(err.Error())
		for _, p := range []string{"chave_invalida", "cpf_invalido", "conta_bloqueada", "nao configurado", "destino de cartao ausente"} {
			if strings.Contains(msg, p) {
				permanent = true
				break
			}
		}
		// Re-acquire lock for the failure update.
		_, lTx, lErr := w.db.AcquireM2MSettlementLock(ctx, intent.ID)
		if lErr == nil && lTx != nil {
			_ = w.db.MarkM2MFailed(ctx, lTx, intent.ID, err.Error(), permanent)
		}
		// Publish m2m.settlement.failed to the bus so the webhook dispatcher
		// can fan it out to subscribers (canonical event: m2m.settlement.failed).
		// Permanent failures are published immediately; transient failures are
		// enqueued on the DLQ and also published so subscribers are notified early.
		w.bus.Publish(Event{
			Type:    "m2m.settlement.failed",
			OrderID: intent.ID,
			Payload: map[string]any{
				"intent_id":    intent.ID,
				"permanent":    permanent,
				"error":        err.Error(),
				"attempts":     intent.Attempts,
				"payment_type": string(intent.PaymentType),
				"amount_brl":   intent.AmountBRL,
			},
		})
		if !permanent {
			w.dlq.Push(Event{Type: "m2m.settlement.failed", OrderID: intent.ID}, intent.Attempts, err.Error())
		}
		return err
	}

	// ── Success ──────────────────────────────────────────────────────────────
	_, lTx, lErr := w.db.AcquireM2MSettlementLock(ctx, intent.ID)
	if lErr != nil || lTx == nil {
		// Best-effort — if we can't write settled, the poller will retry.
		// The fiat payout already happened, so we log aggressively.
		slog.Error("M2MSettlement: CRITICAL — fiat payout confirmed but failed to record settled status",
			"intent_id", intent.ID, "efi_end_to_end_id", endToEndID)
		return nil
	}
	if err := w.db.MarkM2MSettled(ctx, lTx, intent.ID, endToEndID, efiStatus); err != nil {
		slog.Error("M2MSettlement: CRITICAL — mark settled failed",
			"intent_id", intent.ID, "efi_end_to_end_id", endToEndID, "err", err)
		return nil
	}

	w.bus.Publish(Event{
		Type:    "m2m.settlement.done",
		OrderID: intent.ID,
		Payload: map[string]any{
			"efi_end_to_end_id": endToEndID,
			"efi_status":        efiStatus,
			"amount_brl":        intent.AmountBRL,
		},
	})
	return nil
}

// queueCardPayment publishes the destination payment data for the operator/RPA.
// The intent remains in "settling" until the real fiat payment is confirmed.
func (w *M2MSettlementWorker) queueCardPayment(_ context.Context, intent *database.AgentPaymentIntent) error {
	if intent.PaymentLink == "" && intent.Barcode == "" {
		return fmt.Errorf("destino de cartao ausente na intent %s: payment_link ou barcode obrigatorio", intent.ID)
	}
	w.bus.Publish(Event{
		Type:    "m2m.card_payment.due",
		OrderID: intent.ID,
		Payload: map[string]any{
			"intent_id":        intent.ID,
			"payment_type":     string(intent.PaymentType),
			"amount_brl":       fmt.Sprintf("%.2f", intent.AmountBRL),
			"payment_link":     intent.PaymentLink,
			"barcode":          intent.Barcode,
			"beneficiary_name": intent.BeneficiaryName,
			"due_date":         intent.DueDate,
			"agent_wallet":     intent.AgentWallet,
			"deposit_tx":       intent.DepositTx,
		},
	})
	return nil
}

// callEfiSettlement routes PIX intents to Efí.
// Returns (provider settlement ID, provider status, error).
func (w *M2MSettlementWorker) callEfiSettlement(ctx context.Context, intent *database.AgentPaymentIntent) (string, string, error) {
	switch intent.PaymentType {
	case database.M2MTypePix:
		if w.cfg.EfiClientID == "" || w.cfg.EfiClientSecret == "" {
			return "", "", fmt.Errorf("EFI_CLIENT_ID / EFI_CLIENT_SECRET nao configurados")
		}
		if intent.PixKey == "" {
			return "", "", fmt.Errorf("pix_key ausente na intent %s", intent.ID)
		}
		return w.callEfiPix(ctx, intent.ID, intent.PixKey, intent.AmountBRL)
	default:
		return "", "", fmt.Errorf("payment_type nao suportado: %s", intent.PaymentType)
	}
}

func (w *M2MSettlementWorker) callEfiPix(ctx context.Context, intentID, pixKey string, amountBRL float64) (string, string, error) {
	token, err := w.getEfiToken(ctx)
	if err != nil {
		return "", "", fmt.Errorf("efi auth: %w", err)
	}

	payload := map[string]any{
		"valor":       fmt.Sprintf("%.2f", amountBRL),
		"chave":       pixKey,
		"infoPagador": fmt.Sprintf("ChainFX M2M intent %s", intentID),
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		w.cfg.EfiApiBaseURL+"/v2/gn/pix/"+intentID, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := w.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("efi pix request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("efi pix status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		EndToEndID string `json:"endToEndId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", "", fmt.Errorf("efi response parse: %w", err)
	}
	if result.EndToEndID == "" {
		return "", "", fmt.Errorf("efi: endToEndId vazio na resposta")
	}
	return result.EndToEndID, result.Status, nil
}

// getEfiToken fetches a short-lived Efí OAuth2 client-credentials token.
func (w *M2MSettlementWorker) getEfiToken(ctx context.Context) (string, error) {
	body := strings.NewReader("grant_type=client_credentials")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		w.cfg.EfiApiBaseURL+"/v1/authorize", body)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(w.cfg.EfiClientID, w.cfg.EfiClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("efi token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("efi token status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("efi: access_token vazio")
	}
	return result.AccessToken, nil
}

// checkDailyOutflow guards against exceeding the configured daily settlement
// limit (M2M_MAX_DAILY_OUTFLOW_BRL). A value of 0 disables the check.
func (w *M2MSettlementWorker) checkDailyOutflow(ctx context.Context, thisBRL float64) error {
	maxBRL := w.cfg.M2MMaxDailyOutflowBRL
	if maxBRL <= 0 {
		return nil // unlimited
	}
	used, err := w.db.M2MDailyOutflowBRL(ctx)
	if err != nil {
		slog.Warn("M2MSettlement: erro ao verificar daily outflow", "err", err)
		return fmt.Errorf("daily outflow cap unavailable: %w", err)
	}
	if used+thisBRL > maxBRL {
		return fmt.Errorf("daily outflow cap atingido: used=%.2f + this=%.2f > max=%.2f BRL",
			used, thisBRL, maxBRL)
	}
	return nil
}
