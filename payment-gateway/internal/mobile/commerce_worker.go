package mobile

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const commerceWorkerPollInterval = 5 * time.Second

type commerceOutboxEvent struct {
	ID          string
	EventType   string
	AggregateID string
	Provider    string
	Payload     string
	Attempts    int
}

type commerceOrderForProvider struct {
	ID                string
	UserEmail         string
	WalletAddress     string
	ProductID         string
	ProviderID        string
	ProviderProductID string
	ProviderSlug      string
	Brand             string
	Title             string
	Currency          string
	ProductType       string
	Quantity          int
	AmountBRLMinor    int64
	RequiredUSDTMicro int64
	Status            string
	ProviderReference string
	ProviderOrderID   string
	RedemptionCodeEnc string
	RedemptionPINEnc  string
	RedemptionURLEnc  string
	RecipientPhone    string
}

func (s *Server) startCommerceOutboxWorker(ctx context.Context) {
	if s == nil || s.db == nil || s.db.SQL == nil {
		return
	}
	if strings.EqualFold(os.Getenv("BITREFILL_RECONCILIATION_ENABLED"), "false") {
		return
	}
	ticker := time.NewTicker(commerceWorkerPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processNextCommerceOutbox(ctx)
			s.reconcilePendingCommerceOrders(ctx)
		}
	}
}

func (s *Server) processNextCommerceOutbox(ctx context.Context) {
	event, err := s.claimCommerceOutboxEvent(ctx)
	if err == sql.ErrNoRows {
		return
	}
	if err != nil {
		slog.Warn("commerce outbox claim failed", "error", err)
		return
	}
	if event.EventType != "commerce.purchase.requested" {
		_ = s.markCommerceOutboxProcessed(ctx, event.ID)
		return
	}
	if !bitrefillLivePurchasesEnabled() {
		_ = s.deferCommerceOutbox(ctx, event.ID, event.Attempts, "live_purchases_disabled")
		return
	}
	order, err := s.loadCommerceOrderForProvider(ctx, event.AggregateID)
	if err != nil {
		_ = s.deferCommerceOutbox(ctx, event.ID, event.Attempts, "order_load_failed")
		return
	}
	if order.Status == "delivered" || order.Status == "failed" || order.Status == "funds_released" {
		_ = s.markCommerceOutboxProcessed(ctx, event.ID)
		return
	}
	if order.ProviderReference != "" {
		_ = s.reconcileCommerceOrder(ctx, order)
		_ = s.markCommerceOutboxProcessed(ctx, event.ID)
		return
	}
	if strings.TrimSpace(s.cfg.LGPDSecret) == "" {
		_ = s.deferCommerceOutbox(ctx, event.ID, event.Attempts, "lgpd_secret_missing")
		return
	}
	provider := s.commerceProvider()
	if provider == nil {
		_ = s.deferCommerceOutbox(ctx, event.ID, event.Attempts, "provider_not_configured")
		return
	}
	result, err := provider.Purchase(ctx, commercePurchaseRequest{
		Product: commerceProduct{
			ID:                order.ProductID,
			Provider:          order.ProviderSlug,
			ProviderProductID: strings.TrimPrefix(order.ProviderProductID, "gcpp_bitrefill_"),
			Type:              firstNonEmptyStr(order.ProductType, "gift_card"),
			Brand:             order.Brand,
			Title:             order.Title,
			CountryCode:       "BR",
			Currency:          order.Currency,
		},
		Quantity:         order.Quantity,
		UnitPriceMinor:   order.AmountBRLMinor / int64(maxInt(order.Quantity, 1)),
		RecipientEmail:   order.UserEmail,
		RecipientCountry: "BR",
		RecipientPhone:   order.RecipientPhone,
	})
	if err != nil {
		if commerceProviderErrorCode(err) == "provider_timeout" {
			_ = s.markCommerceOrderProviderUnknown(ctx, order.ID, err.Error())
			_ = s.deferCommerceOutbox(ctx, event.ID, event.Attempts, "provider_timeout")
			return
		}
		_ = s.failCommerceOrderAndRelease(ctx, order, commerceProviderErrorCode(err))
		_ = s.markCommerceOutboxProcessed(ctx, event.ID)
		return
	}
	_ = s.applyCommerceProviderResult(ctx, order, result)
	_ = s.markCommerceOutboxProcessed(ctx, event.ID)
}

