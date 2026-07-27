package workers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/liquidity"
)

// DCAWorker executes due Dollar-Cost-Averaging buy strategies.
// Uses SELECT FOR UPDATE SKIP LOCKED so multiple pod instances never
// process the same strategy simultaneously.
//
// Accounting contract:
//   - A dca_executions row is created with status='pending' at the start of each cycle.
//   - total_invested / total_tokens on dca_strategies are ONLY updated when the
//     downstream buy.sent event is confirmed, preventing phantom balances on failure.
//   - On any failure the execution row is marked 'failed' and dca.execution.failed is published.
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
	var amountBRL, cryptoAmount float64

	tx, err := dw.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("DCAWorker: erro ao iniciar tx confirmacao DCA",
			"buy_order_id", buyOrderID, "err", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	err = tx.QueryRowContext(ctx, `
		UPDATE dca_executions
		SET    status='completed', updated_at=NOW()
		WHERE  buy_order_id = $1::uuid
		  AND  status = 'pending'
		RETURNING id, strategy_id, amount_brl, COALESCE(crypto_amount, 0)
	`, buyOrderID).Scan(&execID, &strategyID, &amountBRL, &cryptoAmount)
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
		SET    total_invested = total_invested + $1,
		       total_tokens   = total_tokens   + $2,
		       updated_at     = NOW()
		WHERE  id = $3
	`, amountBRL, cryptoAmount, strategyID); err != nil {
		slog.Warn("DCAWorker: erro ao atualizar stats DCA apos buy.sent",
			"strategy_id", strategyID, "err", err)
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Error("DCAWorker: erro ao commitar confirmacao DCA",
			"exec_id", execID, "err", err)
		return
	}

	slog.Info("DCAWorker: execucao DCA confirmada e stats atualizados",
		"exec_id", execID, "strategy_id", strategyID,
		"amount_brl", amountBRL, "crypto_amount", cryptoAmount)
	_ = dw.db.AddBuyEvent(ctx, buyOrderID, "dca.execution.confirmed", map[string]any{
		"strategy_id":   strategyID,
		"exec_id":       execID,
		"amount_brl":    amountBRL,
		"crypto_amount": cryptoAmount,
	})
}

type dcaStrategy struct {
	ID          string
	UserID      string
	TokenSymbol string
	Network     string
	AmountBRL   float64
	Frequency   string
}

func (dw *DCAWorker) runDue(ctx context.Context) {
	// Use a transaction with SKIP LOCKED so concurrent instances/pods don't double-execute
	tx, err := dw.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("DCAWorker: erro ao iniciar transação", "err", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		SELECT id, user_id, token_symbol, network, amount_brl, frequency
		FROM   dca_strategies
		WHERE  active = true
		  AND  next_execution <= NOW()
		ORDER  BY next_execution ASC
		LIMIT  50
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		slog.Warn("DCAWorker: erro ao buscar estratégias", "err", err)
		return
	}

	var strategies []dcaStrategy
	for rows.Next() {
		var s dcaStrategy
		if err := rows.Scan(&s.ID, &s.UserID, &s.TokenSymbol, &s.Network, &s.AmountBRL, &s.Frequency); err != nil {
			slog.Warn("DCAWorker: scan error", "err", err)
			continue
		}
		strategies = append(strategies, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("DCAWorker: erro ao iterar rows", "err", err)
		return
	}
	if len(strategies) == 0 {
		return
	}

	// Pre-schedule next executions inside the transaction to prevent re-runs
	for _, s := range strategies {
		next := nextExecution(s.Frequency)
		if _, err := tx.ExecContext(ctx,
			"UPDATE dca_strategies SET next_execution=$1 WHERE id=$2", next, s.ID); err != nil {
			slog.Warn("DCAWorker: erro ao agendar próxima execução", "strategy_id", s.ID, "err", err)
		}
	}

	if err := tx.Commit(); err != nil {
		slog.Error("DCAWorker: erro ao commitar transação", "err", err)
		return
	}

	slog.Info("DCAWorker: executando estratégias", "count", len(strategies))
	for _, s := range strategies {
		go dw.execute(ctx, s)
	}
}

func (dw *DCAWorker) execute(ctx context.Context, s dcaStrategy) {
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	slog.Info("DCAWorker: executando DCA",
		"strategy_id", s.ID, "user_id", s.UserID,
		"token", s.TokenSymbol, "network", s.Network, "amount_brl", s.AmountBRL)

	if dw.cfg.AllowSimulations && !dw.cfg.IsProduction() {
		// Dev simulation: record the investment directly without creating a real buy order
		if _, err := dw.db.SQL.ExecContext(execCtx, `
			UPDATE dca_strategies
			SET    total_invested = total_invested + $1
			WHERE  id = $2`, s.AmountBRL, s.ID); err != nil {
			slog.Warn("DCAWorker: erro ao atualizar simulação", "strategy_id", s.ID, "err", err)
		} else {
			slog.Info("DCAWorker: DCA simulado concluído", "strategy_id", s.ID)
		}
		return
	}

	// --- Create a pending execution record before doing anything irreversible ---
	execID, err := dw.createPendingExecution(execCtx, s)
	if err != nil {
		slog.Warn("DCAWorker: erro ao criar execucao pendente", "strategy_id", s.ID, "err", err)
		dw.dlq.Push(Event{
			Type:    "dca.buy.requested",
			OrderID: s.ID,
			Payload: map[string]any{
				"user_id": s.UserID, "asset": s.TokenSymbol,
				"network": s.Network, "amount_brl": s.AmountBRL,
				"source": "dca", "strategy_id": s.ID,
				"error": err.Error(),
			},
		}, 1, err.Error())
		return
	}

	fail := func(reason string, origErr error) {
		errMsg := reason
		if origErr != nil {
			errMsg = origErr.Error()
		}
		slog.Warn("DCAWorker: "+reason, "strategy_id", s.ID, "err", origErr)
		dw.markExecutionFailed(ctx, execID, s, origErr)
		dw.dlq.Push(Event{
			Type:    "dca.buy.requested",
			OrderID: s.ID,
			Payload: map[string]any{
				"user_id": s.UserID, "asset": s.TokenSymbol,
				"network": s.Network, "amount_brl": s.AmountBRL,
				"source": "dca", "strategy_id": s.ID,
				"error": errMsg,
			},
		}, 1, errMsg)
	}

	destAddress, err := dw.userWalletAddress(execCtx, s.UserID, s.Network)
	if err != nil {
		fail("erro ao buscar carteira do usuario", err)
		return
	}
	if destAddress == "" {
		noWalletErr := fmt.Errorf("usuario sem carteira configurada para rede %s", s.Network)
		slog.Warn("DCAWorker: usuario sem carteira para DCA",
			"strategy_id", s.ID, "user_id", s.UserID)
		dw.markExecutionFailed(ctx, execID, s, noWalletErr)
		return
	}
	if !dw.dcaPairExecutable(s) {
		pairErr := fmt.Errorf("par DCA sem rota executavel: %s/%s", s.TokenSymbol, s.Network)
		fail("par sem rota executavel", pairErr)
		return
	}

	feeBRL, payoutBRL := dw.dcaFeeAndPayout(s.AmountBRL)
	if payoutBRL <= 0 {
		fail("valor DCA invalido apos taxa", fmt.Errorf("payout_brl %.8f invalido", payoutBRL))
		return
	}

	// --- Resolve rate: prefer a real quote from the liquidity router ---
	rate, cryptoAmount, err := dw.resolveRateAndAmount(execCtx, s, destAddress, payoutBRL)
	if err != nil {
		fail("erro ao obter cotacao para DCA", err)
		return
	}

	// Update pending execution with resolved amounts so confirmDCAExecution has accurate data
	if execID != "" {
		if _, err := dw.db.SQL.ExecContext(execCtx, `
			UPDATE dca_executions
			SET    crypto_amount=$1, rate_brl=$2, updated_at=NOW()
			WHERE  id=$3
		`, cryptoAmount, rate, execID); err != nil {
			fail("erro ao atualizar execucao DCA com cotacao", err)
			return
		}
	}

	buy, err := dw.createPaidBuyOrder(execCtx, s, destAddress, rate, feeBRL, payoutBRL, cryptoAmount)
	if err != nil {
		fail("erro ao criar buy order para DCA", err)
		return
	}

	// Link the execution row to the buy order so confirmDCAExecution can find it
	if execID != "" {
		if _, err := dw.db.SQL.ExecContext(execCtx, `
			UPDATE dca_executions SET buy_order_id=$1::uuid, updated_at=NOW() WHERE id=$2
		`, buy.ID, execID); err != nil {
			fail("erro ao vincular buy order na execucao DCA", err)
			return
		}
	}

	dw.bus.Publish(Event{
		Type:    "buy.paid",
		OrderID: buy.ID,
		Payload: map[string]any{
			"user_id": s.UserID, "asset": s.TokenSymbol, "token_symbol": s.TokenSymbol,
			"network": s.Network, "amount_brl": payoutBRL, "dest_address": destAddress,
			"source": "dca", "strategy_id": s.ID,
		},
	})
}

// resolveRateAndAmount obtains the BRL rate and crypto amount for a DCA cycle.
// When the liquidity router is enabled it attempts to fetch a real provider quote;
// on failure (or when router is disabled) it falls back to the price-cache rate
// with the configured spread.
func (dw *DCAWorker) resolveRateAndAmount(ctx context.Context, s dcaStrategy, destAddress string, payoutBRL float64) (rate, cryptoAmount float64, err error) {
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
				provisionalCrypto = payoutBRL / cacheRate
			}

			req := liquidity.Request{
				OrderID:      "dca-quote-" + s.ID,
				UserID:       s.UserID,
				Asset:        pair.Asset,
				Network:      pair.Network,
				FiatCurrency: "BRL",
				AmountBRL:    payoutBRL,
				CryptoAmount: provisionalCrypto,
				DestAddress:  destAddress,
				CreatedAt:    time.Now().UTC(),
			}

			// Collect quotes from all providers and pick the best (most crypto out).
			allQuotes := dw.router.QuoteAll(qCtx, req)
			bestCrypto := 0.0
			bestRate := 0.0
			for _, q := range allQuotes {
				if q.CryptoAmount <= 0 || q.FiatCostBRL <= 0 {
					continue
				}
				impliedRate := q.FiatCostBRL / q.CryptoAmount
				if bestCrypto == 0 || q.CryptoAmount > bestCrypto {
					bestCrypto = q.CryptoAmount
					bestRate = impliedRate
				}
			}
			if bestCrypto > 0 && bestRate > 0 {
				slog.Info("DCAWorker: cotacao real obtida do router",
					"strategy_id", s.ID, "rate_brl", bestRate, "crypto_amount", bestCrypto)
				return bestRate, bestCrypto, nil
			}
			slog.Info("DCAWorker: nenhuma cotacao do router; usando cache de precos",
				"strategy_id", s.ID)
		}
	}

	// Fallback: price-cache rate with configured spread
	cacheRate := dw.dcaBuyRate(s.TokenSymbol)
	if cacheRate <= 0 {
		return 0, 0, fmt.Errorf("cotacao indisponivel para %s", s.TokenSymbol)
	}
	ca := payoutBRL / cacheRate
	if ca <= 0 {
		return 0, 0, fmt.Errorf("amount DCA invalido apos conversao")
	}
	return cacheRate, ca, nil
}

// createPendingExecution inserts a dca_executions row with status='pending' and
// returns its UUID. DCA execution stops if this fails because later accounting
// depends on this row.
func (dw *DCAWorker) createPendingExecution(ctx context.Context, s dcaStrategy) (string, error) {
	var execID string
	err := dw.db.SQL.QueryRowContext(ctx, `
		INSERT INTO dca_executions (strategy_id, amount_brl, status)
		VALUES ($1::uuid, $2, 'pending')
		RETURNING id
	`, s.ID, s.AmountBRL).Scan(&execID)
	if err != nil {
		return "", fmt.Errorf("createPendingExecution: %w", err)
	}
	return execID, nil
}

// markExecutionFailed sets a dca_executions row to 'failed' and publishes
// the dca.execution.failed event. Safe to call with an empty execID.
func (dw *DCAWorker) markExecutionFailed(ctx context.Context, execID string, s dcaStrategy, origErr error) {
	errMsg := ""
	if origErr != nil {
		errMsg = origErr.Error()
	}
	if execID != "" {
		_, _ = dw.db.SQL.ExecContext(ctx, `
			UPDATE dca_executions
			SET    status='failed', error_message=$1, updated_at=NOW()
			WHERE  id=$2
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
			"amount_brl":  s.AmountBRL,
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
func (dw *DCAWorker) createPaidBuyOrder(ctx context.Context, s dcaStrategy, destAddress string, rate, feeBRL, payoutBRL, cryptoAmount float64) (*database.BuyOrder, error) {
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	buy, err := dw.db.CreateBuyOrder(ctx, database.BuyOrderInput{
		Status:            "pago_fiat",
		AmountBRL:         s.AmountBRL,
		AmountFiat:        s.AmountBRL,
		FiatCurrency:      "BRL",
		PaymentMethod:     "dca_internal",
		ProviderPaymentID: "dca-" + s.ID + "-" + time.Now().UTC().Format("20060102150405"),
		RequestID:         "dca-" + s.ID,
		FeeBRL:            feeBRL,
		PayoutBRL:         payoutBRL,
		CryptoAmount:      cryptoAmount,
		Asset:             strings.ToUpper(strings.TrimSpace(s.TokenSymbol)),
		Network:           strings.ToUpper(strings.TrimSpace(s.Network)),
		DestAddress:       strings.TrimSpace(destAddress),
		RateLocked:        rate,
		RateLockExpiresAt: expiresAt,
		PixPayload: map[string]any{
			"provider":    "dca_internal",
			"source":      "dca",
			"strategy_id": s.ID,
			"user_id":     s.UserID,
		},
	})
	if err != nil {
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
		"rate_brl":    rate,
		"fee_brl":     feeBRL,
	})
	return buy, nil
}

func (dw *DCAWorker) dcaFeeAndPayout(amountBRL float64) (feeBRL, payoutBRL float64) {
	if amountBRL <= 0 {
		return 0, 0
	}
	bps := 0
	if dw != nil && dw.cfg != nil && dw.cfg.BuyRateSpreadBps > 0 {
		bps = dw.cfg.BuyRateSpreadBps
	}
	feeBRL = amountBRL * (float64(bps) / 10000)
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
	switch frequency {
	case "weekly":
		return time.Now().Add(7 * 24 * time.Hour)
	case "monthly":
		return time.Now().AddDate(0, 1, 0)
	default: // daily
		return time.Now().Add(24 * time.Hour)
	}
}
