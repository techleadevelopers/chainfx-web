package workers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
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
	swapCompleted := w.bus.Subscribe("swap.completed")
	m2mSettled := w.bus.Subscribe("m2m.settlement.done")
	nfcCaptured := w.bus.Subscribe("nfc.capture.completed")
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
		case ev, ok := <-swapCompleted:
			if !ok {
				return
			}
			go w.sendSwapReceipt(ev)
		case ev, ok := <-m2mSettled:
			if !ok {
				return
			}
			go w.sendM2MReceipt(ev)
		case ev, ok := <-nfcCaptured:
			if !ok {
				return
			}
			go w.sendNFCReceipt(ev)
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
		Network:      buy.Network,
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

func (w *EmailWorker) sendSwapReceipt(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	swapID := firstPayloadString(ev.Payload, "swap_id", ev.OrderID)
	if swapID == "" {
		return
	}
	var to, fromAsset, toAsset, txHash string
	var fromAmount, toAmount, rate float64
	var completedAt time.Time
	err := w.db.SQL.QueryRowContext(ctx, `
SELECT u.email, s.from_asset, s.to_asset, s.from_amount, COALESCE(s.to_amount,0), COALESCE(s.rate,0), COALESCE(s.tx_hash,''), s.updated_at
FROM swaps s
JOIN users u ON u.id=s.user_id
WHERE s.id=$1`, swapID).Scan(&to, &fromAsset, &toAsset, &fromAmount, &toAmount, &rate, &txHash, &completedAt)
	if err != nil || strings.TrimSpace(to) == "" {
		slog.Info("EmailWorker: swap sem email para recibo", "swap_id", swapID, "error", err)
		return
	}
	if err := w.mailer.SendTransaction(to, "Swap concluido na ChainFX", email.TransactionReceipt{
		Title: "Swap concluido",
		Intro: "Sua troca de cripto foi concluida com sucesso.",
		CTA:   "Ver swap",
		Details: []email.TransactionDetail{
			{Label: "Enviado", Value: fmt.Sprintf("%.8f %s", fromAmount, fromAsset)},
			{Label: "Recebido", Value: fmt.Sprintf("%.8f %s", toAmount, toAsset)},
			{Label: "Cotacao", Value: fmt.Sprintf("%.8f", rate)},
			{Label: "Hash/ID", Value: fallbackTx(txHash), CopyHint: true},
			{Label: "Ordem", Value: swapID, CopyHint: true},
			{Label: "Concluido em", Value: completedAt.Format("02/01/2006 15:04 MST")},
		},
	}); err != nil {
		slog.Warn("EmailWorker: falha ao enviar recibo SWAP", "swap_id", swapID, "error", err)
	}
}

func (w *EmailWorker) sendM2MReceipt(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	intentID := firstPayloadString(ev.Payload, "intent_id", ev.OrderID)
	intent, err := w.db.GetAgentPaymentIntent(ctx, intentID)
	if err != nil || intent == nil {
		slog.Warn("EmailWorker: pagamento QR nao encontrado para recibo", "intent_id", intentID, "error", err)
		return
	}
	to, err := w.emailByWallet(ctx, intent.AgentWallet)
	if err != nil || to == "" {
		slog.Info("EmailWorker: pagamento QR sem email para recibo", "intent_id", intentID, "error", err)
		return
	}
	txHash := payloadString(ev.Payload, "tx_id")
	if txHash == "" && intent.EfiEndToEndID != nil {
		txHash = *intent.EfiEndToEndID
	}
	kind := "Pagamento QR Code"
	if intent.PaymentType == database.M2MTypeCreditCard {
		kind = "Pagamento de boleto/cartao"
	}
	when := time.Now()
	if intent.SettledAt != nil {
		when = *intent.SettledAt
	}
	if err := w.mailer.SendTransaction(to, "Pagamento concluido na ChainFX", email.TransactionReceipt{
		Title: "Pagamento concluido",
		Intro: "Seu pagamento foi confirmado e processado pela ChainFX.",
		CTA:   "Ver pagamento",
		Details: []email.TransactionDetail{
			{Label: "Tipo", Value: kind},
			{Label: "Valor", Value: moneyBRLText(intent.AmountBRL)},
			{Label: "USDT debitado", Value: fmt.Sprintf("%.6f USDT", intent.RequiredUSDT)},
			{Label: "Taxa", Value: fmt.Sprintf("%.6f USDT", intent.FeeUSDT)},
			{Label: "Destino", Value: firstNonEmptyEmail(intent.BeneficiaryName, intent.PixKey, intent.PaymentLink, intent.Barcode)},
			{Label: "Hash/ID", Value: fallbackTx(txHash), CopyHint: true},
			{Label: "Ordem", Value: intent.ID, CopyHint: true},
			{Label: "Concluido em", Value: when.Format("02/01/2006 15:04 MST")},
		},
	}); err != nil {
		slog.Warn("EmailWorker: falha ao enviar recibo QR/M2M", "intent_id", intentID, "error", err)
	}
}

