package mobile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"payment-gateway/internal/metrics"
)

type EfiMobileWebhookEvent struct {
	IDEnvio string
	E2EID   string
	Status  string
	Raw     []byte
}

type EfiMobileWebhookResult struct {
	Matched     bool
	Duplicate   bool
	Applied     bool
	PaymentID   string
	ExecutionID string
	Status      string
}

func ApplyEfiMobileWebhook(ctx context.Context, db *sql.DB, event EfiMobileWebhookEvent) (EfiMobileWebhookResult, error) {
	if db == nil {
		return EfiMobileWebhookResult{}, fmt.Errorf("database indisponivel")
	}
	metrics.RecordEfiMobileWebhook("received")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return EfiMobileWebhookResult{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	eventHash := mobileEfiWebhookHash(event)
	eventID := "mpevt_" + eventHash[:24]
	rawJSON := json.RawMessage(event.Raw)
	if len(rawJSON) == 0 || !json.Valid(rawJSON) {
		rawJSON = json.RawMessage(`{}`)
	}
	var insertedID string
	err = tx.QueryRowContext(ctx, `
INSERT INTO mobile_payment_provider_events
  (id, provider, provider_event_hash, provider_reference, provider_transaction_id, provider_status, raw_payload)
VALUES ($1,'efi',$2,$3,$4,$5,$6)
ON CONFLICT (provider_event_hash) DO NOTHING
RETURNING id`,
		eventID, eventHash, strings.TrimSpace(event.E2EID), strings.TrimSpace(event.IDEnvio),
		strings.TrimSpace(event.Status), rawJSON).Scan(&insertedID)
	if err == sql.ErrNoRows {
		metrics.RecordEfiMobileWebhook("duplicate")
		return EfiMobileWebhookResult{Duplicate: true}, tx.Commit()
	}
	if err != nil {
		return EfiMobileWebhookResult{}, err
	}

	claim := &mobilePaymentExecutionClaim{}
	err = tx.QueryRowContext(ctx, `
SELECT e.id, e.payment_intent_id, e.user_id::text, e.provider, e.provider_idempotency_key,
       e.status, e.attempt_count, e.provider_reference, e.provider_transaction_id, e.provider_status,
       i.status, i.wallet_address, i.funding_tx_hash, i.funding_network, i.funding_token_contract,
       i.funding_token_decimals, i.funding_amount_raw, COALESCE(f.from_address, ''),
       i.payment_type, i.raw_code, i.beneficiary_name, i.document, i.description,
       i.amount_brl::float8, i.required_usdt_micro
FROM mobile_payment_executions e
JOIN mobile_payment_intents i ON i.id=e.payment_intent_id
LEFT JOIN mobile_payment_funding_transactions f ON lower(f.tx_hash)=lower(i.funding_tx_hash)
WHERE e.provider='efi'
  AND (($1 <> '' AND e.provider_idempotency_key=$1)
    OR ($1 <> '' AND e.provider_transaction_id=$1)
    OR ($2 <> '' AND e.provider_reference=$2))
ORDER BY e.created_at
LIMIT 1
FOR UPDATE OF e, i SKIP LOCKED`, strings.TrimSpace(event.IDEnvio), strings.TrimSpace(event.E2EID)).Scan(
		&claim.ExecutionID, &claim.PaymentID, &claim.UserID, &claim.Provider, &claim.ProviderIdempotencyKey,
		&claim.StatusBefore, &claim.Attempt, &claim.ProviderReference, &claim.ProviderTransactionID, &claim.ProviderStatus,
		&claim.IntentStatus, &claim.WalletAddress, &claim.FundingTxHash, &claim.FundingNetwork,
		&claim.FundingTokenContract, &claim.FundingTokenDecimals, &claim.FundingAmountRaw, &claim.FundingFromAddress,
		&claim.PaymentType, &claim.RawCode, &claim.BeneficiaryName, &claim.Document, &claim.Description,
		&claim.AmountBRL, &claim.RequiredUSDTMic)
	if err == sql.ErrNoRows {
		_, _ = tx.ExecContext(ctx, `UPDATE mobile_payment_provider_events SET applied=false WHERE id=$1`, eventID)
		return EfiMobileWebhookResult{Matched: false}, tx.Commit()
	}
	if err != nil {
		return EfiMobileWebhookResult{}, err
	}

	result := parseMobileEfiWebhookResult(event.IDEnvio, event.E2EID, event.Status)
	out := EfiMobileWebhookResult{Matched: true, PaymentID: claim.PaymentID, ExecutionID: claim.ExecutionID, Status: result.ProviderStatus}
	switch result.Outcome {
	case mobilePaymentProviderResultCompleted:
		applied, err := applyMobileEfiWebhookCompleted(ctx, tx, claim, result)
		if err != nil {
			return EfiMobileWebhookResult{}, err
		}
		out.Applied = applied
	case mobilePaymentProviderResultFailed:
		applied, err := applyMobileEfiWebhookFailed(ctx, tx, claim, result)
		if err != nil {
			return EfiMobileWebhookResult{}, err
		}
		out.Applied = applied
	case mobilePaymentProviderResultPending:
		applied, err := applyMobileEfiWebhookPending(ctx, tx, claim, result)
		if err != nil {
			return EfiMobileWebhookResult{}, err
		}
		out.Applied = applied
	}
	if out.Applied {
		metrics.RecordEfiMobileWebhook("applied")
	}
	_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_provider_events
SET payment_id=$2, execution_id=$3, applied=$4
WHERE id=$1`, eventID, claim.PaymentID, claim.ExecutionID, out.Applied)
	if err != nil {
		return EfiMobileWebhookResult{}, err
	}
	return out, tx.Commit()
}

func applyMobileEfiWebhookCompleted(ctx context.Context, tx *sql.Tx, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) (bool, error) {
	if claim.StatusBefore == mobilePaymentExecutionStatusCompleted || claim.IntentStatus == "completed" {
		return false, nil
	}
	var refundStatus, refundTx string
	_ = tx.QueryRowContext(ctx, `
SELECT status, tx_hash
FROM mobile_payment_refunds
WHERE payment_id=$1
FOR UPDATE`, claim.PaymentID).Scan(&refundStatus, &refundTx)
	if strings.TrimSpace(refundTx) != "" || refundStatus == mobilePaymentRefundStatusBroadcast ||
		refundStatus == mobilePaymentRefundStatusConfirming || refundStatus == mobilePaymentRefundStatusCompleted {
		if err := markMobilePaymentFinancialIncident(ctx, tx, claim.PaymentID, claim.ExecutionID, "mprefund_"+mobilePayHash(claim.PaymentID)[:24], "efi_late_success_after_refund_broadcast"); err != nil {
			return false, err
		}
		return true, nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='cancelled', last_error='Efí completed antes do refund broadcast', updated_at=NOW()
WHERE payment_id=$1 AND status IN ('pending','retry_wait','provider_unknown','manual_review')`, claim.PaymentID); err != nil {
		return false, err
	}
	store := &mobilePaymentSQLStore{}
	return true, store.completeExecutionTx(ctx, tx, claim, result)
}

