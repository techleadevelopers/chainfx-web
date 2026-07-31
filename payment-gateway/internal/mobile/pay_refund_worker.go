package mobile

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/security"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	mobilePaymentRefundStatusPending         = "pending"
	mobilePaymentRefundStatusProcessing      = "processing"
	mobilePaymentRefundStatusBroadcast       = "broadcast"
	mobilePaymentRefundStatusConfirming      = "confirming"
	mobilePaymentRefundStatusCompleted       = "completed"
	mobilePaymentRefundStatusRetryWait       = "retry_wait"
	mobilePaymentRefundStatusProviderUnknown = "provider_unknown"
	mobilePaymentRefundStatusFailed          = "failed"
	mobilePaymentRefundStatusManualReview    = "manual_review"
	mobilePaymentRefundStatusCancelled       = "cancelled"

	mobileRefundActionBroadcast = "broadcast"
	mobileRefundActionConfirm   = "confirm"
	mobileRefundActionReconcile = "reconcile"
)

type mobilePaymentRefundWorker struct {
	store       mobilePaymentRefundStore
	signer      mobilePaymentRefundSigner
	verifier    mobilePaymentRefundVerifier
	pollEvery   time.Duration
	staleAfter  time.Duration
	maxAttempts int
	now         func() time.Time
}

type mobilePaymentRefundStore interface {
	RecoverStaleRefunds(ctx context.Context, staleAfter time.Duration) (int64, error)
	ClaimNextRefund(ctx context.Context, maxAttempts int) (*mobilePaymentRefundClaim, error)
	MarkRefundBroadcast(ctx context.Context, claim *mobilePaymentRefundClaim, txHash string) error
	MarkRefundRetry(ctx context.Context, claim *mobilePaymentRefundClaim, reason string, nextAttemptAt time.Time) error
	MarkRefundUnknown(ctx context.Context, claim *mobilePaymentRefundClaim, reason string, nextAttemptAt time.Time) error
	CompleteRefund(ctx context.Context, claim *mobilePaymentRefundClaim, receipt mobilePaymentRefundReceipt) error
	MarkRefundManualReview(ctx context.Context, claim *mobilePaymentRefundClaim, reason string) error
	CancelRefund(ctx context.Context, claim *mobilePaymentRefundClaim, reason string) error
}

type mobilePaymentRefundSigner interface {
	BroadcastRefund(ctx context.Context, req mobilePaymentRefundSignerRequest) (mobilePaymentRefundSignerResult, error)
	ReconcileRefund(ctx context.Context, req mobilePaymentRefundSignerRequest) (mobilePaymentRefundSignerResult, error)
}

type mobilePaymentRefundVerifier interface {
	VerifyRefundReceipt(ctx context.Context, refund mobilePaymentRefundClaim) (mobilePaymentRefundReceipt, error)
}

type mobilePaymentRefundClaim struct {
	RefundID          string
	PaymentID         string
	ExecutionID       string
	UserID            string
	WalletAddress     string
	Asset             string
	Network           string
	TokenContract     string
	TokenDecimals     int
	AmountMicro       int64
	AmountRaw         string
	StatusBefore      string
	RefundReason      string
	IdempotencyKey    string
	SignerOperationID string
	TxHash            string
	Attempt           int
	Action            string
	ExecutionStatus   string
	IntentStatus      string
	ProviderStatus    string
	FundingTxHash     string
	TreasuryAddress   string
}

type mobilePaymentRefundSignerRequest struct {
	RefundID          string
	PaymentID         string
	To                string
	Amount            string
	AmountRaw         string
	TokenContract     string
	Network           string
	IdempotencyKey    string
	SignerOperationID string
}

type mobilePaymentRefundSignerResult struct {
	TxHash  string
	From    string
	Network string
	Status  string
}

type mobilePaymentRefundReceipt struct {
	TxHash        string
	BlockNumber   uint64
	BlockHash     string
	LogIndex      int
	Confirmations uint64
	Status        uint64
	From          string
	To            string
	AmountRaw     string
}

