package workers

// pushnotif.go — Phase 5: FCM / APNS push notification worker.
//
// PushNotifWorker subscribes to the event bus and fires push notifications
// via FCM (Firebase Cloud Messaging) for the events listed in
// models.Phase5WebhookEvents that are also notification-worthy.
//
// FCM HTTP v1 API is used. APNS is routed through FCM (using the
// firebase-admin flow) so a single FCM_SERVER_KEY covers both platforms.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
)

// pushEvents are the bus event types that trigger push notifications.
var pushEvents = []string{
	"order.completed",
	"payment.received",
	"payout.sent",
	"swap.completed",
	"swap.failed",
	"dca.executed",
	"kyc.approved",
	"kyc.rejected",
	"price.change",
}

// pushTemplate maps event type to a (title, body) pair.
var pushTemplate = map[string]struct{ Title, Body string }{
	"order.completed":  {"✅ Ordem concluída", "Sua ordem foi processada com sucesso."},
	"payment.received": {"💰 Pagamento recebido", "PIX confirmado. Sua cripto está sendo enviada."},
	"payout.sent":      {"📤 PIX enviado", "Seu saque PIX foi enviado."},
	"swap.completed":   {"🔄 Swap concluído", "Troca de cripto realizada com sucesso."},
	"swap.failed":      {"❌ Swap falhou", "Houve um problema com sua troca. Tente novamente."},
	"dca.executed":     {"📈 DCA executado", "Compra automática realizada."},
	"kyc.approved":     {"🎉 KYC aprovado", "Seus limites foram aumentados."},
	"kyc.rejected":     {"⚠️ KYC rejeitado", "Sua verificação foi rejeitada. Veja os detalhes no app."},
	"price.change":     {"📊 Variação de preço", "O mercado teve uma variação significativa."},
}

// PushNotifWorker sends FCM/APNS push notifications on gateway events.
type PushNotifWorker struct {
	bus        *EventBus
	db         *database.DB
	fcmKey     string
	httpClient *http.Client
}

func NewPushNotifWorker(bus *EventBus, db *database.DB) *PushNotifWorker {
	return &PushNotifWorker{
		bus:        bus,
		db:         db,
		fcmKey:     os.Getenv("FCM_SERVER_KEY"),
		httpClient: httpclient.Default(),
	}
}

func (w *PushNotifWorker) Start(ctx context.Context) {
	slog.Info("PushNotifWorker iniciado", "fcm_configured", w.fcmKey != "")

	// Subscribe to all push-worthy events
	channels := make([]chan Event, 0, len(pushEvents))
	for _, evType := range pushEvents {
		ch := w.bus.Subscribe(evType)
		channels = append(channels, ch)
		defer w.bus.Unsubscribe(evType, ch)
	}

	// Fan-in all channels into a single merged channel
	merged := mergeEventChannels(ctx, channels...)

	for {
		select {
		case <-ctx.Done():
			slog.Info("PushNotifWorker encerrado")
			return
		case ev, ok := <-merged:
			if !ok {
				return
			}
			w.handleEvent(ctx, ev)
		}
	}
}

func (w *PushNotifWorker) handleEvent(ctx context.Context, ev Event) {
	tmpl, ok := pushTemplate[ev.Type]
	if !ok {
		return
	}

	userID, _ := ev.Payload["user_id"].(string)
	if userID == "" {
		return
	}

	// Persist in-app notification
	body := tmpl.Body
	_ = w.createNotification(ctx, userID, tmpl.Title, body, ev.Type, ev.Payload)

	// Fire FCM push (no-op if key not configured)
	if w.fcmKey == "" {
		slog.Debug("PushNotifWorker: FCM_SERVER_KEY não configurado, notificação in-app salva apenas",
			"event", ev.Type, "user", userID)
		return
	}

	tokens, err := w.getDeviceTokens(ctx, userID)
	if err != nil || len(tokens) == 0 {
		return
	}

	for _, token := range tokens {
		if err := w.sendFCM(ctx, token, tmpl.Title, body, ev.Type); err != nil {
			slog.Warn("PushNotifWorker: erro ao enviar FCM", "token_prefix", safePrefix(token), "error", err)
		}
	}
}

func (w *PushNotifWorker) sendFCM(ctx context.Context, token, title, body, eventType string) error {
	payload := map[string]any{
		"to": token,
		"notification": map[string]string{
			"title": title,
			"body":  body,
		},
		"data": map[string]string{
			"event_type": eventType,
		},
		"priority": "high",
	}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://fcm.googleapis.com/fcm/send", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "key="+w.fcmKey)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("FCM retornou %d", resp.StatusCode)
	}
	return nil
}

func (w *PushNotifWorker) createNotification(ctx context.Context, userID, title, body, ntype string, data map[string]any) error {
	var dataStr *string
	if data != nil {
		b, _ := json.Marshal(data)
		s := string(b)
		dataStr = &s
	}
	_, err := w.db.SQL.ExecContext(ctx, `
		INSERT INTO notifications (user_id, title, body, type, data)
		VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5)`,
		userID, title, body, ntype, dataStr)
	return err
}

func (w *PushNotifWorker) getDeviceTokens(ctx context.Context, userID string) ([]string, error) {
	rows, err := w.db.SQL.QueryContext(ctx, `
		SELECT COALESCE(fcm_token, apns_token)
		FROM devices
		WHERE user_id=$1 AND (fcm_token IS NOT NULL OR apns_token IS NOT NULL)
		  AND last_active > NOW() - INTERVAL '30 days'`,
		userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil && t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens, rows.Err()
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// mergeEventChannels fans-in multiple event channels into one.
func mergeEventChannels(ctx context.Context, channels ...chan Event) chan Event {
	merged := make(chan Event, 200)
	for _, ch := range channels {
		go func(c chan Event) {
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-c:
					if !ok {
						return
					}
					select {
					case merged <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}(ch)
	}
	return merged
}

func safePrefix(s string) string {
	if len(s) > 8 {
		return s[:8] + "..."
	}
	return s
}
