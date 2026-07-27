package workers

import (
	"context"
	"log/slog"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

type NFCExpirationWorker struct {
	bus *EventBus
	db  *database.DB
	cfg *config.Config
}

func NewNFCExpirationWorker(bus *EventBus, db *database.DB, cfg *config.Config) *NFCExpirationWorker {
	return &NFCExpirationWorker{bus: bus, db: db, cfg: cfg}
}

func (w *NFCExpirationWorker) Start(ctx context.Context) {
	if w == nil || w.db == nil || w.cfg == nil || !w.cfg.NFCEnabled {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	slog.Info("NFCExpirationWorker iniciado")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.expire(ctx)
		}
	}
}

func (w *NFCExpirationWorker) expire(ctx context.Context) {
	expired, err := w.db.ExpireNFCHolds(ctx, 100)
	if err != nil {
		slog.Error("NFCExpirationWorker: falha ao expirar holds", "error", err)
		return
	}
	for _, auth := range expired {
		if w.bus != nil {
			w.bus.Publish(Event{
				Type:    "nfc.authorization.expired",
				OrderID: auth.ID,
				Payload: map[string]any{
					"authorization_id":    auth.ID,
					"wallet_address":      auth.Wallet,
					"network":             auth.Network,
					"merchant_id":         auth.MerchantID,
					"terminal_id":         auth.TerminalID,
					"amount_brl_minor":    auth.AmountBRLMinor,
					"required_usdt_micro": auth.RequiredUSDTMic,
				},
			})
		}
	}
	if len(expired) > 0 {
		slog.Warn("NFCExpirationWorker: holds expirados revertidos", "count", len(expired))
	}
}