func (s *Server) startMobilePaymentRefundWorker(ctx context.Context) {
	if s == nil || s.db == nil || s.db.SQL == nil || s.cfg == nil {
		return
	}
	if err := mobileDB(s.db).ensureMobilePaySchema(ctx); err != nil {
		slog.Error("mobile payment refund worker: schema indisponivel", "err", err)
		return
	}
	worker := &mobilePaymentRefundWorker{
		store:       &mobilePaymentRefundSQLStore{db: s.db.SQL},
		signer:      newMobilePaymentRefundSignerClient(s.cfg),
		verifier:    s,
		pollEvery:   time.Duration(envInt("MOBILE_PAYMENT_REFUND_POLL_SEC", 10)) * time.Second,
		staleAfter:  time.Duration(envInt("MOBILE_PAYMENT_REFUND_STALE_SEC", 180)) * time.Second,
		maxAttempts: envInt("MOBILE_PAYMENT_REFUND_MAX_ATTEMPTS", 6),
		now:         time.Now,
	}
	if worker.pollEvery <= 0 {
		worker.pollEvery = 10 * time.Second
	}
	if worker.staleAfter <= 0 {
		worker.staleAfter = 3 * time.Minute
	}
	if worker.maxAttempts <= 0 {
		worker.maxAttempts = 6
	}
	worker.Run(ctx)
}

func (w *mobilePaymentRefundWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollEvery)
	defer ticker.Stop()
	for {
		w.runTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *mobilePaymentRefundWorker) runTick(ctx context.Context) {
	if recovered, err := w.store.RecoverStaleRefunds(ctx, w.staleAfter); err != nil {
		slog.Error("mobile payment refund stale recovery falhou", "err", err)
	} else if recovered > 0 {
		slog.Warn("mobile payment refund stale recuperado", "count", recovered)
	}
	for i := 0; i < envInt("MOBILE_PAYMENT_REFUND_BATCH_SIZE", 5); i++ {
		ok, err := w.runOnce(ctx)
		if err != nil {
			slog.Error("mobile payment refund tick falhou", "err", err)
			return
		}
		if !ok {
			return
		}
	}
}

func (w *mobilePaymentRefundWorker) runOnce(ctx context.Context) (bool, error) {
	claim, err := w.store.ClaimNextRefund(ctx, w.maxAttempts)
	if err != nil || claim == nil {
		return claim != nil, err
	}
	w.processRefund(ctx, claim)
	return true, nil
}

func (w *mobilePaymentRefundWorker) processRefund(ctx context.Context, claim *mobilePaymentRefundClaim) {
	start := time.Now()
	statusBefore := claim.StatusBefore
	switch claim.Action {
	case mobileRefundActionBroadcast:
		w.broadcastRefund(ctx, claim, start, statusBefore)
	case mobileRefundActionConfirm:
		w.confirmRefund(ctx, claim, start, statusBefore)
	default:
		w.reconcileRefund(ctx, claim, start, statusBefore)
	}
}

func (w *mobilePaymentRefundWorker) broadcastRefund(ctx context.Context, claim *mobilePaymentRefundClaim, start time.Time, statusBefore string) {
	req := mobilePaymentRefundSignerRequest{
		RefundID:          claim.RefundID,
		PaymentID:         claim.PaymentID,
		To:                claim.WalletAddress,
		Amount:            mobilePayRawToDecimal(claim.AmountRaw, claim.TokenDecimals),
		AmountRaw:         claim.AmountRaw,
		TokenContract:     claim.TokenContract,
		Network:           claim.Network,
		IdempotencyKey:    claim.IdempotencyKey,
		SignerOperationID: claim.SignerOperationID,
	}
	result, err := w.signer.BroadcastRefund(ctx, req)
	if err != nil {
		w.applyRefundError(ctx, claim, err)
		slog.Warn("mobile payment refund broadcast erro", "payment_id", claim.PaymentID, "execution_id", claim.ExecutionID,
			"refund_id", claim.RefundID, "attempt", claim.Attempt, "status_before", statusBefore,
			"latency_ms", time.Since(start).Milliseconds(), "error_class", mobileRefundErrorClass(err))
		return
	}
	_ = w.store.MarkRefundBroadcast(ctx, claim, result.TxHash)
	metrics.RecordMobilePaymentRefund("broadcast")
	slog.Info("mobile payment refund broadcast", "payment_id", claim.PaymentID, "execution_id", claim.ExecutionID,
		"refund_id", claim.RefundID, "attempt", claim.Attempt, "status_before", statusBefore,
		"status_after", mobilePaymentRefundStatusConfirming, "tx_hash", result.TxHash,
		"latency_ms", time.Since(start).Milliseconds())
}

