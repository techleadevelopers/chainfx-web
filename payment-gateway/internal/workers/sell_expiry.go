package workers

import (
	"context"
	"log/slog"
	"time"

	"payment-gateway/internal/database"
)

const sellExpiryInterval = 30 * time.Second

type SellExpiryWorker struct {
	db *database.DB
}

func NewSellExpiryWorker(db *database.DB) *SellExpiryWorker {
	return &SellExpiryWorker{db: db}
}

func (w *SellExpiryWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(sellExpiryInterval)
	defer ticker.Stop()

	w.expire(ctx)
	for {
		select {
		case <-ctx.Done():
			slog.Info("SellExpiryWorker encerrado")
			return
		case <-ticker.C:
			w.expire(ctx)
		}
	}
}

func (w *SellExpiryWorker) expire(ctx context.Context) {
	if w == nil || w.db == nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	sellCount, err := w.db.ExpireStaleSellOrders(runCtx)
	if err != nil {
		slog.Warn("SellExpiryWorker: erro ao expirar ordens sell", "err", err)
	}
	if sellCount > 0 {
		slog.Info("SellExpiryWorker: ordens sell expiradas", "count", sellCount, "ttl", "rate_lock_expires_at")
	}

	buyCount, err := w.db.ExpireStaleBuyOrders(runCtx)
	if err != nil {
		slog.Warn("SellExpiryWorker: erro ao expirar ordens buy", "err", err)
	}
	if buyCount > 0 {
		slog.Info("SellExpiryWorker: ordens buy expiradas", "count", buyCount, "ttl", "rate_lock_expires_at")
	}
}
