package workers

import (
	"context"
	"log/slog"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
)

type EmailWorker struct {
	bus    *EventBus
	db     *database.DB
	mailer *email.Service
}

func NewEmailWorker(bus *EventBus, db *database.DB, mailer *email.Service) *EmailWorker {
	return &EmailWorker{bus: bus, db: db, mailer: mailer}
}

func (w *EmailWorker) Start(ctx context.Context) {
	buySent := w.bus.Subscribe("buy.sent")
	payoutSettled := w.bus.Subscribe("payout.settled")
	slog.Info("EmailWorker escutando eventos transacionais")
	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando EmailWorker")
			return
		case ev, ok := <-buySent:
			if !ok {
				return
			}
			go w.sendBuyReceipt(ev)
		case ev, ok := <-payoutSettled:
			if !ok {
				return
			}
			go w.sendSellReceipt(ev)
		}
	}
}

func (w *EmailWorker) sendBuyReceipt(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	buy, err := w.db.GetBuyOrder(ctx, ev.OrderID)
	if err != nil || buy == nil {
		slog.Warn("EmailWorker: compra nao encontrada para recibo", "order_id", ev.OrderID, "error", err)
		return
	}
	to, err := w.db.GetBuyOrderEmail(ctx, ev.OrderID)
	if err != nil || to == "" {
		slog.Info("EmailWorker: compra sem email para recibo", "order_id", ev.OrderID, "error", err)
		return
	}
	txHash := payloadString(ev.Payload, "txHash")
	if txHash == "" && buy.TxHashOut != nil {
		txHash = *buy.TxHashOut
	}
	when := time.Now()
	if buy.DeliveredAt != nil {
		when = *buy.DeliveredAt
	}
	if err := w.mailer.SendBuyCompleted(to, email.Receipt{
		OrderID:      buy.ID,
		Asset:        buy.Asset,
		Network:      "BSC",
		AmountFiat:   buy.AmountFiat,
		FeeFiat:      buy.FeeBRL,
		PayoutFiat:   buy.PayoutBRL,
		CryptoAmount: buy.CryptoAmount,
		Rate:         buy.RateLocked,
		Wallet:       buy.DestAddress,
		TxHash:       txHash,
		CompletedAt:  when,
	}); err != nil {
		slog.Warn("EmailWorker: falha ao enviar recibo BUY", "order_id", ev.OrderID, "error", err)
	}
}

func (w *EmailWorker) sendSellReceipt(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	order, err := w.db.GetOrder(ctx, ev.OrderID)
	if err != nil || order == nil {
		slog.Warn("EmailWorker: venda nao encontrada para recibo", "order_id", ev.OrderID, "error", err)
		return
	}
	to, err := w.db.GetOrderEmail(ctx, ev.OrderID)
	if err != nil || to == "" {
		slog.Info("EmailWorker: venda sem email para recibo", "order_id", ev.OrderID, "error", err)
		return
	}
	txHash := payloadString(ev.Payload, "tx_hash_pix")
	if txHash == "" {
		txHash = payloadString(ev.Payload, "txHash")
	}
	if txHash == "" && order.TxHash != nil {
		txHash = *order.TxHash
	}
	if err := w.mailer.SendSellCompleted(to, email.Receipt{
		OrderID:      order.ID,
		Asset:        order.Asset,
		Network:      order.Network,
		AmountFiat:   order.PayoutBRL,
		FeeFiat:      order.FeeBRL,
		PayoutFiat:   order.PayoutBRL,
		CryptoAmount: order.AmountUSDT,
		Rate:         order.RateLocked,
		Wallet:       order.Address,
		TxHash:       txHash,
		CompletedAt:  time.Now(),
	}); err != nil {
		slog.Warn("EmailWorker: falha ao enviar recibo SELL", "order_id", ev.OrderID, "error", err)
	}
}

func payloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}