func (w *mobilePaymentRefundWorker) confirmRefund(ctx context.Context, claim *mobilePaymentRefundClaim, start time.Time, statusBefore string) {
	receipt, err := w.verifier.VerifyRefundReceipt(ctx, *claim)
	if err != nil {
		if _, ok := isMobilePayFundingPending(err); ok {
			_ = w.store.MarkRefundBroadcast(ctx, claim, claim.TxHash)
			return
		}
		if errors.Is(err, errMobileRefundReceiptReverted) {
			_ = w.store.MarkRefundManualReview(ctx, claim, "refund tx reverted")
			return
		}
		_ = w.store.MarkRefundUnknown(ctx, claim, err.Error(), w.now().UTC().Add(w.backoff(claim.Attempt)))
		metrics.RecordMobilePaymentRefund("unknown")
		return
	}
	_ = w.store.CompleteRefund(ctx, claim, receipt)
	metrics.RecordMobilePaymentRefund("completed")
	slog.Info("mobile payment refund confirmed", "payment_id", claim.PaymentID, "execution_id", claim.ExecutionID,
		"refund_id", claim.RefundID, "attempt", claim.Attempt, "status_before", statusBefore,
		"status_after", mobilePaymentRefundStatusCompleted, "tx_hash", receipt.TxHash,
		"latency_ms", time.Since(start).Milliseconds())
}

func (w *mobilePaymentRefundWorker) reconcileRefund(ctx context.Context, claim *mobilePaymentRefundClaim, start time.Time, statusBefore string) {
	if strings.TrimSpace(claim.TxHash) != "" {
		w.confirmRefund(ctx, claim, start, statusBefore)
		return
	}
	req := mobilePaymentRefundSignerRequest{
		RefundID:          claim.RefundID,
		PaymentID:         claim.PaymentID,
		To:                claim.WalletAddress,
		Amount:            mobilePayRawToDecimal(claim.AmountRaw, claim.TokenDecimals),
		AmountRaw:         claim.AmountRaw,
		TokenContract:     claim.TokenContract,
		Network:           claim.Network,
		IdempotencyKey:    claim.IdempotencyKey,
		SignerOperationID: claim.SignerOperationID,
	}
	result, err := w.signer.ReconcileRefund(ctx, req)
	if err != nil {
		if mobileRefundErrorClass(err) == mobilePaymentProviderErrorTransient {
			_ = w.store.MarkRefundRetry(ctx, claim, err.Error(), w.now().UTC().Add(w.backoff(claim.Attempt)))
			return
		}
		if claim.Attempt >= w.maxAttempts {
			_ = w.store.MarkRefundManualReview(ctx, claim, err.Error())
			metrics.RecordMobilePaymentRefund("manual_review")
		} else {
			_ = w.store.MarkRefundUnknown(ctx, claim, err.Error(), w.now().UTC().Add(w.backoff(claim.Attempt)))
			metrics.RecordMobilePaymentRefund("unknown")
		}
		return
	}
	if strings.TrimSpace(result.TxHash) == "" {
		_ = w.store.MarkRefundUnknown(ctx, claim, "signer sem tx_hash para operation", w.now().UTC().Add(w.backoff(claim.Attempt)))
		return
	}
	_ = w.store.MarkRefundBroadcast(ctx, claim, result.TxHash)
	metrics.RecordMobilePaymentRefund("broadcast")
	if result.Status == "confirmed" {
		claim.TxHash = result.TxHash
		claim.StatusBefore = mobilePaymentRefundStatusConfirming
		w.confirmRefund(ctx, claim, start, statusBefore)
	}
}

func (w *mobilePaymentRefundWorker) applyRefundError(ctx context.Context, claim *mobilePaymentRefundClaim, err error) {
	class := mobileRefundErrorClass(err)
	retryAfter := mobileRefundRetryAfter(err)
	if retryAfter <= 0 {
		retryAfter = w.backoff(claim.Attempt)
	}
	if class == mobilePaymentProviderErrorAmbiguous {
		_ = w.store.MarkRefundUnknown(ctx, claim, err.Error(), w.now().UTC().Add(retryAfter))
		metrics.RecordMobilePaymentRefund("unknown")
		return
	}
	if claim.Attempt >= w.maxAttempts {
		_ = w.store.MarkRefundManualReview(ctx, claim, err.Error())
		metrics.RecordMobilePaymentRefund("manual_review")
		return
	}
	_ = w.store.MarkRefundRetry(ctx, claim, err.Error(), w.now().UTC().Add(retryAfter))
}

