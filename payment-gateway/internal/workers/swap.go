package workers

// swap.go — Phase 5: Swap execution worker.
//
// SwapWorker listens for swap.created events and executes the crypto-to-crypto
// exchange. PancakeSwap V2 executes real BSC ERC20 swaps when
// MOBILE_SWAP_PANCAKE_ENABLED=true; otherwise mobile swap is unavailable.

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
		FROM swaps WHERE id=$1 AND status IN ('pending','execution_requested')`, id).Scan(
		&id, &userID, &fromAsset, &toAsset, &fromAmount, &feeBPS, &slippage)
	if err == sql.ErrNoRows {
		return // already picked up or completed
	}
	if err != nil {
		slog.Error("SwapWorker: erro ao buscar swap", "id", id, "error", err)
		return
	}
	if w.cfg == nil || !w.cfg.MobileSwapPancakeEnabled {
		w.markSwapRouteUnavailable(ctx, id)
		return
	}

	if w.cfg != nil && w.cfg.MobileSwapPancakeEnabled {
		if _, err := w.db.SQL.ExecContext(ctx,
			"UPDATE swaps SET status='signing', updated_at=NOW() WHERE id=$1 AND status='execution_requested'", id); err != nil {
			slog.Error("SwapWorker: erro ao marcar signing", "id", id, "error", err)
			return
		}
		result, err := w.executePancakeSwap(ctx, id, userID, fromAsset, toAsset, fromAmount, slippage, feeBPS)
		if err != nil {
			w.failSwap(ctx, id, "PancakeSwap: "+err.Error())
			return
		}
		if _, err := w.db.SQL.ExecContext(ctx, `
			UPDATE swaps SET status='completed', to_amount=$1, rate=$2,
			                 tx_hash=$3, confirmed_at=NOW(), updated_at=NOW()
			WHERE id=$4`, result.ToAmount, result.Rate, result.TxHash, id); err != nil {
			slog.Error("SwapWorker: erro ao completar swap Pancake", "id", id, "error", err)
			return
		}
		slog.Info("SwapWorker: swap Pancake concluido",
			"id", id, "from", fromAsset, "to", toAsset,
			"from_amount", fromAmount, "to_amount", result.ToAmount, "rate", result.Rate, "tx_hash", result.TxHash)
		w.bus.Publish(Event{
			Type: "swap.completed",
			Payload: map[string]any{
				"swap_id":     id,
				"user_id":     userID,
				"from_asset":  fromAsset,
				"to_asset":    toAsset,
				"from_amount": fromAmount,
				"to_amount":   result.ToAmount,
				"rate":        result.Rate,
				"tx_hash":     result.TxHash,
				"provider":    "pancakeswap_v2",
				"network":     "BSC",
			},
		})
		return
	}

	w.markSwapRouteUnavailable(ctx, id)

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

func (w *SwapWorker) markSwapRouteUnavailable(ctx context.Context, id string) {
	if _, err := w.db.SQL.ExecContext(ctx, `
		UPDATE swaps SET status='route_unavailable', error='ROUTE_UNAVAILABLE', updated_at=NOW()
		WHERE id=$1 AND status IN ('pending','execution_requested')`, id); err != nil {
		slog.Error("SwapWorker: erro ao marcar route_unavailable", "id", id, "error", err)
		return
	}
	slog.Warn("SwapWorker: executor real de swap indisponivel", "id", id)
}

func (w *SwapWorker) retryPending(ctx context.Context) {
	rows, err := w.db.SQL.QueryContext(ctx, `
		SELECT id FROM swaps WHERE status IN ('pending','execution_requested') AND created_at < NOW() - INTERVAL '1 minute'
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