func applyMobileEfiWebhookFailed(ctx context.Context, tx *sql.Tx, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) (bool, error) {
	if claim.StatusBefore == mobilePaymentExecutionStatusCompleted || claim.IntentStatus == "completed" {
		return false, nil
	}
	store := &mobilePaymentSQLStore{}
	return true, store.failExecutionForRefundTx(ctx, tx, claim, "efi_webhook_provider_rejected", result)
}

func applyMobileEfiWebhookPending(ctx context.Context, tx *sql.Tx, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) (bool, error) {
	if claim.StatusBefore == mobilePaymentExecutionStatusCompleted || claim.IntentStatus == "completed" {
		return false, nil
	}
	_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='provider_pending', provider_reference=$2, provider_transaction_id=$3, provider_status=$4, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','failed')`,
		claim.ExecutionID, firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
		firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID),
		firstNonEmptyStr(result.ProviderStatus, "provider_pending"))
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='provider_pending', provider_status=$2, provider_reference=$3, provider_transaction_id=$4, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','refund_pending','refunded')`,
		claim.PaymentID, firstNonEmptyStr(result.ProviderStatus, "provider_pending"),
		firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
		firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID))
	return err == nil, err
}

func parseMobileEfiWebhookResult(idEnvio, e2eID, status string) mobilePaymentProviderResult {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "" {
		status = "SUBMITTED"
	}
	outcome := mobilePaymentProviderResultPending
	switch {
	case strings.Contains(status, "REALIZ") || strings.Contains(status, "CONCL") || strings.Contains(status, "CONFIRM") || strings.Contains(status, "EFETIV"):
		outcome = mobilePaymentProviderResultCompleted
	case strings.Contains(status, "REJEIT") || strings.Contains(status, "CANCEL") || strings.Contains(status, "FALH") || strings.Contains(status, "DEVOLV"):
		outcome = mobilePaymentProviderResultFailed
	}
	return mobilePaymentProviderResult{
		Outcome:               outcome,
		ProviderReference:     firstNonEmptyStr(e2eID, idEnvio),
		ProviderTransactionID: firstNonEmptyStr(idEnvio, e2eID),
		ProviderStatus:        status,
	}
}

func mobileEfiWebhookHash(event EfiMobileWebhookEvent) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(event.IDEnvio) + "|" + strings.TrimSpace(event.E2EID) + "|" + strings.TrimSpace(event.Status) + "|" + string(event.Raw)))
	return hex.EncodeToString(sum[:])
}