func (w *mobilePaymentRefundWorker) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	seconds := 60 * math.Pow(2, float64(attempt-1))
	if seconds > 600 {
		seconds = 600
	}
	return time.Duration(seconds) * time.Second
}

type mobilePaymentRefundSQLStore struct {
	db *sql.DB
}

func (s *mobilePaymentRefundSQLStore) RecoverStaleRefunds(ctx context.Context, staleAfter time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='provider_unknown',
    last_error='refund processing stale; enviado para reconciliation',
    next_attempt_at=NOW(),
    updated_at=NOW()
WHERE status='processing'
  AND COALESCE(started_at, updated_at) <= NOW() - ($1::bigint * interval '1 millisecond')`,
		staleAfter.Milliseconds())
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}

func (s *mobilePaymentRefundSQLStore) ClaimNextRefund(ctx context.Context, maxAttempts int) (*mobilePaymentRefundClaim, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	claim := &mobilePaymentRefundClaim{}
	err = tx.QueryRowContext(ctx, `
SELECT r.id, r.payment_id, r.execution_id, r.user_id::text, r.wallet_address, r.asset, r.network,
       r.token_contract, r.token_decimals, r.amount_micro, r.amount_raw, r.status, r.refund_reason,
       r.idempotency_key, r.signer_operation_id, r.tx_hash, r.attempt_count,
       e.status, i.status, COALESCE(NULLIF(e.provider_status, ''), i.provider_status), i.funding_tx_hash, i.treasury_address
FROM mobile_payment_refunds r
JOIN mobile_payment_intents i ON i.id=r.payment_id
JOIN mobile_payment_executions e ON e.id=r.execution_id
WHERE r.status IN ('pending','retry_wait','provider_unknown','broadcast','confirming')
  AND r.next_attempt_at <= NOW()
ORDER BY r.created_at
LIMIT 1
FOR UPDATE OF r, i, e SKIP LOCKED`).Scan(
		&claim.RefundID, &claim.PaymentID, &claim.ExecutionID, &claim.UserID, &claim.WalletAddress,
		&claim.Asset, &claim.Network, &claim.TokenContract, &claim.TokenDecimals, &claim.AmountMicro,
		&claim.AmountRaw, &claim.StatusBefore, &claim.RefundReason, &claim.IdempotencyKey,
		&claim.SignerOperationID, &claim.TxHash, &claim.Attempt, &claim.ExecutionStatus,
		&claim.IntentStatus, &claim.ProviderStatus, &claim.FundingTxHash, &claim.TreasuryAddress)
	if err == sql.ErrNoRows {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if claim.ExecutionStatus == mobilePaymentExecutionStatusCompleted || claim.IntentStatus == "completed" {
		if strings.TrimSpace(claim.TxHash) != "" && claim.StatusBefore != mobilePaymentRefundStatusCompleted {
			_ = markMobilePaymentFinancialIncident(ctx, tx, claim.PaymentID, claim.ExecutionID, claim.RefundID, "efi_completed_after_refund_broadcast")
			return nil, tx.Commit()
		}
		_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='cancelled', last_error='Efí completed antes do refund broadcast', updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed')`, claim.RefundID)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if !mobileRefundEligibleForBroadcast(claim) {
		metrics.RecordEfiReconcile("refund_blocked_ambiguous")
		_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='manual_review', last_error='refund nao elegivel pelo estado atual do provider', updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed')`, claim.RefundID)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if claim.Attempt >= maxAttempts {
		_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='manual_review', last_error='max attempts atingido', updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed')`, claim.RefundID)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	switch claim.StatusBefore {
	case mobilePaymentRefundStatusBroadcast, mobilePaymentRefundStatusConfirming:
		claim.Action = mobileRefundActionConfirm
	case mobilePaymentRefundStatusProviderUnknown:
		claim.Action = mobileRefundActionReconcile
	default:
		claim.Action = mobileRefundActionBroadcast
	}
	err = tx.QueryRowContext(ctx, `
UPDATE mobile_payment_refunds
SET status='processing',
    attempt_count=attempt_count+1,
    started_at=COALESCE(started_at, NOW()),
    last_error='',
    updated_at=NOW()
WHERE id=$1 AND status=$2 AND status <> 'completed'
RETURNING attempt_count`, claim.RefundID, claim.StatusBefore).Scan(&claim.Attempt)
	if err != nil {
		return nil, err
	}
	return claim, tx.Commit()
}

