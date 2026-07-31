package workers

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

type BTCSellFundingWorker struct {
	bus *EventBus
	db  *database.DB
	cfg *config.Config
}

func NewBTCSellFundingWorker(bus *EventBus, db *database.DB, cfg *config.Config) *BTCSellFundingWorker {
	return &BTCSellFundingWorker{bus: bus, db: db, cfg: cfg}
}

func (w *BTCSellFundingWorker) Start(ctx context.Context) {
	detected := w.bus.Subscribe("btc.deposit.detected")
	confirmed := w.bus.Subscribe("btc.deposit.confirmed")
	slog.Info("BTCSellFundingWorker escutando eventos BTC nativos")
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-detected:
			if !ok {
				return
			}
			w.process(ctx, ev)
		case ev, ok := <-confirmed:
			if !ok {
				return
			}
			w.process(ctx, ev)
		}
	}
}

func (w *BTCSellFundingWorker) process(ctx context.Context, ev Event) {
	p := ev.Payload
	network := stringValueFromAny(p["network"])
	userID := stringValueFromAny(p["user_id"])
	address := stringValueFromAny(p["address"])
	txid := stringValueFromAny(p["txid"])
	vout, _ := uint32FromAny(p["vout"])
	amountSats, _ := int64FromAny(p["amount_sats"])
	confirmations, _ := intFromAny(p["confirmations"])
	minConfirmations := btcMinConfirmationsEnv()
	if strings.TrimSpace(network) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(address) == "" || strings.TrimSpace(txid) == "" || amountSats <= 0 {
		return
	}
	match, err := w.db.ApplyBTCSellFundingEvent(ctx, network, userID, address, txid, vout, amountSats, confirmations, minConfirmations)
	if err != nil {
		slog.Error("BTCSellFundingWorker: erro ao aplicar funding BTC", "txid", txid, "vout", vout, "err", err)
		return
	}
	if match == nil || !match.Ready {
		return
	}
	depositAmount := float64(match.ReceivedSats) / 100000000
	evidence := map[string]any{
		"asset":            "BTC",
		"network":          "BITCOIN",
		"btcNetwork":       network,
		"txid":             txid,
		"vout":             vout,
		"depositTx":        match.DepositKey,
		"amountSats":       match.ReceivedSats,
		"expectedSats":     match.ExpectedSats,
		"depositAmount":    depositAmount,
		"confirmations":    confirmations,
		"minConfirmations": minConfirmations,
		"address":          address,
	}
	accepted, err := w.db.ConfirmSellDepositForPayout(ctx, match.OrderID, match.DepositKey, depositAmount, evidence)
	if err != nil {
		slog.Error("BTCSellFundingWorker: erro ao confirmar SELL BTC", "order_id", match.OrderID, "err", err)
		return
	}
	if !accepted {
		return
	}
	w.bus.Publish(Event{
		Type:    "payout.requested",
		OrderID: match.OrderID,
		Payload: evidence,
	})
}

func stringValueFromAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

func btcMinConfirmationsEnv() int {
	if v := strings.TrimSpace(os.Getenv("BTC_MIN_CONFIRMATIONS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}

func intFromAny(v any) (int, bool) {
	n, ok := int64FromAny(v)
	return int(n), ok
}

func uint32FromAny(v any) (uint32, bool) {
	n, ok := int64FromAny(v)
	return uint32(n), ok
}

func int64FromAny(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case uint32:
		return int64(t), true
	case uint64:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}
