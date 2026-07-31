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

	"payment-gateway/internal/email"
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
	FundingMethod     string
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
	if isPaymentEngineCommerceProduct(order.ProductType) && order.ProviderSlug == "bitrefill" {
		if order.Status == "provider_unknown" && strings.TrimSpace(order.ProviderReference) == "" {
			_ = s.markGiftCardPaymentExecutionManualReview(ctx, order.ID, "provider_unknown_without_reference")
			_ = s.markCommerceOutboxProcessed(ctx, event.ID)
			return
		}
		if ok, reason := s.claimGiftCardPaymentExecution(ctx, order.ID); !ok {
			_ = s.deferCommerceOutbox(ctx, event.ID, event.Attempts, reason)
			return
		}
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
		CustomIdentifier: "giftcard:" + order.ID,
	})
	if err != nil {
		if commerceProviderErrorCode(err) == "provider_timeout" {
			_ = s.markCommerceOrderProviderUnknown(ctx, order.ID, err.Error())
			_ = s.markGiftCardPaymentExecutionUnknown(ctx, order.ID, err.Error())
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
       o.redemption_code_enc, o.redemption_pin_enc, o.redemption_url_enc, COALESCE(o.recipient_phone, ''), COALESCE(o.funding_method, 'internal_usdt')
FROM mobile_gift_card_orders o
JOIN users u ON u.id = o.user_id
JOIN gift_card_providers p ON p.id = o.provider_id
JOIN gift_card_provider_products pp ON pp.id = o.provider_product_id
WHERE o.id=$1`, orderID).Scan(
		&order.ID, &order.UserEmail, &order.WalletAddress, &order.ProductID, &order.ProviderID, &order.ProviderProductID,
		&order.ProviderSlug, &order.Brand, &order.Title, &order.Currency, &order.ProductType, &order.Quantity, &amountText,
		&order.RequiredUSDTMicro, &order.Status, &order.ProviderReference, &order.ProviderOrderID,
		&order.RedemptionCodeEnc, &order.RedemptionPINEnc, &order.RedemptionURLEnc, &order.RecipientPhone, &order.FundingMethod,
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

func (s *Server) markGiftCardPaymentExecutionUnknown(ctx context.Context, orderID, reason string) error {
	_, err := s.db.SQL.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='provider_unknown',
    provider_status='provider_unknown',
    error_message=$2,
    ambiguous_submit=TRUE,
    first_ambiguous_at=COALESCE(first_ambiguous_at, NOW()),
    next_attempt_at=NOW()+interval '5 minutes',
    updated_at=NOW()
WHERE payment_intent_id=$1 AND provider='bitrefill' AND status='processing'`, orderID, reason)
	return err
}