func (s *mobilePaymentRefundSQLStore) MarkRefundBroadcast(ctx context.Context, claim *mobilePaymentRefundClaim, txHash string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='confirming', tx_hash=$2, broadcast_at=COALESCE(broadcast_at, NOW()), next_attempt_at=NOW(), updated_at=NOW()
WHERE id=$1 AND status='processing'`, claim.RefundID, strings.ToLower(strings.TrimSpace(txHash)))
	return err
}

func (s *mobilePaymentRefundSQLStore) MarkRefundRetry(ctx context.Context, claim *mobilePaymentRefundClaim, reason string, nextAttemptAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='retry_wait', last_error=$2, next_attempt_at=$3, updated_at=NOW()
WHERE id=$1 AND status='processing'`, claim.RefundID, truncateMobilePaymentError(reason), nextAttemptAt)
	return err
}

func (s *mobilePaymentRefundSQLStore) MarkRefundUnknown(ctx context.Context, claim *mobilePaymentRefundClaim, reason string, nextAttemptAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='provider_unknown', last_error=$2, next_attempt_at=$3, updated_at=NOW()
WHERE id=$1 AND status='processing'`, claim.RefundID, truncateMobilePaymentError(reason), nextAttemptAt)
	return err
}

func (s *mobilePaymentRefundSQLStore) CompleteRefund(ctx context.Context, claim *mobilePaymentRefundClaim, receipt mobilePaymentRefundReceipt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='completed', tx_hash=$2, confirmed_at=NOW(), updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`,
		claim.RefundID, strings.ToLower(receipt.TxHash)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='refunded',
    provider_status='refund_confirmed',
    refund_tx_hash=$2,
    refunded_at=NOW(),
    updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','refunded')`,
		claim.PaymentID, strings.ToLower(receipt.TxHash)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO mobile_payment_ledger_entries
  (id, payment_intent_id, user_id, entry_type, asset, network, amount_micro, tx_hash, provider, metadata)
VALUES ($1,$2,$3::uuid,'refund_completed','USDT',$4,$5,$6,'signer',
        jsonb_build_object('refund_id',$7,'block_number',$8,'confirmations',$9))
ON CONFLICT (payment_intent_id, entry_type) DO NOTHING`,
		"mpledger_"+mobilePayHash(claim.PaymentID + ":refund_completed")[:24],
		claim.PaymentID, claim.UserID, firstNonEmptyStr(claim.Network, "BSC"), claim.AmountMicro,
		strings.ToLower(receipt.TxHash), claim.RefundID, int64(receipt.BlockNumber), int64(receipt.Confirmations)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *mobilePaymentRefundSQLStore) MarkRefundManualReview(ctx context.Context, claim *mobilePaymentRefundClaim, reason string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='manual_review', last_error=$2, updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`, claim.RefundID, truncateMobilePaymentError(reason))
	return err
}

func (s *mobilePaymentRefundSQLStore) CancelRefund(ctx context.Context, claim *mobilePaymentRefundClaim, reason string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='cancelled', last_error=$2, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed')`, claim.RefundID, truncateMobilePaymentError(reason))
	return err
}

func mobileRefundEligibleForBroadcast(claim *mobilePaymentRefundClaim) bool {
	if claim == nil {
		return false
	}
	intentStatus := strings.ToLower(strings.TrimSpace(claim.IntentStatus))
	execStatus := strings.ToLower(strings.TrimSpace(claim.ExecutionStatus))
	refundStatus := strings.ToLower(strings.TrimSpace(claim.StatusBefore))
	if strings.TrimSpace(claim.FundingTxHash) == "" || strings.TrimSpace(claim.WalletAddress) == "" ||
		strings.TrimSpace(claim.AmountRaw) == "" || claim.AmountMicro <= 0 {
		return false
	}
	if intentStatus != "refund_pending" && !(intentStatus == "manual_review" && strings.TrimSpace(claim.TxHash) != "") {
		return false
	}
	if execStatus != mobilePaymentExecutionStatusFailed {
		return false
	}
	if !mobilePaymentRefundHasDefinitiveProof(claim) {
		return false
	}
	return refundStatus == mobilePaymentRefundStatusPending ||
		refundStatus == mobilePaymentRefundStatusRetryWait ||
		refundStatus == mobilePaymentRefundStatusProviderUnknown ||
		refundStatus == mobilePaymentRefundStatusBroadcast ||
		refundStatus == mobilePaymentRefundStatusConfirming
}