func (s *Server) claimCommerceOutboxEvent(ctx context.Context) (commerceOutboxEvent, error) {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return commerceOutboxEvent{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	var event commerceOutboxEvent
	err = tx.QueryRowContext(ctx, `
SELECT id, event_type, aggregate_id, provider, payload::text, attempts
FROM commerce_outbox_events
WHERE status IN ('pending','retry_wait') AND next_attempt_at <= NOW()
ORDER BY created_at ASC
FOR UPDATE SKIP LOCKED
LIMIT 1`).Scan(&event.ID, &event.EventType, &event.AggregateID, &event.Provider, &event.Payload, &event.Attempts)
	if err != nil {
		return commerceOutboxEvent{}, err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE commerce_outbox_events
SET status='processing', locked_at=NOW(), attempts=attempts+1, updated_at=NOW()
WHERE id=$1`, event.ID)
	if err != nil {
		return commerceOutboxEvent{}, err
	}
	return event, tx.Commit()
}

func (s *Server) markCommerceOutboxProcessed(ctx context.Context, id string) error {
	_, err := s.db.SQL.ExecContext(ctx, `
UPDATE commerce_outbox_events
SET status='processed', processed_at=NOW(), updated_at=NOW()
WHERE id=$1`, id)
	return err
}

func (s *Server) deferCommerceOutbox(ctx context.Context, id string, attempts int, reason string) error {
	delay := time.Duration(minInt(300, 5*(attempts+1)*(attempts+1))) * time.Second
	_, err := s.db.SQL.ExecContext(ctx, `
UPDATE commerce_outbox_events
SET status='retry_wait', next_attempt_at=NOW()+($2::text)::interval, last_error=$3, updated_at=NOW()
WHERE id=$1`, id, delay.String(), reason)
	return err
}

func (s *Server) loadCommerceOrderForProvider(ctx context.Context, orderID string) (commerceOrderForProvider, error) {
	var order commerceOrderForProvider
	var amountText string
	err := s.db.SQL.QueryRowContext(ctx, `
SELECT o.id, u.email, o.wallet_address, o.product_id, o.provider_id, o.provider_product_id,
       p.slug, pp.brand, pp.title, pp.currency, pp.product_type, o.quantity, o.amount_brl::text,
       o.required_usdt_micro, o.status, o.provider_reference, COALESCE(o.provider_order_id, ''),
       o.redemption_code_enc, o.redemption_pin_enc, o.redemption_url_enc, COALESCE(o.recipient_phone, '')
FROM mobile_gift_card_orders o
JOIN users u ON u.id = o.user_id
JOIN gift_card_providers p ON p.id = o.provider_id
JOIN gift_card_provider_products pp ON pp.id = o.provider_product_id
WHERE o.id=$1`, orderID).Scan(
		&order.ID, &order.UserEmail, &order.WalletAddress, &order.ProductID, &order.ProviderID, &order.ProviderProductID,
		&order.ProviderSlug, &order.Brand, &order.Title, &order.Currency, &order.ProductType, &order.Quantity, &amountText,
		&order.RequiredUSDTMicro, &order.Status, &order.ProviderReference, &order.ProviderOrderID,
		&order.RedemptionCodeEnc, &order.RedemptionPINEnc, &order.RedemptionURLEnc, &order.RecipientPhone,
	)
	order.AmountBRLMinor = decimalToMinor(amountText, brlMinorScale)
	return order, err
}

func (s *Server) reconcileCommerceOrder(ctx context.Context, order commerceOrderForProvider) error {
	provider := s.commerceProvider()
	if provider == nil {
		return errors.New("provider not configured")
	}
	result, err := provider.GetOrder(ctx, firstNonEmptyStr(order.ProviderOrderID, order.ProviderReference))
	if err != nil {
		return err
	}
	return s.applyCommerceProviderResult(ctx, order, result)
}

func (s *Server) reconcilePendingCommerceOrders(ctx context.Context) {
	if !bitrefillLivePurchasesEnabled() || !bitrefillEnabled() {
		return
	}
	rows, err := s.db.SQL.QueryContext(ctx, `
SELECT id
FROM mobile_gift_card_orders
WHERE provider_reference <> ''
  AND status IN ('processing','purchasing','provider_pending','provider_unknown','retry_wait')
ORDER BY updated_at ASC
LIMIT 5`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var orderID string
		if err := rows.Scan(&orderID); err != nil {
			continue
		}
		order, err := s.loadCommerceOrderForProvider(ctx, orderID)
		if err != nil {
			continue
		}
		if err := s.reconcileCommerceOrder(ctx, order); err != nil {
			_ = s.markCommerceOrderProviderUnknown(ctx, order.ID, commerceProviderErrorCode(err))
		}
	}
}

func (s *Server) markCommerceOrderProviderUnknown(ctx context.Context, orderID, reason string) error {
	_, err := s.db.SQL.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status='provider_unknown', provider_status='provider_unknown', error_message=$2, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('delivered','failed','funds_released')`, orderID, reason)
	return err
}

func (s *Server) failCommerceOrderAndRelease(ctx context.Context, order commerceOrderForProvider, reason string) error {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	_, _ = tx.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status='failed', provider_status=$2, error_message=$2, updated_at=NOW()
WHERE id=$1`, order.ID, reason)
	if err := txReleaseGiftCardLocked(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) applyCommerceProviderResult(ctx context.Context, order commerceOrderForProvider, result *commercePurchaseResult) error {
	if result == nil {
		return nil
	}
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	status := firstNonEmptyStr(result.Status, "provider_pending")
	_, err = tx.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status=$2, provider_status=$3, provider_reference=COALESCE(NULLIF($4,''), provider_reference),
    provider_order_id=COALESCE(NULLIF($5,''), provider_order_id),
    redemption_code=$6, redemption_pin=$7, redemption_url=$8, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('delivered','failed','funds_released')`,
		order.ID, status, firstNonEmptyStr(result.ProviderStatus, status), result.ProviderReference,
		result.TransactionID, maskGiftCardSecret(result.RedemptionCode), maskGiftCardSecret(result.RedemptionPIN), maskGiftCardSecret(result.RedemptionURL))
	if err != nil {
		return err
	}
	txInsertGiftCardProviderAttempt(requestWithContext(ctx), tx, order.ID, order.ProviderID, "purchase", giftCardProviderResult{
		Status: status, ProviderStatus: result.ProviderStatus, ProviderReference: result.ProviderReference, ErrorMessage: result.ErrorMessage,
	})
	if status == "delivered" {
		codeEnc, pinEnc, urlEnc := s.encryptGiftCardRedemption(giftCardProviderResult{
			RedemptionCode: result.RedemptionCode,
			RedemptionPIN:  result.RedemptionPIN,
			RedemptionURL:  result.RedemptionURL,
		})
		txInsertGiftCardDelivery(requestWithContext(ctx), tx, order.ID, "code", codeEnc, pinEnc, urlEnc)
		if err := txCaptureGiftCardLocked(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
			return err
		}
	} else if status == "failed" {
		if err := txReleaseGiftCardLocked(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func requestWithContext(ctx context.Context) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://chainfx.internal/commerce-worker", nil)
	return req
}