func (s *Server) markGiftCardPaymentExecutionManualReview(ctx context.Context, orderID, reason string) error {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='manual_review', provider_status='manual_review', error_message=$2, updated_at=NOW()
WHERE payment_intent_id=$1 AND provider='bitrefill' AND status NOT IN ('completed','failed')`, orderID, reason); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='manual_review', provider_status='bitrefill_manual_review', error_message=$2, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','refunded')`, orderID, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) failCommerceOrderAndRelease(ctx context.Context, order commerceOrderForProvider, reason string) error {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status='failed', provider_status=$2, error_message=$2, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('delivered','failed','funds_released','refunded')`, order.ID, reason)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return tx.Commit()
	}
	if err := txReleaseGiftCardLocked(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
		return err
	}
	if err := txSyncGiftCardPaymentFailure(ctx, tx, order, reason); err != nil {
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
	var currentStatus string
	var capturedAt, refundedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `
SELECT status, captured_at, refunded_at
FROM mobile_gift_card_orders
WHERE id=$1
FOR UPDATE`, order.ID).Scan(&currentStatus, &capturedAt, &refundedAt); err != nil {
		return err
	}
	txInsertGiftCardProviderAttempt(requestWithContext(ctx), tx, order.ID, order.ProviderID, "purchase", giftCardProviderResult{
		Status: status, ProviderStatus: result.ProviderStatus, ProviderReference: result.ProviderReference, ErrorMessage: result.ErrorMessage,
	})
	if status == "delivered" {
		if currentStatus == "delivered" || currentStatus == "failed" || currentStatus == "funds_released" || currentStatus == "refunded" || capturedAt.Valid {
			return tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status=$2, provider_status=$3, provider_reference=COALESCE(NULLIF($4,''), provider_reference),
    provider_order_id=COALESCE(NULLIF($5,''), provider_order_id),
    redemption_code=$6, redemption_pin=$7, redemption_url=$8, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('delivered','failed','funds_released','refunded')`,
			order.ID, status, firstNonEmptyStr(result.ProviderStatus, status), result.ProviderReference,
			result.TransactionID, maskGiftCardSecret(result.RedemptionCode), maskGiftCardSecret(result.RedemptionPIN), maskGiftCardSecret(result.RedemptionURL)); err != nil {
			return err
		}
		codeEnc, pinEnc, urlEnc := s.encryptGiftCardRedemption(giftCardProviderResult{
			RedemptionCode: result.RedemptionCode,
			RedemptionPIN:  result.RedemptionPIN,
			RedemptionURL:  result.RedemptionURL,
		})
		txInsertGiftCardDelivery(requestWithContext(ctx), tx, order.ID, "code", codeEnc, pinEnc, urlEnc)
		if order.FundingMethod == "internal_usdt" {
			if err := txCaptureGiftCardLocked(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
				return err
			}
		}
		if err := txSyncGiftCardPaymentCompleted(ctx, tx, order, result); err != nil {
			return err
		}
	} else if status == "failed" {
		if currentStatus == "delivered" || currentStatus == "failed" || currentStatus == "funds_released" || currentStatus == "refunded" || capturedAt.Valid || refundedAt.Valid {
			return tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status='failed', provider_status=$2, error_message=$2, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('delivered','failed','funds_released','refunded')`, order.ID, firstNonEmptyStr(result.ProviderStatus, "failed")); err != nil {
			return err
		}
		if order.FundingMethod == "internal_usdt" {
			if err := txReleaseGiftCardLocked(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
				return err
			}
		} else {
			_, _ = tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='refund_pending', provider_status=$2, refund_reason=$2, refund_amount_micro=$3, updated_at=NOW()
WHERE id=$1`, order.ID, firstNonEmptyStr(result.ProviderStatus, "failed"), order.RequiredUSDTMicro)
		}
		if err := txSyncGiftCardPaymentFailure(ctx, tx, order, firstNonEmptyStr(result.ProviderStatus, "failed")); err != nil {
			return err
		}
	} else if status == "refunded" {
		if refundedAt.Valid || currentStatus == "refunded" || currentStatus == "funds_released" || currentStatus == "failed" {
			return tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status='refunded',
    provider_status=$2,
    provider_reference=COALESCE(NULLIF($3,''), provider_reference),
    provider_order_id=COALESCE(NULLIF($4,''), provider_order_id),
    error_message=COALESCE(NULLIF($5,''), error_message),
    updated_at=NOW()
WHERE id=$1 AND status NOT IN ('failed','funds_released','refunded')`,
			order.ID, firstNonEmptyStr(result.ProviderStatus, "refunded"), result.ProviderReference, result.TransactionID, result.ErrorMessage); err != nil {
			return err
		}
		if order.FundingMethod != "internal_usdt" {
			_, _ = tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='refund_pending', provider_status=$2, refund_reason=$2, refund_amount_micro=$3, updated_at=NOW()
WHERE id=$1`, order.ID, firstNonEmptyStr(result.ProviderStatus, "refunded"), order.RequiredUSDTMicro)
		} else if capturedAt.Valid || currentStatus == "delivered" {
			if err := txCreditGiftCardProviderRefund(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
				return err
			}
		} else {
			if err := txReleaseGiftCardLocked(requestWithContext(ctx), tx, order.WalletAddress, order.ID, order.RequiredUSDTMicro); err != nil {
				return err
			}
		}
		if err := txSyncGiftCardPaymentRefunded(ctx, tx, order, result); err != nil {
			return err
		}
	} else {
		if currentStatus == "delivered" || currentStatus == "failed" || currentStatus == "funds_released" || currentStatus == "refunded" {
			return tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET status=$2, provider_status=$3, provider_reference=COALESCE(NULLIF($4,''), provider_reference),
    provider_order_id=COALESCE(NULLIF($5,''), provider_order_id),
    redemption_code=$6, redemption_pin=$7, redemption_url=$8, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('delivered','failed','funds_released','refunded')`,
			order.ID, status, firstNonEmptyStr(result.ProviderStatus, status), result.ProviderReference,
			result.TransactionID, maskGiftCardSecret(result.RedemptionCode), maskGiftCardSecret(result.RedemptionPIN), maskGiftCardSecret(result.RedemptionURL)); err != nil {
			return err
		}
		if err := txSyncGiftCardPaymentPending(ctx, tx, order, result); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if status == "delivered" {
		s.sendCommerceProviderOrderEmailAsync(order, result)
	}
	return nil
}

func (s *Server) claimGiftCardPaymentExecution(ctx context.Context, orderID string) (bool, string) {
	tx, err := s.db.SQL.BeginTx(ctx, nil)
	if err != nil {
		return false, "execution_claim_db_failed"
	}
	defer tx.Rollback() //nolint:errcheck
	var executionID, status string
	err = tx.QueryRowContext(ctx, `
SELECT e.id, e.status
FROM mobile_payment_executions e
WHERE e.payment_intent_id=$1 AND e.provider='bitrefill'
FOR UPDATE SKIP LOCKED`, orderID).Scan(&executionID, &status)
	if err == sql.ErrNoRows {
		return false, "execution_missing_or_locked"
	}
	if err != nil {
		return false, "execution_claim_failed"
	}
	switch status {
	case "pending", "retry_wait":
	case "provider_unknown", "provider_pending":
		return false, "execution_reconcile_required"
	default:
		return false, "execution_not_runnable_" + status
	}
	_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='processing',
    attempt_count=attempt_count+1,
    started_at=COALESCE(started_at, NOW()),
    last_attempt_at=NOW(),
    submit_started_at=COALESCE(submit_started_at, NOW()),
    submit_outcome='submit_started',
    updated_at=NOW()
WHERE id=$1 AND status=$2`, executionID, status)
	if err != nil {
		return false, "execution_update_failed"
	}
	_, _ = tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='processing', provider_status='bitrefill_processing', updated_at=NOW()
WHERE id=$1 AND status IN ('reserved','processing','provider_pending')`, orderID)
	if err := tx.Commit(); err != nil {
		return false, "execution_claim_commit_failed"
	}
	return true, ""
}

func requestWithContext(ctx context.Context) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://chainfx.internal/commerce-worker", nil)
	return req
}

func txSyncGiftCardPaymentCompleted(ctx context.Context, tx *sql.Tx, order commerceOrderForProvider, result *commercePurchaseResult) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='completed',
    provider_reference=COALESCE(NULLIF($2,''), provider_reference),
    provider_transaction_id=COALESCE(NULLIF($3,''), provider_transaction_id),
    provider_status=$4,
    submit_completed_at=COALESCE(submit_completed_at, NOW()),
    submit_outcome='submit_confirmed',
    completed_at=NOW(),
    updated_at=NOW()
WHERE payment_intent_id=$1 AND provider='bitrefill' AND status <> 'completed'`,
		order.ID, result.ProviderReference, result.TransactionID, firstNonEmptyStr(result.ProviderStatus, "delivered")); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='completed',
    provider_status=$2,
    provider_reference=COALESCE(NULLIF($3,''), provider_reference),
    provider_transaction_id=COALESCE(NULLIF($4,''), provider_transaction_id),
    completed_at=COALESCE(completed_at, NOW()),
    updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`,
		order.ID, firstNonEmptyStr(result.ProviderStatus, "delivered"), result.ProviderReference, result.TransactionID)
	return err
}

func txSyncGiftCardPaymentFailure(ctx context.Context, tx *sql.Tx, order commerceOrderForProvider, reason string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='failed',
    provider_status=$2,
    error_message=$2,
    failed_at=NOW(),
    submit_outcome='definitive_failed',
    updated_at=NOW()
WHERE payment_intent_id=$1 AND provider='bitrefill' AND status <> 'completed'`, order.ID, reason); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='failed',
    provider_status=$2,
    error_message=$2,
    failed_at=COALESCE(failed_at, NOW()),
    updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','refunded')`, order.ID, reason)
	return err
}

