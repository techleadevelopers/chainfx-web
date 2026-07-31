package workers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/liquidity"
	"payment-gateway/internal/money"
)

// DCAWorker executes due Dollar-Cost-Averaging buy strategies.
// Uses SELECT FOR UPDATE SKIP LOCKED so multiple pod instances never
// process the same strategy simultaneously.
//
// Accounting contract:
//   - A dca_executions row is created with a deterministic cycle identity before
//     creating the downstream buy_order.
//   - total_invested / total_tokens on dca_strategies are ONLY updated when the
//     downstream buy.sent event is confirmed, preventing phantom balances on failure.
//   - On any failure the execution row is moved to retry_wait and dca.execution.failed is published.
type DCAWorker struct {
	bus    *EventBus
	db     *database.DB
	cfg    *config.Config
	dlq    *DeadLetterQueue
	router *liquidity.Router
	prices interface {
		GetPrice(string) float64
	}
}

func NewDCAWorker(bus *EventBus, db *database.DB, cfg *config.Config, prices interface {
	GetPrice(string) float64
}) *DCAWorker {
	var client *http.Client = httpclient.Default()
	return &DCAWorker{
		bus:    bus,
		db:     db,
		cfg:    cfg,
		dlq:    NewPersistentDLQ(db, 500),
		router: newBuyLiquidityRouter(cfg, client),
		prices: prices,
	}
}

func (dw *DCAWorker) Start(ctx context.Context) {
	slog.Info("DCAWorker iniciado — verificando estratégias a cada minuto")
	dw.dlq.StartPeriodicLog(ctx, 5*time.Minute)

	// Subscribe to buy.sent events to confirm DCA cycle accounting.
	sentChan := dw.bus.Subscribe("buy.sent")
	go dw.listenBuySent(ctx, sentChan)

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("DCAWorker: encerrando")
			return
		case <-ticker.C:
			dw.runDue(ctx)
		}
	}
}

// listenBuySent watches for buy.sent events and confirms any pending DCA execution
// tied to that buy_order_id, updating total_invested/total_tokens only at this point.
func (dw *DCAWorker) listenBuySent(ctx context.Context, ch <-chan Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if evt.Type != "buy.sent" || evt.OrderID == "" {
				continue
			}
			go dw.confirmDCAExecution(ctx, evt.OrderID)
		}
	}
}

