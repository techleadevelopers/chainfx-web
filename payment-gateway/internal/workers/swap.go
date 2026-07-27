package workers

// swap.go — Phase 5: Swap execution worker.
//
// SwapWorker listens for swap.created events and executes the crypto-to-crypto
// exchange. In the current implementation swaps are simulated at the live
// on-chain price (BRL bridge). A production integration would call a DEX
// aggregator (1inch, Paraswap) or an internal liquidity pool.

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

// SwapWorker processes pending swap orders.
type SwapWorker struct {
	bus *EventBus
	db  *database.DB
	cfg *config.Config
	pw  *PriceWorker
}

func NewSwapWorker(bus *EventBus, db *database.DB, cfg *config.Config, pw *PriceWorker) *SwapWorker {
	return &SwapWorker{bus: bus, db: db, cfg: cfg, pw: pw}
}

func (w *SwapWorker) Start(ctx context.Context) {
	slog.Info("SwapWorker iniciado")

	swapCh := w.bus.Subscribe("swap.created")
	defer w.bus.Unsubscribe("swap.created", swapCh)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("SwapWorker encerrado")
			return
		case ev, ok := <-swapCh:
			if !ok {
				return
			}
			if id, ok := ev.Payload["swap_id"].(string); ok {
				w.executeSwap(ctx, id)
			}
		case <-ticker.C:
			w.retryPending(ctx)
		}
	}
}

func (w *SwapWorker) executeSwap(ctx context.Context, id string) {
	// Lock row
	var fromAsset, toAsset, userID string
	var fromAmount, slippage float64
	var feeBPS int
	err := w.db.SQL.QueryRowContext(ctx, `
		SELECT id, user_id, from_asset, to_asset, from_amount, fee_bps, slippage_tolerance
		FROM swaps WHERE id=$1 AND status='pending'`, id).Scan(
		&id, &userID, &fromAsset, &toAsset, &fromAmount, &feeBPS, &slippage)
	if err == sql.ErrNoRows {
		return // already picked up or completed
	}
	if err != nil {
		slog.Error("SwapWorker: erro ao buscar swap", "id", id, "error", err)
		return
	}

	// Mark executing
	if _, err := w.db.SQL.ExecContext(ctx,
		"UPDATE swaps SET status='executing', updated_at=NOW() WHERE id=$1", id); err != nil {
		slog.Error("SwapWorker: erro ao marcar executing", "id", id, "error", err)
		return
	}

	// Calculate rate using live prices
	fromBRL := priceInBRL(w.pw, fromAsset)
	toBRL := priceInBRL(w.pw, toAsset)

	if fromBRL <= 0 || toBRL <= 0 {
		errMsg := "cotação indisponível para " + fromAsset + "/" + toAsset
		w.failSwap(ctx, id, errMsg)
		return
	}

	rate := fromBRL / toBRL
	fee := fromAmount * float64(feeBPS) / 10_000
	netFrom := fromAmount - fee
	toAmount := netFrom * rate
	minReceived := toAmount * (1 - slippage)

	if toAmount < minReceived {
		w.failSwap(ctx, id, "slippage excedido")
		return
	}

	// Simulate on-chain execution (in prod: call DEX/aggregator here)
	time.Sleep(200 * time.Millisecond)
	fakeTxHash := "0xswap_" + id[:8]

	if _, err := w.db.SQL.ExecContext(ctx, `
		UPDATE swaps SET status='completed', to_amount=$1, rate=$2,
		                 tx_hash=$3, updated_at=NOW()
		WHERE id=$4`, toAmount, rate, fakeTxHash, id); err != nil {
		slog.Error("SwapWorker: erro ao completar swap", "id", id, "error", err)
		return
	}

	slog.Info("SwapWorker: swap concluído",
		"id", id, "from", fromAsset, "to", toAsset,
		"from_amount", fromAmount, "to_amount", toAmount, "rate", rate)

	w.bus.Publish(Event{
		Type: "swap.completed",
		Payload: map[string]any{
			"swap_id":     id,
			"user_id":     userID,
			"from_asset":  fromAsset,
			"to_asset":    toAsset,
			"from_amount": fromAmount,
			"to_amount":   toAmount,
			"rate":        rate,
		},
	})
}

func (w *SwapWorker) failSwap(ctx context.Context, id, reason string) {
	if _, err := w.db.SQL.ExecContext(ctx, `
		UPDATE swaps SET status='failed', error=$1, updated_at=NOW()
		WHERE id=$2`, reason, id); err != nil {
		slog.Error("SwapWorker: erro ao marcar failed", "id", id, "error", err)
	}
	slog.Warn("SwapWorker: swap falhou", "id", id, "reason", reason)

	// Fetch user_id so downstream workers (push, webhooks) can route to the right user
	var userID string
	_ = w.db.SQL.QueryRowContext(ctx, "SELECT user_id FROM swaps WHERE id=$1", id).Scan(&userID)

	w.bus.Publish(Event{
		Type:    "swap.failed",
		Payload: map[string]any{"swap_id": id, "user_id": userID, "reason": reason},
	})
}

func (w *SwapWorker) retryPending(ctx context.Context) {
	rows, err := w.db.SQL.QueryContext(ctx, `
		SELECT id FROM swaps WHERE status='pending' AND created_at < NOW() - INTERVAL '1 minute'
		LIMIT 5`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			w.executeSwap(ctx, id)
		}
	}
}

// priceInBRL returns the BRL price of an asset via the PriceWorker cache.
func priceInBRL(pw *PriceWorker, symbol string) float64 {
	if pw == nil {
		return 0
	}
	switch symbol {
	case "USDT", "USDC", "BUSD":
		return pw.GetPrice("BRL")
	case "BTCB", "BTC":
		btcUSD := pw.GetPrice("BTCUSDT_SOURCE")
		usdtBRL := pw.GetPrice("BRL")
		if btcUSD > 0 && usdtBRL > 0 {
			return btcUSD * usdtBRL
		}
	case "EURC":
		usdtEUR := pw.GetPrice("USDTEUR")
		usdtBRL := pw.GetPrice("BRL")
		if usdtEUR > 0 && usdtBRL > 0 {
			return (1 / usdtEUR) * usdtBRL
		}
	}
	return 0
}