func (w *EmailWorker) sendNFCReceipt(ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	authID := firstPayloadString(ev.Payload, "authorization_id", ev.OrderID)
	auth, err := w.db.GetNFCAuthorization(ctx, authID)
	if err != nil || auth == nil {
		slog.Warn("EmailWorker: pagamento NFC nao encontrado para recibo", "authorization_id", authID, "error", err)
		return
	}
	to, err := w.emailByWallet(ctx, auth.Wallet)
	if err != nil || to == "" {
		slog.Info("EmailWorker: pagamento NFC sem email para recibo", "authorization_id", authID, "error", err)
		return
	}
	if err := w.mailer.SendTransaction(to, "Pagamento NFC concluido na ChainFX", email.TransactionReceipt{
		Title: "Pagamento NFC concluido",
		Intro: "Seu pagamento por aproximacao foi capturado com sucesso.",
		CTA:   "Ver pagamento",
		Details: []email.TransactionDetail{
			{Label: "Valor", Value: moneyBRLMinorText(auth.AmountBRLMinor)},
			{Label: "Taxa", Value: moneyBRLMinorText(auth.FeeBRLMinor)},
			{Label: "Total", Value: moneyBRLMinorText(auth.TotalBRLMinor)},
			{Label: "USDT debitado", Value: fmt.Sprintf("%.6f USDT", float64(auth.RequiredUSDTMic)/1_000_000)},
			{Label: "Estabelecimento", Value: auth.MerchantID},
			{Label: "Terminal", Value: auth.TerminalID, CopyHint: true},
			{Label: "Autorizacao", Value: auth.ID, CopyHint: true},
			{Label: "Concluido em", Value: auth.UpdatedAt.Format("02/01/2006 15:04 MST")},
		},
	}); err != nil {
		slog.Warn("EmailWorker: falha ao enviar recibo NFC", "authorization_id", authID, "error", err)
	}
}

func (w *EmailWorker) emailByWallet(ctx context.Context, wallet string) (string, error) {
	var to string
	err := w.db.SQL.QueryRowContext(ctx, `
SELECT email
FROM users
WHERE lower(wallet_address)=lower($1) AND deleted_at IS NULL
LIMIT 1`, strings.TrimSpace(wallet)).Scan(&to)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return strings.TrimSpace(to), err
}

func firstPayloadString(payload map[string]interface{}, key, fallback string) string {
	value := payloadString(payload, key)
	if value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func fallbackTx(value string) string {
	if strings.TrimSpace(value) == "" {
		return "processado"
	}
	return strings.TrimSpace(value)
}

func moneyBRLText(value float64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("R$ %.2f", value)
}

func moneyBRLMinorText(value int64) string {
	if value <= 0 {
		return "-"
	}
	return fmt.Sprintf("R$ %.2f", float64(value)/100)
}

func firstNonEmptyEmail(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "processado"
}