// confirmDCAExecution is called when buy.sent fires for a buy_order that may belong
// to a DCA cycle. It marks the execution 'completed' and atomically credits
// total_invested and total_tokens on the parent strategy.
func (dw *DCAWorker) confirmDCAExecution(ctx context.Context, buyOrderID string) {
	var execID, strategyID string
	var fundingWallet sql.NullString
	var requiredUSDTMicro sql.NullInt64
	var amountBRLText, cryptoAmountText string

	tx, err := dw.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("DCAWorker: erro ao iniciar tx confirmacao DCA",
			"buy_order_id", buyOrderID, "err", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	err = tx.QueryRowContext(ctx, `
		UPDATE dca_executions
		SET    status='completed', completed_at=COALESCE(completed_at, NOW()), updated_at=NOW()
		WHERE  buy_order_id = $1::uuid
		  AND  status IN ('processing','submitted','pending')
		RETURNING id, strategy_id, amount_brl::text, COALESCE(crypto_amount, 0)::text, funding_wallet_address, required_usdt_micro
	`, buyOrderID).Scan(&execID, &strategyID, &amountBRLText, &cryptoAmountText, &fundingWallet, &requiredUSDTMicro)
	if err == sql.ErrNoRows {
		return // Not a DCA buy, or already confirmed.
	}
	if err != nil {
		slog.Warn("DCAWorker: erro ao marcar execucao completed",
			"buy_order_id", buyOrderID, "err", err)
		return
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE dca_strategies
		SET    total_invested = total_invested + e.amount_brl,
		       total_tokens   = total_tokens   + COALESCE(e.crypto_amount, 0),
		       updated_at     = NOW()
		FROM   dca_executions e
		WHERE  dca_strategies.id = $1
		  AND  e.id = $2::uuid
		  AND  e.strategy_id = dca_strategies.id
	`, strategyID, execID); err != nil {
		slog.Warn("DCAWorker: erro ao atualizar stats DCA apos buy.sent",
			"strategy_id", strategyID, "err", err)
		return
	}
	if fundingWallet.Valid && requiredUSDTMicro.Valid && requiredUSDTMicro.Int64 > 0 {
		if err := txCaptureDCAFunding(ctx, tx, fundingWallet.String, execID, requiredUSDTMicro.Int64); err != nil {
			slog.Warn("DCAWorker: erro ao capturar funding DCA", "exec_id", execID, "err", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("DCAWorker: erro ao commitar confirmacao DCA",
			"exec_id", execID, "err", err)
		return
	}

	slog.Info("DCAWorker: execucao DCA confirmada e stats atualizados",
		"exec_id", execID, "strategy_id", strategyID,
		"amount_brl", amountBRLText, "crypto_amount", cryptoAmountText)
	_ = dw.db.AddBuyEvent(ctx, buyOrderID, "dca.execution.confirmed", map[string]any{
		"strategy_id":   strategyID,
		"exec_id":       execID,
		"amount_brl":    amountBRLText,
		"crypto_amount": cryptoAmountText,
	})
}

type dcaStrategy struct {
	ID          string
	UserID      string
	TokenSymbol string
	Network     string
	AmountBRL   money.MoneyMinor
	Frequency   string
	ScheduledAt time.Time
	ExecutionID string
	BuyOrderID  string
}

func (s dcaStrategy) amountBRLString() string {
	return s.AmountBRL.String()
}

func (s dcaStrategy) amountBRLBoundaryFloat() float64 {
	return s.AmountBRL.Float64()
}

func (dw *DCAWorker) runDue(ctx context.Context) {
	strategies := dw.claimRecoverable(ctx)
	strategies = append(strategies, dw.claimDue(ctx)...)
	if len(strategies) == 0 {
		return
	}
	slog.Info("DCAWorker: executando estrategias", "count", len(strategies))
	for _, s := range strategies {
		go dw.execute(ctx, s)
	}
}

func (dw *DCAWorker) claimRecoverable(ctx context.Context) []dcaStrategy {
	tx, err := dw.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("DCAWorker: erro ao iniciar transacao recovery", "err", err)
		return nil
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		SELECT e.id::text, e.strategy_id::text, e.user_id::text, e.token_symbol, e.network,
		       e.amount_brl::text, e.frequency, e.scheduled_at, COALESCE(e.buy_order_id::text, '')
		FROM dca_executions e
		WHERE e.status IN ('claimed','processing','retry_wait')
		  AND e.next_attempt_at <= NOW()
		ORDER BY e.scheduled_at ASC
		LIMIT 50
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		slog.Warn("DCAWorker: erro ao buscar execucoes recovery", "err", err)
		return nil
	}
	defer rows.Close()

	var strategies []dcaStrategy
	for rows.Next() {
		var s dcaStrategy
		var amountText string
		if err := rows.Scan(&s.ExecutionID, &s.ID, &s.UserID, &s.TokenSymbol, &s.Network, &amountText, &s.Frequency, &s.ScheduledAt, &s.BuyOrderID); err != nil {
			slog.Warn("DCAWorker: scan recovery error", "err", err)
			continue
		}
		amount, err := dcaParseBRL(amountText)
		if err != nil {
			slog.Warn("DCAWorker: amount_brl invalido em recovery", "strategy_id", s.ID, "err", err)
			continue
		}
		s.AmountBRL = amount
		strategies = append(strategies, s)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("DCAWorker: erro ao iterar recovery", "err", err)
		return nil
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("DCAWorker: erro ao commitar recovery", "err", err)
		return nil
	}
	return strategies
}

func (dw *DCAWorker) claimDue(ctx context.Context) []dcaStrategy {
	tx, err := dw.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("DCAWorker: erro ao iniciar transacao", "err", err)
		return nil
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		SELECT id, user_id, token_symbol, network, amount_brl::text, frequency, next_execution
		FROM   dca_strategies
		WHERE  active = true
		  AND  cancelled_at IS NULL
		  AND  reconciliation_hold_at IS NULL
		  AND  next_execution <= NOW()
		ORDER  BY next_execution ASC
		LIMIT  50
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		slog.Warn("DCAWorker: erro ao buscar estrategias", "err", err)
		return nil
	}

	var strategies []dcaStrategy
	for rows.Next() {
		var s dcaStrategy
		var amountText string
		if err := rows.Scan(&s.ID, &s.UserID, &s.TokenSymbol, &s.Network, &amountText, &s.Frequency, &s.ScheduledAt); err != nil {
			slog.Warn("DCAWorker: scan error", "err", err)
			continue
		}
		amount, err := dcaParseBRL(amountText)
		if err != nil {
			slog.Warn("DCAWorker: amount_brl invalido", "strategy_id", s.ID, "err", err)
			continue
		}
		s.AmountBRL = amount
		execID, claimed, err := dw.claimExecutionWindow(ctx, tx, s)
		if err != nil {
			slog.Warn("DCAWorker: erro ao claim janela DCA", "strategy_id", s.ID, "err", err)
			continue
		}
		if !claimed {
			continue
		}
		s.ExecutionID = execID
		strategies = append(strategies, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("DCAWorker: erro ao iterar rows", "err", err)
		return nil
	}
	if len(strategies) == 0 {
		return nil
	}

	for _, s := range strategies {
		next := nextExecutionFrom(s.Frequency, s.ScheduledAt)
		if _, err := tx.ExecContext(ctx,
			"UPDATE dca_strategies SET next_execution=$1, updated_at=NOW() WHERE id=$2 AND active=true AND cancelled_at IS NULL AND reconciliation_hold_at IS NULL", next, s.ID); err != nil {
			slog.Warn("DCAWorker: erro ao agendar proxima execucao", "strategy_id", s.ID, "err", err)
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("DCAWorker: erro ao commitar transacao", "err", err)
		return nil
	}
	return strategies
}
func (dw *DCAWorker) execute(ctx context.Context, s dcaStrategy) {
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	slog.Info("DCAWorker: executando DCA",
		"strategy_id", s.ID, "execution_id", s.ExecutionID, "scheduled_at", s.ScheduledAt,
		"user_id", s.UserID, "token", s.TokenSymbol, "network", s.Network, "amount_brl", s.amountBRLString())

	if s.ExecutionID == "" {
		dw.markExecutionFailed(ctx, "", s, fmt.Errorf("execucao DCA sem execution_id"))
		return
	}

	if dw.cfg.AllowSimulations && !dw.cfg.IsProduction() {
		if _, err := dw.db.SQL.ExecContext(execCtx, `
			UPDATE dca_executions
			SET status='completed', completed_at=NOW(), updated_at=NOW()
			WHERE id=$1::uuid AND status IN ('claimed','processing','retry_wait')`, s.ExecutionID); err != nil {
			slog.Warn("DCAWorker: erro ao concluir simulacao", "strategy_id", s.ID, "err", err)
		} else {
			slog.Info("DCAWorker: DCA simulado concluido", "strategy_id", s.ID)
		}
		return
	}

	ok, err := dw.transitionExecutionProcessing(execCtx, s.ExecutionID)
	if err != nil {
		dw.markExecutionFailed(ctx, s.ExecutionID, s, err)
		return
	}
	if !ok {
		return
	}

	reservedWallet := ""
	reservedMicro := int64(0)
	submitted := false
	fail := func(reason string, origErr error) {
		errMsg := reason
		if origErr != nil {
			errMsg = origErr.Error()
		}
		slog.Warn("DCAWorker: "+reason, "strategy_id", s.ID, "execution_id", s.ExecutionID, "err", origErr)
		if reservedWallet != "" && reservedMicro > 0 && !submitted {
			_ = dw.releaseDCAFunding(ctx, reservedWallet, s.ExecutionID, reservedMicro)
		}
		dw.markExecutionFailed(ctx, s.ExecutionID, s, origErr)
		dw.dlq.Push(Event{
			Type:    "dca.buy.requested",
			OrderID: s.ID,
			Payload: map[string]any{
				"user_id": s.UserID, "asset": s.TokenSymbol,
				"network": s.Network, "amount_brl": s.amountBRLString(),
				"source": "dca", "strategy_id": s.ID, "execution_id": s.ExecutionID,
				"scheduled_at": s.ScheduledAt, "error": errMsg,
			},
		}, 1, errMsg)
	}

	destAddress, err := dw.userWalletAddress(execCtx, s.UserID, s.Network)
	if err != nil {
		fail("erro ao buscar carteira do usuario", err)
		return
	}
	if destAddress == "" {
		fail("usuario sem carteira para DCA", fmt.Errorf("usuario sem carteira configurada para rede %s", s.Network))
		return
	}
	if !dw.dcaPairExecutable(s) {
		fail("par sem rota executavel", fmt.Errorf("par DCA sem rota executavel: %s/%s", s.TokenSymbol, s.Network))
		return
	}

	feeBRL, payoutBRL := dw.dcaFeeAndPayout(s.AmountBRL)
	if payoutBRL <= 0 {
		fail("valor DCA invalido apos taxa", fmt.Errorf("payout_brl %s invalido", payoutBRL.String()))
		return
	}

	quote, err := dw.resolveRateAndAmount(execCtx, s, destAddress, feeBRL, payoutBRL)
	if err != nil {
		fail("erro ao obter cotacao para DCA", err)
		return
	}

	quoteExpiresAt := time.Now().UTC().Add(5 * time.Minute)
	if _, err := dw.db.SQL.ExecContext(execCtx, `
		UPDATE dca_executions
		SET crypto_amount=$1, rate_brl=$2, quote_expires_at=$3, updated_at=NOW()
		WHERE id=$4::uuid AND status='processing'`, quote.CryptoAmount, quote.RateBRL, quoteExpiresAt, s.ExecutionID); err != nil {
		fail("erro ao atualizar execucao DCA com cotacao", err)
		return
	}

	fundingWallet, err := dw.userFundingWalletAddress(execCtx, s.UserID)
	if err != nil {
		fail("erro ao buscar wallet de funding DCA", err)
		return
	}
	requiredUSDTMicro, err := dw.requiredUSDTMicroForBRL(s.AmountBRL)
	if err != nil {
		fail("erro ao calcular funding DCA", err)
		return
	}
	if err := dw.reserveDCAFunding(execCtx, fundingWallet, s.ExecutionID, requiredUSDTMicro); err != nil {
		fail("saldo USDT insuficiente para DCA", err)
		return
	}
	reservedWallet = fundingWallet
	reservedMicro = requiredUSDTMicro

	quote.RequiredUSDT = requiredUSDTMicro
	buy, err := dw.createPaidBuyOrder(execCtx, s, destAddress, quote)
	if err != nil {
		fail("erro ao criar buy order para DCA", err)
		return
	}
	submitted = true

	res, err := dw.db.SQL.ExecContext(execCtx, `
		UPDATE dca_executions
		SET buy_order_id=$1::uuid, status='submitted', submitted_at=COALESCE(submitted_at, NOW()), updated_at=NOW()
		WHERE id=$2::uuid AND status IN ('submitted','processing') AND quote_expires_at > NOW()`, buy.ID, s.ExecutionID)
	if err != nil {
		fail("erro ao vincular buy order na execucao DCA", err)
		return
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		_, _ = dw.db.SQL.ExecContext(execCtx, `
			UPDATE dca_executions
			SET buy_order_id=$1::uuid, status='provider_unknown', provider_status='quote_expired_after_buy_order',
			    error_message='quote expirada apos criar buy order DCA', updated_at=NOW()
			WHERE id=$2::uuid AND status NOT IN ('completed','manual_review')`, buy.ID, s.ExecutionID)
		return
	}

	dw.bus.Publish(Event{
		Type:    "buy.paid",
		OrderID: buy.ID,
		Payload: map[string]any{
			"user_id": s.UserID, "asset": s.TokenSymbol, "token_symbol": s.TokenSymbol,
			"network": s.Network, "amount_brl": quote.PayoutBRL.String(), "dest_address": destAddress,
			"source": "dca", "strategy_id": s.ID, "execution_id": s.ExecutionID,
			"scheduled_at": s.ScheduledAt,
		},
	})
}

func (dw *DCAWorker) claimExecutionWindow(ctx context.Context, tx *sql.Tx, s dcaStrategy) (string, bool, error) {
	if s.ScheduledAt.IsZero() {
		return "", false, fmt.Errorf("scheduled_at vazio")
	}
	operationID := dcaOperationID(s.ID, s.ScheduledAt)
	var execID string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO dca_executions
		  (strategy_id, scheduled_at, user_id, token_symbol, network, frequency, amount_brl, status, operation_id, claimed_at, next_attempt_at)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, 'claimed', $8, NOW(), NOW())
		ON CONFLICT (strategy_id, scheduled_at) DO NOTHING
		RETURNING id::text
	`, s.ID, s.ScheduledAt.UTC(), s.UserID, s.TokenSymbol, s.Network, s.Frequency, s.AmountBRL.String(), operationID).Scan(&execID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return execID, true, nil
}

func (dw *DCAWorker) transitionExecutionProcessing(ctx context.Context, execID string) (bool, error) {
	var claimed string
	err := dw.db.SQL.QueryRowContext(ctx, `
		UPDATE dca_executions e
		SET status='processing', processing_at=COALESCE(processing_at, NOW()), attempts=attempts+1, updated_at=NOW()
		FROM dca_strategies s
		WHERE e.id=$1::uuid
		  AND s.id=e.strategy_id
		  AND s.active=true
		  AND s.cancelled_at IS NULL
		  AND s.reconciliation_hold_at IS NULL
		  AND (
		    e.status IN ('claimed','retry_wait')
		    OR (e.status='processing' AND e.processing_at < NOW() - interval '2 minutes')
		  )
		  AND e.buy_order_id IS NULL
		  AND e.next_attempt_at <= NOW()
		RETURNING e.id::text
	`, execID).Scan(&claimed)
	if err == sql.ErrNoRows {
		_, _ = dw.db.SQL.ExecContext(ctx, `
			UPDATE dca_executions e
			SET status='skipped_paused', updated_at=NOW()
			FROM dca_strategies s
			WHERE e.id=$1::uuid
			  AND s.id=e.strategy_id
			  AND (s.active=false OR s.cancelled_at IS NOT NULL OR s.reconciliation_hold_at IS NOT NULL)
			  AND e.status IN ('claimed','retry_wait')
			  AND e.buy_order_id IS NULL`, execID)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (dw *DCAWorker) markExecutionSubmitted(ctx context.Context, execID string) (bool, error) {
	var claimed string
	err := dw.db.SQL.QueryRowContext(ctx, `
		UPDATE dca_executions
		SET status='submitted', submitted_at=COALESCE(submitted_at, NOW()), updated_at=NOW()
		WHERE id=$1::uuid AND status='processing' AND quote_expires_at > NOW()
		RETURNING id::text
	`, execID).Scan(&claimed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func dcaOperationID(strategyID string, scheduledAt time.Time) string {
	return "dca:" + strategyID + ":" + scheduledAt.UTC().Format(time.RFC3339Nano)
}

func (dw *DCAWorker) userFundingWalletAddress(ctx context.Context, userID string) (string, error) {
	var wallet sql.NullString
	if err := dw.db.SQL.QueryRowContext(ctx, `
		SELECT wallet_address
		FROM users
		WHERE id=$1::uuid AND deleted_at IS NULL`, userID).Scan(&wallet); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("usuario nao encontrado")
		}
		return "", err
	}
	if strings.TrimSpace(wallet.String) == "" {
		return "", fmt.Errorf("usuario sem wallet USDT interna")
	}
	return strings.ToLower(strings.TrimSpace(wallet.String)), nil
}

func (dw *DCAWorker) requiredUSDTMicroForBRL(amountBRL money.MoneyMinor) (int64, error) {
	if amountBRL <= 0 {
		return 0, fmt.Errorf("amount_brl invalido")
	}
	if dw == nil || dw.prices == nil {
		return 0, fmt.Errorf("price cache indisponivel")
	}
	usdtBRL := dw.prices.GetPrice("BRL")
	if usdtBRL <= 0 {
		return 0, fmt.Errorf("cotacao USDT/BRL indisponivel")
	}
	rate, err := dcaDecimalFromBoundaryFloat(usdtBRL, 12)
	if err != nil {
		return 0, err
	}
	return dcaUSDTMicroCeil(amountBRL, rate)
}

func (dw *DCAWorker) reserveDCAFunding(ctx context.Context, wallet, execID string, requiredMicro int64) error {
	return withDCAFundingTx(ctx, dw.db.SQL, execID, func(tx *sql.Tx) error {
		var reservedAt, capturedAt, releasedAt sql.NullTime
		var existingWallet sql.NullString
		var existingMicro sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT funding_wallet_address, required_usdt_micro, reserved_at, captured_at, released_at
			FROM dca_executions
			WHERE id=$1::uuid
			FOR UPDATE`, execID).Scan(&existingWallet, &existingMicro, &reservedAt, &capturedAt, &releasedAt); err != nil {
			return err
		}
		if capturedAt.Valid || releasedAt.Valid {
			return fmt.Errorf("execucao DCA ja terminal no funding")
		}
		if reservedAt.Valid {
			if !strings.EqualFold(existingWallet.String, wallet) || existingMicro.Int64 != requiredMicro {
				return fmt.Errorf("reserva DCA existente diverge do payload")
			}
			return nil
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE nfc_wallet_balances
			SET available_usdt_micro = available_usdt_micro - $2,
			    locked_usdt_micro = locked_usdt_micro + $2,
			    updated_at = NOW()
			WHERE lower(wallet_address)=lower($1)
			  AND network='BSC'
			  AND asset='USDT'
			  AND available_usdt_micro >= $2`, wallet, requiredMicro)
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows != 1 {
			return fmt.Errorf("saldo USDT insuficiente")
		}
		if err := txInsertDCAFundingLedger(ctx, tx, wallet, execID, "dca_execution_reserve", -requiredMicro, requiredMicro); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE dca_executions
			SET funding_wallet_address=$2,
			    required_usdt_micro=$3,
			    reserved_at=COALESCE(reserved_at, NOW()),
			    updated_at=NOW()
			WHERE id=$1::uuid`, execID, wallet, requiredMicro)
		return err
	})
}

func (dw *DCAWorker) releaseDCAFunding(ctx context.Context, wallet, execID string, requiredMicro int64) error {
	return withDCAFundingTx(ctx, dw.db.SQL, execID, func(tx *sql.Tx) error {
		return txReleaseDCAFunding(ctx, tx, wallet, execID, requiredMicro)
	})
}

func txCaptureDCAFunding(ctx context.Context, tx *sql.Tx, wallet, execID string, requiredMicro int64) error {
	var reservedAt, capturedAt, releasedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT reserved_at, captured_at, released_at
		FROM dca_executions
		WHERE id=$1::uuid
		FOR UPDATE`, execID).Scan(&reservedAt, &capturedAt, &releasedAt); err != nil {
		return err
	}
	if capturedAt.Valid {
		return nil
	}
	if releasedAt.Valid {
		return fmt.Errorf("execucao DCA ja liberou funding")
	}
	if !reservedAt.Valid {
		return fmt.Errorf("execucao DCA sem funding reservado")
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE nfc_wallet_balances
		SET locked_usdt_micro = locked_usdt_micro - $3,
		    updated_at = NOW()
		WHERE lower(wallet_address)=lower($1)
		  AND network=$2
		  AND asset='USDT'
		  AND locked_usdt_micro >= $3`, wallet, "BSC", requiredMicro)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows != 1 {
		return fmt.Errorf("locked USDT insuficiente para capturar DCA")
	}
	if err := txInsertDCAFundingLedger(ctx, tx, wallet, execID, "dca_execution_capture", 0, -requiredMicro); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE dca_executions
		SET captured_at=COALESCE(captured_at, NOW()), updated_at=NOW()
		WHERE id=$1::uuid`, execID)
	return err
}

func txReleaseDCAFunding(ctx context.Context, tx *sql.Tx, wallet, execID string, requiredMicro int64) error {
	var reservedAt, capturedAt, releasedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT reserved_at, captured_at, released_at
		FROM dca_executions
		WHERE id=$1::uuid
		FOR UPDATE`, execID).Scan(&reservedAt, &capturedAt, &releasedAt); err != nil {
		return err
	}
	if releasedAt.Valid || capturedAt.Valid || !reservedAt.Valid {
		return nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE nfc_wallet_balances
		SET available_usdt_micro = available_usdt_micro + $3,
		    locked_usdt_micro = locked_usdt_micro - $3,
		    updated_at = NOW()
		WHERE lower(wallet_address)=lower($1)
		  AND network=$2
		  AND asset='USDT'
		  AND locked_usdt_micro >= $3`, wallet, "BSC", requiredMicro)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows != 1 {
		return fmt.Errorf("locked USDT insuficiente para liberar DCA")
	}
	if err := txInsertDCAFundingLedger(ctx, tx, wallet, execID, "dca_execution_release", requiredMicro, -requiredMicro); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE dca_executions
		SET released_at=COALESCE(released_at, NOW()), updated_at=NOW()
		WHERE id=$1::uuid`, execID)
	return err
}

func withDCAFundingTx(ctx context.Context, db *sql.DB, execID string, fn func(*sql.Tx) error) error {
	if strings.TrimSpace(execID) == "" {
		return fmt.Errorf("execution_id obrigatorio")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func txInsertDCAFundingLedger(ctx context.Context, tx *sql.Tx, wallet, execID, source string, availableDelta, lockedDelta int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO mobile_wallet_ledger_entries
		  (id, wallet_address, network, asset, source, reference_id, available_delta_micro, locked_delta_micro)
		VALUES ($1,$2,'BSC','USDT',$3,$4,$5,$6)
		ON CONFLICT (id) DO NOTHING`,
		"mwle_"+dcaHashString(execID + ":" + source)[:24], wallet, source, execID, availableDelta, lockedDelta)
	return err
}

func dcaHashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// resolveRateAndAmount obtains the BRL rate and crypto amount for a DCA cycle.
// When the liquidity router is enabled it attempts to fetch a real provider quote;
// on failure (or when router is disabled) it falls back to the price-cache rate
// with the configured spread.
func (dw *DCAWorker) resolveRateAndAmount(ctx context.Context, s dcaStrategy, destAddress string, feeBRL, payoutBRL money.MoneyMinor) (dcaQuoteSnapshot, error) {
	snapshot := dcaQuoteSnapshot{
		AmountBRL: s.AmountBRL,
		FeeBRL:    feeBRL,
		PayoutBRL: payoutBRL,
	}
	if dw.cfg.LiquidityRouterEnabled && dw.router != nil {
		pair, ok := resolveLiquidityPair(dw.cfg, s.TokenSymbol, s.Network)
		if ok {
			timeout := 2500 * time.Millisecond
			if ms := dw.cfg.LiquidityQuoteTimeoutMs; ms > 0 {
				timeout = time.Duration(ms) * time.Millisecond
			}
			qCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// Use a provisional cache-rate amount so providers can compute fees
			cacheRate := dw.dcaBuyRate(s.TokenSymbol)
			var provisionalCrypto float64
			if cacheRate > 0 {
				provisionalCrypto = payoutBRL.Float64() / cacheRate
			}

			req := liquidity.Request{
				OrderID:      "dca-quote-" + s.ID,
				UserID:       s.UserID,
				Asset:        pair.Asset,
				Network:      pair.Network,
				FiatCurrency: "BRL",
				AmountBRL:    payoutBRL.Float64(),
				CryptoAmount: provisionalCrypto,
				DestAddress:  destAddress,
				CreatedAt:    time.Now().UTC(),
			}

			// Collect quotes from all providers and pick the best (most crypto out).
			allQuotes := dw.router.QuoteAll(qCtx, req)
			bestCrypto := 0.0
			bestRateText := ""
			bestCryptoText := ""
			for _, q := range allQuotes {
				if q.CryptoAmount <= 0 || q.FiatCostBRL <= 0 {
					continue
				}
				impliedRate := q.FiatCostBRL / q.CryptoAmount
				rateText, err := dcaDecimalFromBoundaryFloat(impliedRate, 12)
				if err != nil {
					continue
				}
				cryptoText, err := dcaDecimalFromBoundaryFloat(q.CryptoAmount, dcaCryptoDisplayDecimals)
				if err != nil {
					continue
				}
				if bestCrypto == 0 || q.CryptoAmount > bestCrypto {
					bestCrypto = q.CryptoAmount
					bestRateText = rateText
					bestCryptoText = cryptoText
				}
			}
			if bestCrypto > 0 && bestRateText != "" && bestCryptoText != "" {
				rateFloat, err := dcaBoundaryFloat(bestRateText)
				if err != nil {
					return snapshot, err
				}
				cryptoFloat, err := dcaBoundaryFloat(bestCryptoText)
				if err != nil {
					return snapshot, err
				}
				snapshot.RateBRL = bestRateText
				snapshot.CryptoAmount = bestCryptoText
				snapshot.boundaryRate = rateFloat
				snapshot.boundaryCrypto = cryptoFloat
				slog.Info("DCAWorker: cotacao real obtida do router",
					"strategy_id", s.ID, "rate_brl", snapshot.RateBRL, "crypto_amount", snapshot.CryptoAmount)
				return snapshot, nil
			}
			slog.Info("DCAWorker: nenhuma cotacao do router; usando cache de precos",
				"strategy_id", s.ID)
		}
	}

	// Fallback: price-cache rate with configured spread
	cacheRate := dw.dcaBuyRate(s.TokenSymbol)
	if cacheRate <= 0 {
		return snapshot, fmt.Errorf("cotacao indisponivel para %s", s.TokenSymbol)
	}
	rateText, err := dcaDecimalFromBoundaryFloat(cacheRate, 12)
	if err != nil {
		return snapshot, err
	}
	cryptoText, err := dcaCryptoFromFiat(payoutBRL, rateText)
	if err != nil {
		return snapshot, err
	}
	cryptoFloat, err := dcaBoundaryFloat(cryptoText)
	if err != nil {
		return snapshot, err
	}
	snapshot.RateBRL = rateText
	snapshot.CryptoAmount = cryptoText
	snapshot.boundaryRate = cacheRate
	snapshot.boundaryCrypto = cryptoFloat
	return snapshot, nil
}

// markExecutionFailed parks recoverable DCA failures without creating a second
// economic operation for the same scheduled cycle.
func (dw *DCAWorker) markExecutionFailed(ctx context.Context, execID string, s dcaStrategy, origErr error) {
	errMsg := ""
	if origErr != nil {
		errMsg = origErr.Error()
	}
	if execID != "" {
		_, _ = dw.db.SQL.ExecContext(ctx, `
			UPDATE dca_executions
			SET    status=CASE WHEN submitted_at IS NOT NULL OR buy_order_id IS NOT NULL THEN 'provider_unknown' ELSE 'retry_wait' END,
			       provider_status=CASE WHEN submitted_at IS NOT NULL OR buy_order_id IS NOT NULL THEN 'submit_ambiguous' ELSE provider_status END,
			       error_message=$1,
			       next_attempt_at=NOW() + interval '5 minutes',
			       updated_at=NOW()
			WHERE  id=$2
			  AND  status NOT IN ('completed','manual_review')
		`, errMsg, execID)
	}
	dw.bus.Publish(Event{
		Type:    "dca.execution.failed",
		OrderID: s.ID,
		Payload: map[string]any{
			"strategy_id": s.ID,
			"exec_id":     execID,
			"user_id":     s.UserID,
			"asset":       s.TokenSymbol,
			"network":     s.Network,
			"amount_brl":  s.amountBRLString(),
			"error":       errMsg,
		},
	})
}

func (dw *DCAWorker) dcaPairExecutable(s dcaStrategy) bool {
	if dw == nil || dw.cfg == nil {
		return false
	}
	pair, ok := resolveLiquidityPair(dw.cfg, s.TokenSymbol, s.Network)
	if !ok {
		return false
	}
	if dw.cfg.LiquidityRouterEnabled {
		return true
	}
	return strings.EqualFold(pair.Asset, "USDT") && strings.EqualFold(pair.Network, "BSC")
}

// createPaidBuyOrder creates a buy_order with status 'pago_fiat' and payment_method
// 'dca_internal'. It does NOT update total_invested/total_tokens — that happens
// only in confirmDCAExecution when buy.sent is received.
func (dw *DCAWorker) createPaidBuyOrder(ctx context.Context, s dcaStrategy, destAddress string, quote dcaQuoteSnapshot) (*database.BuyOrder, error) {
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	orderID := s.ExecutionID
	if orderID == "" {
		return nil, fmt.Errorf("execution_id obrigatorio para buy order DCA")
	}
	buy, err := dw.db.CreateBuyOrder(ctx, database.BuyOrderInput{
		ID:                orderID,
		Status:            "pago_fiat",
		AmountBRL:         s.amountBRLBoundaryFloat(),
		AmountFiat:        s.amountBRLBoundaryFloat(),
		FiatCurrency:      "BRL",
		PaymentMethod:     "dca_internal",
		ProviderPaymentID: dcaOperationID(s.ID, s.ScheduledAt),
		RequestID:         dcaOperationID(s.ID, s.ScheduledAt),
		FeeBRL:            quote.FeeBRL.Float64(),
		PayoutBRL:         quote.PayoutBRL.Float64(),
		CryptoAmount:      quote.boundaryCrypto,
		Asset:             strings.ToUpper(strings.TrimSpace(s.TokenSymbol)),
		Network:           strings.ToUpper(strings.TrimSpace(s.Network)),
		DestAddress:       strings.TrimSpace(destAddress),
		RateLocked:        quote.boundaryRate,
		RateLockExpiresAt: expiresAt,
		PixPayload: map[string]any{
			"provider":            "dca_internal",
			"source":              "dca",
			"strategy_id":         s.ID,
			"execution_id":        s.ExecutionID,
			"scheduled_at":        s.ScheduledAt.UTC().Format(time.RFC3339Nano),
			"user_id":             s.UserID,
			"amount_brl_exact":    quote.AmountBRL.String(),
			"fee_brl_exact":       quote.FeeBRL.String(),
			"payout_brl_exact":    quote.PayoutBRL.String(),
			"rate_brl_exact":      quote.RateBRL,
			"crypto_amount_exact": quote.CryptoAmount,
			"required_usdt_micro": quote.RequiredUSDT,
		},
	})
	if err != nil {
		existing, getErr := dw.db.GetBuyOrder(ctx, orderID)
		if getErr == nil && existing != nil && existing.PaymentMethod == "dca_internal" {
			return existing, nil
		}
		return nil, err
	}
	if _, err := dw.db.SQL.ExecContext(ctx,
		"UPDATE buy_orders SET user_id=$1::uuid WHERE id=$2::uuid", s.UserID, buy.ID); err != nil {
		return nil, fmt.Errorf("vincular usuario na buy order DCA: %w", err)
	}
	_ = dw.db.AddBuyEvent(ctx, buy.ID, "dca.buy.created", map[string]any{
		"strategy_id": s.ID,
		"user_id":     s.UserID,
		"asset":       s.TokenSymbol,
		"network":     s.Network,
		"rate_brl":    quote.RateBRL,
		"fee_brl":     quote.FeeBRL.String(),
	})
	return buy, nil
}

func (dw *DCAWorker) dcaFeeAndPayout(amountBRL money.MoneyMinor) (feeBRL, payoutBRL money.MoneyMinor) {
	if amountBRL <= 0 {
		return 0, 0
	}
	bps := 0
	if dw != nil && dw.cfg != nil && dw.cfg.BuyRateSpreadBps > 0 {
		bps = dw.cfg.BuyRateSpreadBps
	}
	feeBRL = money.FeeBps(amountBRL, bps)
	payoutBRL = amountBRL - feeBRL
	return feeBRL, payoutBRL
}

func (dw *DCAWorker) dcaBuyRate(asset string) float64 {
	if dw == nil || dw.cfg == nil || dw.prices == nil {
		return 0
	}
	asset = strings.ToUpper(strings.TrimSpace(asset))
	usdtBRL := dw.prices.GetPrice("BRL")
	if asset == "USDT" {
		return dcaAddBps(usdtBRL, dw.cfg.BuyRateSpreadBps)
	}
	source := asset + "USDT_SOURCE"
	usd := dw.prices.GetPrice(source)
	if usd <= 0 {
		usd = dw.prices.GetPrice(asset + "USDT")
	}
	if usd <= 0 || usdtBRL <= 0 {
		return 0
	}
	return dcaAddBps(usd*usdtBRL, dw.cfg.BuyRateSpreadBps)
}

func dcaAddBps(value float64, bps int) float64 {
	if value <= 0 {
		return 0
	}
	if bps < 0 {
		bps = 0
	}
	return value * (1 + float64(bps)/10000)
}

func (dw *DCAWorker) userWalletAddress(ctx context.Context, userID, network string) (string, error) {
	network = liquidity.NormalizeNetwork(network)
	if network == "BITCOIN" {
		var address sql.NullString
		err := dw.db.SQL.QueryRowContext(ctx, `
			SELECT address
			FROM btc_wallet_addresses
			WHERE user_id=$1 AND status='active'
			ORDER BY created_at DESC
			LIMIT 1`, userID).Scan(&address)
		if err == sql.ErrNoRows {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(address.String), nil
	}
	if network == "SOLANA" || network == "APTOS" {
		var table string
		if network == "SOLANA" {
			table = "sol_wallet_addresses"
		} else {
			table = "aptos_wallet_addresses"
		}
		var address sql.NullString
		err := dw.db.SQL.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT address
			FROM %s
			WHERE user_id=$1 AND status='active'
			ORDER BY created_at DESC
			LIMIT 1`, table), userID).Scan(&address)
		if err == sql.ErrNoRows {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(address.String), nil
	}
	var address sql.NullString
	err := dw.db.SQL.QueryRowContext(ctx, `
		SELECT wallet_address
		FROM users
		WHERE id=$1::uuid AND deleted_at IS NULL`, userID).Scan(&address)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(address.String), nil
}

func nextExecution(frequency string) time.Time {
	return nextExecutionFrom(frequency, time.Now())
}

func nextExecutionFrom(frequency string, from time.Time) time.Time {
	if from.IsZero() {
		from = time.Now()
	}
	from = from.UTC()
	switch frequency {
	case "weekly":
		return from.Add(7 * 24 * time.Hour)
	case "monthly":
		return from.AddDate(0, 1, 0)
	default: // daily
		return from.Add(24 * time.Hour)
	}
}