func mobilePaymentRefundHasDefinitiveProof(claim *mobilePaymentRefundClaim) bool {
	if claim == nil {
		return false
	}
	providerStatus := strings.ToUpper(strings.TrimSpace(claim.ProviderStatus))
	if mobilePaymentProviderStatusIsTerminalFailure(providerStatus) {
		return true
	}
	return providerStatus == "DEFINITIVE_PROVIDER_ERROR" ||
		providerStatus == "INVALID_PAYLOAD" ||
		strings.Contains(strings.ToLower(claim.RefundReason), "definitive")
}

func markMobilePaymentFinancialIncident(ctx context.Context, tx *sql.Tx, paymentID, executionID, refundID, reason string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='manual_review', provider_status=$2, error_message=$3, updated_at=NOW()
WHERE id=$1`, paymentID, reason, "incidente financeiro: "+reason); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_refunds
SET status='manual_review', last_error=$2, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed')`, refundID, reason)
	return err
}

type mobilePaymentRefundSignerClient struct {
	cfg    *config.Config
	client *http.Client
}

func newMobilePaymentRefundSignerClient(cfg *config.Config) *mobilePaymentRefundSignerClient {
	return &mobilePaymentRefundSignerClient{cfg: cfg, client: mobileRefundHTTPClient(cfg)}
}

func (c *mobilePaymentRefundSignerClient) BroadcastRefund(ctx context.Context, req mobilePaymentRefundSignerRequest) (mobilePaymentRefundSignerResult, error) {
	if c == nil || c.cfg == nil || strings.TrimSpace(c.cfg.SignerUrl) == "" || strings.TrimSpace(c.cfg.SignerHmacSecret) == "" {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "signer nao configurado para refund"}
	}
	payload := map[string]any{
		"to":             req.To,
		"amount":         req.Amount,
		"tokenContract":  req.TokenContract,
		"network":        firstNonEmptyStr(req.Network, "BSC"),
		"idempotencyKey": req.IdempotencyKey,
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.SignerUrl, "/")+"/hd/transfer", bytes.NewReader(body))
	if err != nil {
		return mobilePaymentRefundSignerResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(httpReq, c.cfg.SignerHmacSecret, body)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return mobilePaymentRefundSignerResult{}, classifyMobileRefundRequestError("signer refund request", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return mobilePaymentRefundSignerResult{}, mobileRefundHTTPError("signer refund", resp, respBody)
	}
	var result mobilePaymentRefundSignerResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer refund response invalida", Err: err}
	}
	if strings.TrimSpace(result.TxHash) == "" {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer refund sem txHash"}
	}
	return result, nil
}