func txSyncGiftCardPaymentPending(ctx context.Context, tx *sql.Tx, order commerceOrderForProvider, result *commercePurchaseResult) error {
	status := firstNonEmptyStr(result.Status, "provider_pending")
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='provider_pending',
    provider_reference=COALESCE(NULLIF($2,''), provider_reference),
    provider_transaction_id=COALESCE(NULLIF($3,''), provider_transaction_id),
    provider_status=$4,
    next_attempt_at=NOW()+interval '5 minutes',
    updated_at=NOW()
WHERE payment_intent_id=$1 AND provider='bitrefill' AND status <> 'completed'`,
		order.ID, result.ProviderReference, result.TransactionID, firstNonEmptyStr(result.ProviderStatus, status)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='provider_pending',
    provider_status=$2,
    provider_reference=COALESCE(NULLIF($3,''), provider_reference),
    provider_transaction_id=COALESCE(NULLIF($4,''), provider_transaction_id),
    updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','refunded')`,
		order.ID, firstNonEmptyStr(result.ProviderStatus, status), result.ProviderReference, result.TransactionID)
	return err
}

func txSyncGiftCardPaymentRefunded(ctx context.Context, tx *sql.Tx, order commerceOrderForProvider, result *commercePurchaseResult) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='completed',
    provider_reference=COALESCE(NULLIF($2,''), provider_reference),
    provider_transaction_id=COALESCE(NULLIF($3,''), provider_transaction_id),
    provider_status=$4,
    updated_at=NOW()
WHERE payment_intent_id=$1 AND provider='bitrefill'`,
		order.ID, result.ProviderReference, result.TransactionID, firstNonEmptyStr(result.ProviderStatus, "refunded")); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='refunded',
    provider_status='provider_refunded',
    refund_reason='bitrefill_post_delivery_refund',
    refund_amount_micro=$2,
    refunded_at=COALESCE(refunded_at, NOW()),
    updated_at=NOW()
WHERE id=$1 AND status <> 'refunded'`, order.ID, order.RequiredUSDTMicro)
	return err
}

func (s *Server) sendCommerceProviderOrderEmailAsync(order commerceOrderForProvider, result *commercePurchaseResult) {
	if s == nil || s.cfg == nil || strings.TrimSpace(order.UserEmail) == "" || result == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status := "sent"
		mailer := email.NewService(s.cfg)
		if !mailer.Enabled() {
			status = "smtp_not_configured"
		} else if err := mailer.SendTransaction(order.UserEmail, giftCardEmailSubject(order.ProductType), email.TransactionReceipt{
			Title: giftCardEmailTitle(order.ProductType),
			Intro: "Seu pedido foi concluido e os dados de resgate estao abaixo.",
			CTA:   "Abrir ChainFX",
			Details: giftCardEmailDetails(
				order.ID,
				firstNonEmptyStr(order.Title, order.Brand),
				order.ProductType,
				order.AmountBRLMinor,
				order.RequiredUSDTMicro,
				firstNonEmptyStr(result.Status, "delivered"),
				result.RedemptionCode,
				result.RedemptionPIN,
				result.RedemptionURL,
			),
		}); err != nil {
			status = "send_failed"
		}
		_, _ = s.db.SQL.ExecContext(ctx, `
UPDATE mobile_gift_card_orders
SET email_status=$2, updated_at=NOW()
WHERE id=$1`, order.ID, status)
	}()
}