func (c *mobilePaymentRefundSignerClient) ReconcileRefund(ctx context.Context, req mobilePaymentRefundSignerRequest) (mobilePaymentRefundSignerResult, error) {
	if c == nil || c.cfg == nil || strings.TrimSpace(c.cfg.SignerUrl) == "" || strings.TrimSpace(c.cfg.SignerHmacSecret) == "" {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "signer nao configurado para lookup"}
	}
	operationID := firstNonEmptyStr(req.SignerOperationID, req.IdempotencyKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.cfg.SignerUrl, "/")+"/operations/"+url.PathEscape(operationID), nil)
	if err != nil {
		return mobilePaymentRefundSignerResult{}, err
	}
	security.SignRawBodyHeaders(httpReq, c.cfg.SignerHmacSecret, nil)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return mobilePaymentRefundSignerResult{}, classifyMobileRefundRequestError("signer operation lookup", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer operation not found"}
	}
	if resp.StatusCode >= 400 {
		return mobilePaymentRefundSignerResult{}, mobileRefundHTTPError("signer operation lookup", resp, respBody)
	}
	var result struct {
		TxHash      string `json:"tx_hash"`
		From        string `json:"from"`
		Network     string `json:"network"`
		Status      string `json:"status"`
		Recoverable bool   `json:"recoverable"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer operation lookup response invalida", Err: err}
	}
	switch strings.TrimSpace(result.Status) {
	case "signed":
		if result.Recoverable {
			return c.RecoverRefund(ctx, req)
		}
		if strings.TrimSpace(result.TxHash) == "" {
			return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer signed sem tx_hash"}
		}
		return mobilePaymentRefundSignerResult{TxHash: result.TxHash, From: result.From, Network: result.Network, Status: result.Status}, nil
	case "confirmed", "broadcast", "broadcast_unknown":
		if strings.TrimSpace(result.TxHash) == "" {
			return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer operation ambigua sem tx_hash"}
		}
		return mobilePaymentRefundSignerResult{TxHash: result.TxHash, From: result.From, Network: result.Network, Status: result.Status}, nil
	case "failed_before_broadcast":
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorTransient, Message: "signer failed_before_broadcast"}
	default:
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer operation status " + result.Status}
	}
}

func (c *mobilePaymentRefundSignerClient) RecoverRefund(ctx context.Context, req mobilePaymentRefundSignerRequest) (mobilePaymentRefundSignerResult, error) {
	operationID := firstNonEmptyStr(req.SignerOperationID, req.IdempotencyKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.SignerUrl, "/")+"/operations/"+url.PathEscape(operationID)+"/recover", nil)
	if err != nil {
		return mobilePaymentRefundSignerResult{}, err
	}
	security.SignRawBodyHeaders(httpReq, c.cfg.SignerHmacSecret, nil)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return mobilePaymentRefundSignerResult{}, classifyMobileRefundRequestError("signer operation recover", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return mobilePaymentRefundSignerResult{}, mobileRefundHTTPError("signer operation recover", resp, respBody)
	}
	var result mobilePaymentRefundSignerResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer operation recover response invalida", Err: err}
	}
	if strings.TrimSpace(result.TxHash) == "" {
		return mobilePaymentRefundSignerResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer recover sem txHash"}
	}
	return result, nil
}

func (s *Server) VerifyRefundReceipt(ctx context.Context, refund mobilePaymentRefundClaim) (mobilePaymentRefundReceipt, error) {
	txHash := strings.TrimSpace(refund.TxHash)
	if !common.IsHexHash(txHash) {
		return mobilePaymentRefundReceipt{}, fmt.Errorf("refund tx_hash invalido")
	}
	if !common.IsHexAddress(refund.WalletAddress) || !common.IsHexAddress(refund.TreasuryAddress) || !common.IsHexAddress(refund.TokenContract) {
		return mobilePaymentRefundReceipt{}, fmt.Errorf("refund wallet/treasury/token invalido")
	}
	expected, ok := new(big.Int).SetString(strings.TrimSpace(refund.AmountRaw), 10)
	if !ok || expected.Sign() <= 0 {
		return mobilePaymentRefundReceipt{}, fmt.Errorf("refund amount_raw invalido")
	}
	pool := s.evmPool(firstNonEmptyStr(refund.Network, "BSC"))
	if pool == nil {
		return mobilePaymentRefundReceipt{}, fmt.Errorf("RPC %s nao configurado", refund.Network)
	}
	requiredConfirmations := uint64(envInt("MOBILE_PAYMENT_REFUND_CONFIRMATIONS", int(s.mobilePayRequiredConfirmations(refund.Network))))
	var out mobilePaymentRefundReceipt
	err := pool.Do(ctx, func(client *ethclient.Client) error {
		receipt, err := client.TransactionReceipt(ctx, common.HexToHash(txHash))
		if err != nil {
			if errors.Is(err, ethereum.NotFound) {
				return mobilePayFundingPendingError{status: mobilePaymentRefundStatusBroadcast, message: "refund tx ainda nao encontrada"}
			}
			return err
		}
		if receipt.Status != types.ReceiptStatusSuccessful {
			return errMobileRefundReceiptReverted
		}
		latest, err := client.HeaderByNumber(ctx, nil)
		if err != nil {
			return err
		}
		if latest.Number == nil || receipt.BlockNumber == nil || latest.Number.Cmp(receipt.BlockNumber) < 0 {
			return mobilePayFundingPendingError{status: mobilePaymentRefundStatusConfirming, message: "refund aguardando confirmacoes"}
		}
		confirmations := new(big.Int).Sub(latest.Number, receipt.BlockNumber).Uint64() + 1
		if confirmations < requiredConfirmations {
			return mobilePayFundingPendingError{status: mobilePaymentRefundStatusConfirming, message: "refund aguardando confirmacoes"}
		}
		from := common.HexToAddress(refund.TreasuryAddress)
		to := common.HexToAddress(refund.WalletAddress)
		token := common.HexToAddress(refund.TokenContract)
		for _, lg := range receipt.Logs {
			if lg.Address != token || len(lg.Topics) < 3 || lg.Topics[0] != mobilePayERC20TransferTopic {
				continue
			}
			if mobilePayTopicAddress(lg.Topics[1]) != from || mobilePayTopicAddress(lg.Topics[2]) != to {
				continue
			}
			amount := new(big.Int).SetBytes(lg.Data)
			if amount.Cmp(expected) != 0 {
				continue
			}
			out = mobilePaymentRefundReceipt{
				TxHash:        strings.ToLower(txHash),
				BlockNumber:   receipt.BlockNumber.Uint64(),
				BlockHash:     strings.ToLower(receipt.BlockHash.Hex()),
				LogIndex:      int(lg.Index),
				Confirmations: confirmations,
				Status:        receipt.Status,
				From:          strings.ToLower(from.Hex()),
				To:            strings.ToLower(to.Hex()),
				AmountRaw:     amount.String(),
			}
			return nil
		}
		return fmt.Errorf("refund tx nao contem Transfer USDT exato para wallet original")
	})
	return out, err
}

var errMobileRefundReceiptReverted = errors.New("refund tx reverted")

func mobilePayRawToMicro(raw string, decimals int) int64 {
	value, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok || value.Sign() <= 0 {
		return 0
	}
	if decimals <= 6 {
		multiplier := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(6-decimals)), nil)
		value.Mul(value, multiplier)
	} else {
		divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals-6)), nil)
		value.Div(value, divisor)
	}
	if !value.IsInt64() {
		return 0
	}
	return value.Int64()
}

func mobilePayRawToDecimal(raw string, decimals int) string {
	value, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok || value.Sign() <= 0 {
		return "0"
	}
	if decimals <= 0 {
		return value.String()
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole := new(big.Int).Div(new(big.Int).Set(value), scale)
	frac := new(big.Int).Mod(value, scale).String()
	for len(frac) < decimals {
		frac = "0" + frac
	}
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return whole.String()
	}
	return whole.String() + "." + frac
}

func mobileRefundErrorClass(err error) string {
	return mobileProviderErrorClass(err)
}

func mobileRefundRetryAfter(err error) time.Duration {
	return mobileProviderRetryAfter(err)
}

func classifyMobileRefundRequestError(op string, err error) error {
	class := mobilePaymentProviderErrorTransient
	if isMobilePaymentAmbiguousError(err) {
		class = mobilePaymentProviderErrorAmbiguous
	}
	return &mobilePaymentProviderError{Class: class, Message: fmt.Sprintf("%s: %v", op, err), Err: err}
}

func mobileRefundHTTPError(op string, resp *http.Response, body []byte) error {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	class := mobilePaymentProviderErrorTransient
	switch {
	case status == http.StatusTooManyRequests || status >= 500 || status == http.StatusConflict:
		class = mobilePaymentProviderErrorTransient
	case status == http.StatusRequestTimeout:
		class = mobilePaymentProviderErrorAmbiguous
	case status == http.StatusBadRequest || status == http.StatusUnauthorized ||
		status == http.StatusForbidden || status == http.StatusUnprocessableEntity ||
		status == http.StatusLocked:
		class = mobilePaymentProviderErrorDefinitive
	}
	return &mobilePaymentProviderError{
		Class:      class,
		Message:    fmt.Sprintf("%s status %d: %s", op, status, strings.TrimSpace(string(body))),
		RetryAfter: mobileRetryAfter(resp),
	}
}

func mobileRefundHTTPClient(cfg *config.Config) *http.Client {
	return httpclient.Default()
}
