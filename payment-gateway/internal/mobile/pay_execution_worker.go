package mobile

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/certutil"
	"payment-gateway/internal/config"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/metrics"
)

const (
	mobilePaymentExecutionStatusPending         = "pending"
	mobilePaymentExecutionStatusProcessing      = "processing"
	mobilePaymentExecutionStatusRetryWait       = "retry_wait"
	mobilePaymentExecutionStatusProviderPending = "provider_pending"
	mobilePaymentExecutionStatusProviderUnknown = "provider_unknown"
	mobilePaymentExecutionStatusCompleted       = "completed"
	mobilePaymentExecutionStatusFailed          = "failed"
	mobilePaymentExecutionStatusManualReview    = "manual_review"

	mobilePaymentProviderResultCompleted = "completed"
	mobilePaymentProviderResultPending   = "pending"
	mobilePaymentProviderResultFailed    = "failed"
	mobilePaymentProviderResultUnknown   = "unknown"

	mobilePaymentProviderErrorDefinitive = "definitive"
	mobilePaymentProviderErrorTransient  = "transient"
	mobilePaymentProviderErrorAmbiguous  = "ambiguous"
)

type mobilePaymentExecutionWorker struct {
	store             mobilePaymentExecutionStore
	provider          mobilePaymentProvider
	pollEvery         time.Duration
	staleAfter        time.Duration
	maxAttempts       int
	ambiguousGrace    time.Duration
	maxNotFoundChecks int
	now               func() time.Time
}

type mobilePaymentExecutionStore interface {
	RecoverStaleProcessing(ctx context.Context, staleAfter time.Duration) (int64, error)
	ClaimNext(ctx context.Context, maxAttempts int) (*mobilePaymentExecutionClaim, error)
	CompleteExecution(ctx context.Context, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) error
	FailExecutionForRefund(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, result mobilePaymentProviderResult) error
	RetryExecution(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, nextAttemptAt time.Time) error
	MarkProviderUnknown(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, nextAttemptAt time.Time) error
	MarkProviderPending(ctx context.Context, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult, nextAttemptAt time.Time) error
	MarkReconcileNotFound(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, nextAttemptAt time.Time, manualReview bool) error
	MarkManualReview(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string) error
}

type mobilePaymentProvider interface {
	Execute(ctx context.Context, req mobilePaymentProviderRequest) (mobilePaymentProviderResult, error)
	Reconcile(ctx context.Context, req mobilePaymentProviderRequest) (mobilePaymentProviderResult, error)
}

type mobilePaymentExecutionClaim struct {
	ExecutionID            string
	PaymentID              string
	UserID                 string
	WalletAddress          string
	Provider               string
	ProviderIdempotencyKey string
	StatusBefore           string
	IntentStatus           string
	FundingTxHash          string
	FundingNetwork         string
	FundingTokenContract   string
	FundingTokenDecimals   int
	FundingAmountRaw       string
	FundingFromAddress     string
	PaymentType            string
	RawCode                string
	BeneficiaryName        string
	Document               string
	Description            string
	AmountBRL              float64
	RequiredUSDTMic        int64
	Attempt                int
	ReconciliationAttempts int
	ConsecutiveNotFound    int
	FirstAmbiguousAt       sql.NullTime
	Action                 string
	ProviderReference      string
	ProviderTransactionID  string
	ProviderStatus         string
	SubmitOutcome          string
	AmbiguousSubmit        bool
}

type mobilePaymentProviderRequest struct {
	PaymentID              string
	ExecutionID            string
	ProviderIdempotencyKey string
	ProviderReference      string
	ProviderTransactionID  string
	ProviderStatus         string
	PixKey                 string
	AmountBRL              float64
	BeneficiaryName        string
	Document               string
	Description            string
}

type mobilePaymentProviderResult struct {
	Outcome               string
	ProviderReference     string
	ProviderTransactionID string
	ProviderStatus        string
	RetryAfter            time.Duration
	Raw                   map[string]any
}

type mobilePaymentProviderError struct {
	Class      string
	Message    string
	RetryAfter time.Duration
	HTTPStatus int
	Err        error
}

func (e *mobilePaymentProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Class
}

func (e *mobilePaymentProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (s *Server) startMobilePaymentExecutionWorker(ctx context.Context) {
	if s == nil || s.db == nil || s.db.SQL == nil || s.cfg == nil {
		return
	}
	if err := mobileDB(s.db).ensureMobilePaySchema(ctx); err != nil {
		slog.Error("mobile payment execution worker: schema indisponivel", "err", err)
		return
	}
	worker := &mobilePaymentExecutionWorker{
		store:       &mobilePaymentSQLStore{db: s.db.SQL},
		provider:    newMobileEfiPixProvider(s.cfg),
		pollEvery:   time.Duration(envInt("MOBILE_PAYMENT_EXECUTION_POLL_SEC", 5)) * time.Second,
		staleAfter:  time.Duration(envInt("MOBILE_PAYMENT_EXECUTION_STALE_SEC", 120)) * time.Second,
		maxAttempts: envInt("MOBILE_PAYMENT_EXECUTION_MAX_ATTEMPTS", 6),
		ambiguousGrace: time.Duration(envInt("MOBILE_PAYMENT_EFI_AMBIGUOUS_GRACE_SEC", 15*60)) *
			time.Second,
		maxNotFoundChecks: envInt("MOBILE_PAYMENT_EFI_NOT_FOUND_MIN_RECONCILIATIONS", 3),
		now:               time.Now,
	}
	if worker.pollEvery <= 0 {
		worker.pollEvery = 5 * time.Second
	}
	if worker.staleAfter <= 0 {
		worker.staleAfter = 2 * time.Minute
	}
	if worker.maxAttempts <= 0 {
		worker.maxAttempts = 6
	}
	if worker.ambiguousGrace <= 0 {
		worker.ambiguousGrace = 15 * time.Minute
	}
	if worker.maxNotFoundChecks <= 0 {
		worker.maxNotFoundChecks = 3
	}
	worker.Run(ctx)
}

func (w *mobilePaymentExecutionWorker) Run(ctx context.Context) {
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

func (w *mobilePaymentExecutionWorker) runTick(ctx context.Context) {
	if w == nil || w.store == nil || w.provider == nil {
		return
	}
	if recovered, err := w.store.RecoverStaleProcessing(ctx, w.staleAfter); err != nil {
		slog.Error("mobile payment execution stale recovery falhou", "err", err)
	} else if recovered > 0 {
		slog.Warn("mobile payment execution stale recuperado", "count", recovered)
	}
	for i := 0; i < envInt("MOBILE_PAYMENT_EXECUTION_BATCH_SIZE", 5); i++ {
		ok, err := w.runOnce(ctx)
		if err != nil {
			slog.Error("mobile payment execution worker tick falhou", "err", err)
			return
		}
		if !ok {
			return
		}
	}
}

func (w *mobilePaymentExecutionWorker) runOnce(ctx context.Context) (bool, error) {
	claim, err := w.store.ClaimNext(ctx, w.maxAttempts)
	if err != nil || claim == nil {
		return claim != nil, err
	}
	w.processClaim(ctx, claim)
	return true, nil
}

func (w *mobilePaymentExecutionWorker) processClaim(ctx context.Context, claim *mobilePaymentExecutionClaim) {
	start := time.Now()
	stateBefore := claim.StatusBefore
	req, err := claim.providerRequest()
	if err != nil {
		_ = w.store.FailExecutionForRefund(ctx, claim, err.Error(), mobilePaymentProviderResult{Outcome: mobilePaymentProviderResultFailed, ProviderStatus: "invalid_payload"})
		metrics.RecordMobilePaymentExecution("failed")
		slog.Warn("mobile payment execution bloqueado antes do provider",
			"payment_id", claim.PaymentID, "execution_id", claim.ExecutionID, "provider", claim.Provider,
			"attempt", claim.Attempt, "state_before", stateBefore, "state_after", "failed",
			"latency_ms", time.Since(start).Milliseconds(), "error_class", mobilePaymentProviderErrorDefinitive)
		return
	}

	metrics.RecordMobilePaymentExecution("started")
	var result mobilePaymentProviderResult
	if claim.Action == "reconcile" {
		result, err = w.provider.Reconcile(ctx, req)
	} else {
		result, err = w.provider.Execute(ctx, req)
	}
	latency := time.Since(start)
	if err != nil {
		w.applyProviderError(ctx, claim, err)
		slog.Warn("mobile payment execution provider erro",
			"payment_id", claim.PaymentID, "execution_id", claim.ExecutionID, "provider", claim.Provider,
			"attempt", claim.Attempt, "state_before", stateBefore, "action", claim.Action,
			"latency_ms", latency.Milliseconds(), "error_class", mobileProviderErrorClass(err))
		return
	}
	w.applyProviderResult(ctx, claim, result)
	slog.Info("mobile payment execution provider resultado",
		"payment_id", claim.PaymentID, "execution_id", claim.ExecutionID, "provider", claim.Provider,
		"attempt", claim.Attempt, "state_before", stateBefore, "state_after", result.Outcome,
		"provider_reference", result.ProviderReference, "latency_ms", latency.Milliseconds())
}

func (w *mobilePaymentExecutionWorker) applyProviderError(ctx context.Context, claim *mobilePaymentExecutionClaim, err error) {
	class := mobileProviderErrorClass(err)
	retryAfter := mobileProviderRetryAfter(err)
	if retryAfter <= 0 {
		retryAfter = w.backoff(claim.Attempt)
	}
	switch class {
	case mobilePaymentProviderErrorDefinitive:
		if claim.Action == "reconcile" && mobileProviderErrorHTTPStatus(err) == http.StatusNotFound {
			w.handleReconcileNotFound(ctx, claim, err, retryAfter)
			return
		}
		if claim.Action == "reconcile" {
			_ = w.store.MarkManualReview(ctx, claim, "efi_reconcile_definitive_error_without_terminal_status: "+err.Error())
			metrics.RecordMobilePaymentExecution("provider_unknown")
			metrics.RecordEfiReconcile("manual_review")
			return
		}
		result := mobilePaymentProviderResult{Outcome: mobilePaymentProviderResultFailed, ProviderStatus: "definitive_provider_error"}
		if mobilePaymentCanCreateRefund(claim, result) {
			_ = w.store.FailExecutionForRefund(ctx, claim, err.Error(), result)
			metrics.RecordMobilePaymentExecution("failed")
		} else {
			_ = w.store.MarkManualReview(ctx, claim, "refund_blocked_no_terminal_proof: "+err.Error())
			metrics.RecordEfiReconcile("refund_blocked_ambiguous")
		}
	case mobilePaymentProviderErrorAmbiguous:
		if claim.Attempt >= w.maxAttempts {
			_ = w.store.MarkManualReview(ctx, claim, err.Error())
		} else {
			_ = w.store.MarkProviderUnknown(ctx, claim, err.Error(), w.now().UTC().Add(retryAfter))
			metrics.RecordMobilePaymentExecution("provider_unknown")
		}
	default:
		if claim.Attempt >= w.maxAttempts {
			_ = w.store.MarkManualReview(ctx, claim, "retryable_provider_error_max_attempts_without_terminal_status: "+err.Error())
			metrics.RecordMobilePaymentExecution("provider_unknown")
		} else {
			_ = w.store.RetryExecution(ctx, claim, err.Error(), w.now().UTC().Add(retryAfter))
			metrics.RecordMobilePaymentExecution("retry")
		}
	}
}

func (w *mobilePaymentExecutionWorker) applyProviderResult(ctx context.Context, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) {
	retryAfter := result.RetryAfter
	if retryAfter <= 0 {
		retryAfter = w.backoff(claim.Attempt)
	}
	switch result.Outcome {
	case mobilePaymentProviderResultCompleted:
		_ = w.store.CompleteExecution(ctx, claim, result)
		metrics.RecordMobilePaymentExecution("completed")
		if claim.Action == "reconcile" {
			metrics.RecordMobilePaymentExecution("reconciled")
		}
	case mobilePaymentProviderResultFailed:
		if mobilePaymentCanCreateRefund(claim, result) {
			_ = w.store.FailExecutionForRefund(ctx, claim, "provider_rejected", result)
			metrics.RecordMobilePaymentExecution("failed")
			metrics.RecordEfiReconcile("definitive_failure")
			if claim.Action == "reconcile" {
				metrics.RecordMobilePaymentExecution("reconciled")
			}
		} else {
			_ = w.store.MarkManualReview(ctx, claim, "provider_failed_without_terminal_failure_status")
			metrics.RecordMobilePaymentExecution("provider_unknown")
			metrics.RecordEfiReconcile("refund_blocked_ambiguous")
		}
	case mobilePaymentProviderResultPending:
		if claim.Attempt >= w.maxAttempts {
			_ = w.store.MarkManualReview(ctx, claim, "provider_pending_max_attempts")
		} else {
			_ = w.store.MarkProviderPending(ctx, claim, result, w.now().UTC().Add(retryAfter))
		}
	default:
		if claim.Attempt >= w.maxAttempts {
			_ = w.store.MarkManualReview(ctx, claim, "provider_unknown_max_attempts")
		} else {
			_ = w.store.MarkProviderUnknown(ctx, claim, "provider_unknown", w.now().UTC().Add(retryAfter))
			metrics.RecordMobilePaymentExecution("provider_unknown")
		}
	}
}

func (w *mobilePaymentExecutionWorker) handleReconcileNotFound(ctx context.Context, claim *mobilePaymentExecutionClaim, err error, retryAfter time.Duration) {
	if retryAfter <= 0 {
		retryAfter = w.backoff(claim.ReconciliationAttempts + 1)
	}
	now := w.now().UTC()
	first := now
	if claim.FirstAmbiguousAt.Valid {
		first = claim.FirstAmbiguousAt.Time.UTC()
	}
	nextNotFound := claim.ConsecutiveNotFound + 1
	manual := nextNotFound >= w.maxNotFoundChecks && !first.IsZero() && now.Sub(first) >= w.ambiguousGrace
	reason := "efi_reconcile_not_found_unconfirmed: " + err.Error()
	if manual {
		reason = "efi_reconcile_manual_review_after_persistent_not_found: " + err.Error()
	}
	_ = w.store.MarkReconcileNotFound(ctx, claim, reason, now.Add(retryAfter), manual)
	if manual {
		metrics.RecordMobilePaymentExecution("provider_unknown")
		metrics.RecordEfiReconcile("manual_review")
		slog.Warn("mobile payment efi reconcile manual_review apos ausencia persistente",
			"payment_id", claim.PaymentID, "execution_id", claim.ExecutionID,
			"provider_idempotency_key", claim.ProviderIdempotencyKey,
			"reconciliation_attempt", claim.ReconciliationAttempts+1,
			"first_ambiguous_at", first, "http_status", http.StatusNotFound,
			"state_before", claim.StatusBefore, "state_after", mobilePaymentExecutionStatusManualReview)
		return
	}
	metrics.RecordMobilePaymentExecution("provider_unknown")
	metrics.RecordEfiReconcile("not_found")
	metrics.RecordEfiReconcile("ambiguous")
	slog.Warn("mobile payment efi reconcile not_found nao confirmado",
		"payment_id", claim.PaymentID, "execution_id", claim.ExecutionID,
		"provider_idempotency_key", claim.ProviderIdempotencyKey,
		"reconciliation_attempt", claim.ReconciliationAttempts+1,
		"first_ambiguous_at", first, "http_status", http.StatusNotFound,
		"state_before", claim.StatusBefore, "state_after", mobilePaymentExecutionStatusProviderUnknown)
}

func (w *mobilePaymentExecutionWorker) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	seconds := 30 * math.Pow(2, float64(attempt-1))
	if seconds > 300 {
		seconds = 300
	}
	return time.Duration(seconds) * time.Second
}

func (c *mobilePaymentExecutionClaim) providerRequest() (mobilePaymentProviderRequest, error) {
	if c == nil {
		return mobilePaymentProviderRequest{}, fmt.Errorf("execution claim vazio")
	}
	if strings.ToLower(strings.TrimSpace(c.Provider)) != "efi" {
		return mobilePaymentProviderRequest{}, fmt.Errorf("provider nao suportado: %s", c.Provider)
	}
	if strings.ToLower(strings.TrimSpace(c.PaymentType)) != "pix" {
		return mobilePaymentProviderRequest{}, fmt.Errorf("tipo de QR nao suportado para execucao automatica: %s", c.PaymentType)
	}
	pixKey := mobilePixKeyFromBRCode(c.RawCode)
	if pixKey == "" {
		return mobilePaymentProviderRequest{}, fmt.Errorf("chave Pix nao encontrada no BR Code")
	}
	if c.AmountBRL <= 0 {
		return mobilePaymentProviderRequest{}, fmt.Errorf("amount_brl persistido invalido")
	}
	return mobilePaymentProviderRequest{
		PaymentID:              c.PaymentID,
		ExecutionID:            c.ExecutionID,
		ProviderIdempotencyKey: c.ProviderIdempotencyKey,
		ProviderReference:      c.ProviderReference,
		ProviderTransactionID:  c.ProviderTransactionID,
		ProviderStatus:         c.ProviderStatus,
		PixKey:                 pixKey,
		AmountBRL:              c.AmountBRL,
		BeneficiaryName:        c.BeneficiaryName,
		Document:               c.Document,
		Description:            c.Description,
	}, nil
}

type mobilePaymentSQLStore struct {
	db *sql.DB
}

func (s *mobilePaymentSQLStore) RecoverStaleProcessing(ctx context.Context, staleAfter time.Duration) (int64, error) {
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='provider_unknown',
    error_message='processing stale; enviado para reconciliation',
    next_attempt_at=NOW(),
    updated_at=NOW()
WHERE status='processing'
  AND COALESCE(last_attempt_at, started_at, updated_at) <= NOW() - ($1::bigint * interval '1 millisecond')`,
		staleAfter.Milliseconds())
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}

func (s *mobilePaymentSQLStore) ClaimNext(ctx context.Context, maxAttempts int) (*mobilePaymentExecutionClaim, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	claim := &mobilePaymentExecutionClaim{}
	err = tx.QueryRowContext(ctx, `
SELECT e.id, e.payment_intent_id, e.user_id::text, e.provider, e.provider_idempotency_key,
       e.status, e.attempt_count, e.provider_reference, e.provider_transaction_id, e.provider_status,
       e.reconciliation_attempt_count, e.consecutive_not_found, e.first_ambiguous_at,
       e.submit_outcome, e.ambiguous_submit,
       i.status, i.wallet_address, i.funding_tx_hash, i.funding_network, i.funding_token_contract,
       i.funding_token_decimals, i.funding_amount_raw, COALESCE(f.from_address, ''),
       i.payment_type, i.raw_code, i.beneficiary_name, i.document, i.description,
       i.amount_brl::float8, i.required_usdt_micro
FROM mobile_payment_executions e
JOIN mobile_payment_intents i ON i.id=e.payment_intent_id
LEFT JOIN mobile_payment_funding_transactions f ON lower(f.tx_hash)=lower(i.funding_tx_hash)
WHERE e.status IN ('pending','retry_wait','provider_pending','provider_unknown')
  AND e.next_attempt_at <= NOW()
ORDER BY e.created_at
LIMIT 1
FOR UPDATE OF e, i SKIP LOCKED`).Scan(
		&claim.ExecutionID, &claim.PaymentID, &claim.UserID, &claim.Provider, &claim.ProviderIdempotencyKey,
		&claim.StatusBefore, &claim.Attempt, &claim.ProviderReference, &claim.ProviderTransactionID, &claim.ProviderStatus,
		&claim.ReconciliationAttempts, &claim.ConsecutiveNotFound, &claim.FirstAmbiguousAt,
		&claim.SubmitOutcome, &claim.AmbiguousSubmit,
		&claim.IntentStatus, &claim.WalletAddress, &claim.FundingTxHash, &claim.FundingNetwork,
		&claim.FundingTokenContract, &claim.FundingTokenDecimals, &claim.FundingAmountRaw, &claim.FundingFromAddress,
		&claim.PaymentType, &claim.RawCode,
		&claim.BeneficiaryName, &claim.Document, &claim.Description, &claim.AmountBRL, &claim.RequiredUSDTMic)
	if err == sql.ErrNoRows {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(claim.ProviderIdempotencyKey) == "" {
		claim.ProviderIdempotencyKey = "mpay-efi-" + mobilePayHash(claim.PaymentID)[:24]
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET provider_idempotency_key=$2, updated_at=NOW()
WHERE id=$1 AND provider_idempotency_key=''`, claim.ExecutionID, claim.ProviderIdempotencyKey); err != nil {
			return nil, err
		}
	}
	if !mobilePaymentIntentFundedForExecution(claim) {
		_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='failed', error_message='intent sem funding_confirmed; provider bloqueado', failed_at=NOW(), updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`, claim.ExecutionID)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if strings.TrimSpace(claim.Provider) != "efi" {
		_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='manual_review', error_message='provider invalido para mobile QR Pay', updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`, claim.ExecutionID)
		if err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if claim.Attempt >= maxAttempts {
		_, err = tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='manual_review', error_message='max attempts atingido antes do claim', updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','failed')`, claim.ExecutionID)
		if err != nil {
			return nil, err
		}
		_, _ = tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='manual_review', provider_status='execution_max_attempts', error_message='mobile QR Pay execution max attempts', updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed')`, claim.PaymentID)
		return nil, tx.Commit()
	}
	claim.Action = "execute"
	if claim.StatusBefore == mobilePaymentExecutionStatusProviderPending || claim.StatusBefore == mobilePaymentExecutionStatusProviderUnknown {
		claim.Action = "reconcile"
	}
	err = tx.QueryRowContext(ctx, `
UPDATE mobile_payment_executions
SET status='processing',
    attempt_count=attempt_count+1,
    started_at=COALESCE(started_at, NOW()),
    last_attempt_at=NOW(),
    submit_started_at=CASE WHEN $3='execute' THEN COALESCE(submit_started_at, NOW()) ELSE submit_started_at END,
    submit_outcome=CASE WHEN $3='execute' THEN 'submit_started' ELSE submit_outcome END,
    error_message='',
    updated_at=NOW()
WHERE id=$1 AND status=$2 AND status <> 'completed'
RETURNING attempt_count`, claim.ExecutionID, claim.StatusBefore, claim.Action).Scan(&claim.Attempt)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='processing', provider_status=$2, updated_at=NOW()
WHERE id=$1 AND status IN ('funding_confirmed','provider_pending','provider_unknown','processing')`,
		claim.PaymentID, "efi_"+claim.Action); err != nil {
		return nil, err
	}
	return claim, tx.Commit()
}

func (s *mobilePaymentSQLStore) CompleteExecution(ctx context.Context, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return s.completeExecutionTx(ctx, tx, claim, result)
	})
}

func (s *mobilePaymentSQLStore) completeExecutionTx(ctx context.Context, tx *sql.Tx, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='completed',
    provider_reference=$2,
    provider_transaction_id=$3,
    provider_status=$4,
    submit_completed_at=COALESCE(submit_completed_at, NOW()),
    submit_outcome='submit_confirmed',
    ambiguous_submit=FALSE,
    consecutive_not_found=0,
    completed_at=NOW(),
    updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`,
		claim.ExecutionID, firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
		firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID),
		firstNonEmptyStr(result.ProviderStatus, claim.ProviderStatus, "confirmed")); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='completed',
    provider_status=$2,
    provider_reference=$3,
    provider_transaction_id=$4,
    completed_at=COALESCE(completed_at, NOW()),
    updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`,
		claim.PaymentID, firstNonEmptyStr(result.ProviderStatus, "confirmed"),
		firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
		firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID)); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO mobile_payment_ledger_entries
  (id, payment_intent_id, user_id, entry_type, asset, network, amount_micro, tx_hash, provider, provider_reference, metadata)
VALUES ($1,$2,$3::uuid,'provider_completed','USDT','BSC',$4,$5,'efi',$6,
        jsonb_build_object('provider_status',$7,'provider_transaction_id',$8))
ON CONFLICT (payment_intent_id, entry_type) DO NOTHING`,
		"mpledger_"+mobilePayHash(claim.PaymentID + ":provider_completed")[:24],
		claim.PaymentID, claim.UserID, claim.RequiredUSDTMic, claim.FundingTxHash,
		firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
		firstNonEmptyStr(result.ProviderStatus, "confirmed"),
		firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID))
	return err
}

func (s *mobilePaymentSQLStore) FailExecutionForRefund(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, result mobilePaymentProviderResult) error {
	reason = truncateMobilePaymentError(reason)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return s.failExecutionForRefundTx(ctx, tx, claim, reason, result)
	})
}

func (s *mobilePaymentSQLStore) failExecutionForRefundTx(ctx context.Context, tx *sql.Tx, claim *mobilePaymentExecutionClaim, reason string, result mobilePaymentProviderResult) error {
	reason = truncateMobilePaymentError(reason)
	if !mobilePaymentCanCreateRefund(claim, result) {
		return s.markManualReviewTx(ctx, tx, claim, "refund_blocked_no_terminal_provider_failure: "+reason)
	}
	refundAmountMic := mobilePayRawToMicro(claim.FundingAmountRaw, claim.FundingTokenDecimals)
	if refundAmountMic <= 0 {
		refundAmountMic = claim.RequiredUSDTMic
	}
	refundWallet := strings.TrimSpace(claim.FundingFromAddress)
	if refundWallet == "" {
		return s.markManualReviewTx(ctx, tx, claim, "refund_blocked_missing_funding_sender")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='failed',
    provider_reference=$2,
    provider_transaction_id=$3,
    provider_status=$4,
    error_message=$5,
    submit_completed_at=COALESCE(submit_completed_at, NOW()),
    submit_outcome='definitive_failed',
    ambiguous_submit=FALSE,
    failed_at=NOW(),
    updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`,
		claim.ExecutionID, firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
		firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID),
		firstNonEmptyStr(result.ProviderStatus, "failed"), reason); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='refund_pending',
    provider_status=$2,
    provider_reference=$3,
    provider_transaction_id=$4,
    error_message=$5,
    failed_at=COALESCE(failed_at, NOW()),
    refund_reason=$6,
    refund_amount_micro=$7,
    refund_wallet_address=$8,
    updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','refunded')`,
		claim.PaymentID, firstNonEmptyStr(result.ProviderStatus, "provider_failed"),
		firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
		firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID),
		reason, reason, refundAmountMic, refundWallet); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO mobile_payment_ledger_entries
  (id, payment_intent_id, user_id, entry_type, asset, network, amount_micro, tx_hash, provider, provider_reference, metadata)
VALUES ($1,$2,$3::uuid,'refund_pending','USDT','BSC',$4,$5,'efi',$6,
        jsonb_build_object('reason',$7,'refund_wallet_address',$8))
ON CONFLICT (payment_intent_id, entry_type) DO NOTHING`,
		"mpledger_"+mobilePayHash(claim.PaymentID + ":refund_pending")[:24],
		claim.PaymentID, claim.UserID, refundAmountMic, claim.FundingTxHash,
		firstNonEmptyStr(result.ProviderReference, claim.ProviderReference), reason, refundWallet)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO mobile_payment_refunds
  (id, payment_id, execution_id, user_id, wallet_address, asset, network, token_contract,
   token_decimals, amount_micro, amount_raw, status, refund_reason, idempotency_key,
   signer_operation_id, next_attempt_at)
VALUES ($1,$2,$3,$4::uuid,$5,'USDT',$6,$7,$8,$9,$10,'pending',$11,$12,$13,NOW())
ON CONFLICT (payment_id) DO NOTHING`,
		"mprefund_"+mobilePayHash(claim.PaymentID)[:24],
		claim.PaymentID, claim.ExecutionID, claim.UserID, refundWallet,
		firstNonEmptyStr(claim.FundingNetwork, "BSC"),
		claim.FundingTokenContract, claim.FundingTokenDecimals, refundAmountMic,
		claim.FundingAmountRaw, reason, "refund:"+claim.PaymentID, "refund:"+claim.PaymentID)
	return err
}

func (s *mobilePaymentSQLStore) RetryExecution(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, nextAttemptAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='retry_wait', error_message=$2, next_attempt_at=$3, updated_at=NOW()
WHERE id=$1 AND status='processing'`, claim.ExecutionID, truncateMobilePaymentError(reason), nextAttemptAt)
	return err
}

func (s *mobilePaymentSQLStore) MarkProviderUnknown(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, nextAttemptAt time.Time) error {
	return s.markProviderWaiting(ctx, claim, mobilePaymentExecutionStatusProviderUnknown, "provider_unknown", reason, mobilePaymentProviderResult{}, nextAttemptAt)
}

func (s *mobilePaymentSQLStore) MarkProviderPending(ctx context.Context, claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult, nextAttemptAt time.Time) error {
	return s.markProviderWaiting(ctx, claim, mobilePaymentExecutionStatusProviderPending, firstNonEmptyStr(result.ProviderStatus, "provider_pending"), "provider_pending", result, nextAttemptAt)
}

func (s *mobilePaymentSQLStore) MarkReconcileNotFound(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string, nextAttemptAt time.Time, manualReview bool) error {
	reason = truncateMobilePaymentError(reason)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		execStatus := mobilePaymentExecutionStatusProviderUnknown
		intentStatus := "provider_pending"
		intentProviderStatus := "efi_not_found_unconfirmed"
		if manualReview {
			execStatus = mobilePaymentExecutionStatusManualReview
			intentStatus = "manual_review"
			intentProviderStatus = "efi_reconcile_manual_review"
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status=$2,
    provider_status='not_found_unconfirmed',
    error_message=$3,
    next_attempt_at=$4,
    reconciliation_attempt_count=reconciliation_attempt_count+1,
    first_ambiguous_at=COALESCE(first_ambiguous_at, NOW()),
    last_reconciled_at=NOW(),
    consecutive_not_found=consecutive_not_found+1,
    ambiguous_submit=TRUE,
    updated_at=NOW()
WHERE id=$1 AND status='processing'`, claim.ExecutionID, execStatus, reason, nextAttemptAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status=$2, provider_status=$3, error_message=$4, updated_at=NOW()
WHERE id=$1 AND status NOT IN ('completed','refunded')`,
			claim.PaymentID, intentStatus, intentProviderStatus, reason)
		return err
	})
}

func (s *mobilePaymentSQLStore) MarkManualReview(ctx context.Context, claim *mobilePaymentExecutionClaim, reason string) error {
	reason = truncateMobilePaymentError(reason)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return s.markManualReviewTx(ctx, tx, claim, reason)
	})
}

func (s *mobilePaymentSQLStore) markManualReviewTx(ctx context.Context, tx *sql.Tx, claim *mobilePaymentExecutionClaim, reason string) error {
	reason = truncateMobilePaymentError(reason)
	if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status='manual_review', error_message=$2, updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`, claim.ExecutionID, reason); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='manual_review', provider_status='execution_manual_review', error_message=$2, updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`, claim.PaymentID, reason)
	return err
}

func (s *mobilePaymentSQLStore) markProviderWaiting(ctx context.Context, claim *mobilePaymentExecutionClaim, execStatus, intentProviderStatus, reason string, result mobilePaymentProviderResult, nextAttemptAt time.Time) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_executions
SET status=$2,
    provider_reference=$3,
    provider_transaction_id=$4,
    provider_status=$5,
    error_message=$6,
    next_attempt_at=$7,
    reconciliation_attempt_count=CASE WHEN $2 IN ('provider_unknown','provider_pending') THEN reconciliation_attempt_count + 1 ELSE reconciliation_attempt_count END,
    first_ambiguous_at=CASE WHEN $2='provider_unknown' THEN COALESCE(first_ambiguous_at, NOW()) ELSE first_ambiguous_at END,
    last_reconciled_at=CASE WHEN $2 IN ('provider_unknown','provider_pending') THEN NOW() ELSE last_reconciled_at END,
    consecutive_not_found=CASE WHEN $5='not_found_unconfirmed' THEN consecutive_not_found + 1 ELSE 0 END,
    ambiguous_submit=CASE WHEN $2='provider_unknown' THEN TRUE ELSE ambiguous_submit END,
    updated_at=NOW()
WHERE id=$1 AND status='processing'`,
			claim.ExecutionID, execStatus, firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
			firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID),
			firstNonEmptyStr(result.ProviderStatus, claim.ProviderStatus),
			truncateMobilePaymentError(reason), nextAttemptAt); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
UPDATE mobile_payment_intents
SET status='provider_pending',
    provider_status=$2,
    provider_reference=$3,
    provider_transaction_id=$4,
    error_message=$5,
    updated_at=NOW()
WHERE id=$1 AND status <> 'completed'`,
			claim.PaymentID, intentProviderStatus,
			firstNonEmptyStr(result.ProviderReference, claim.ProviderReference),
			firstNonEmptyStr(result.ProviderTransactionID, claim.ProviderTransactionID),
			truncateMobilePaymentError(reason))
		return err
	})
}

func (s *mobilePaymentSQLStore) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func mobilePaymentIntentFundedForExecution(claim *mobilePaymentExecutionClaim) bool {
	if claim == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(claim.IntentStatus))
	return strings.TrimSpace(claim.FundingTxHash) != "" &&
		(status == "funding_confirmed" || status == "provider_pending" || status == "provider_unknown" || status == "processing")
}

func mobileProviderErrorClass(err error) string {
	var providerErr *mobilePaymentProviderError
	if errors.As(err, &providerErr) && providerErr.Class != "" {
		return providerErr.Class
	}
	if isMobilePaymentAmbiguousError(err) {
		return mobilePaymentProviderErrorAmbiguous
	}
	return mobilePaymentProviderErrorTransient
}

func mobileProviderRetryAfter(err error) time.Duration {
	var providerErr *mobilePaymentProviderError
	if errors.As(err, &providerErr) {
		return providerErr.RetryAfter
	}
	return 0
}

func mobileProviderErrorHTTPStatus(err error) int {
	var providerErr *mobilePaymentProviderError
	if errors.As(err, &providerErr) {
		return providerErr.HTTPStatus
	}
	return 0
}

func mobilePaymentCanCreateRefund(claim *mobilePaymentExecutionClaim, result mobilePaymentProviderResult) bool {
	if claim == nil {
		return false
	}
	if strings.TrimSpace(claim.FundingTxHash) == "" ||
		strings.TrimSpace(claim.FundingAmountRaw) == "" ||
		strings.TrimSpace(claim.FundingFromAddress) == "" {
		return false
	}
	statusBefore := strings.ToLower(strings.TrimSpace(claim.StatusBefore))
	action := strings.ToLower(strings.TrimSpace(claim.Action))
	providerStatus := strings.ToUpper(strings.TrimSpace(result.ProviderStatus))
	terminalFailure := mobilePaymentProviderStatusIsTerminalFailure(providerStatus)
	if statusBefore == mobilePaymentExecutionStatusCompleted ||
		statusBefore == mobilePaymentExecutionStatusManualReview ||
		((statusBefore == mobilePaymentExecutionStatusProviderUnknown ||
			statusBefore == mobilePaymentExecutionStatusProviderPending) && !terminalFailure) {
		return false
	}
	if result.Outcome == mobilePaymentProviderResultFailed && terminalFailure {
		return true
	}
	if action == "execute" && result.Outcome == mobilePaymentProviderResultFailed {
		return providerStatus == "DEFINITIVE_PROVIDER_ERROR" || providerStatus == "INVALID_PAYLOAD"
	}
	return false
}

func mobilePaymentProviderStatusIsTerminalFailure(status string) bool {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "" || status == "NOT_FOUND_UNCONFIRMED" {
		return false
	}
	return strings.Contains(status, "REJEIT") ||
		strings.Contains(status, "REJECT") ||
		strings.Contains(status, "CANCEL") ||
		strings.Contains(status, "FALH") ||
		strings.Contains(status, "FAILED") ||
		strings.Contains(status, "DENIED") ||
		strings.Contains(status, "NEGAD") ||
		strings.Contains(status, "RECUS") ||
		strings.Contains(status, "DEVOLV")
}

func truncateMobilePaymentError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 500 {
		return value
	}
	return value[:500]
}

func mobilePixKeyFromBRCode(raw string) string {
	merchantAccount := emvTag(strings.TrimSpace(raw), "26")
	if merchantAccount == "" {
		return ""
	}
	return strings.TrimSpace(emvTag(merchantAccount, "01"))
}

type mobileEfiPixProvider struct {
	cfg    *config.Config
	client *http.Client
}

func newMobileEfiPixProvider(cfg *config.Config) *mobileEfiPixProvider {
	return &mobileEfiPixProvider{cfg: cfg, client: mobileEfiHTTPClient(cfg)}
}

func (p *mobileEfiPixProvider) Execute(ctx context.Context, req mobilePaymentProviderRequest) (mobilePaymentProviderResult, error) {
	if err := p.validate(req); err != nil {
		return mobilePaymentProviderResult{}, err
	}
	token, err := p.getToken(ctx)
	if err != nil {
		return mobilePaymentProviderResult{}, err
	}
	return p.doPixSend(ctx, req, token, true)
}

func (p *mobileEfiPixProvider) Reconcile(ctx context.Context, req mobilePaymentProviderRequest) (mobilePaymentProviderResult, error) {
	if err := p.validate(req); err != nil {
		return mobilePaymentProviderResult{}, err
	}
	token, err := p.getToken(ctx)
	if err != nil {
		return mobilePaymentProviderResult{}, err
	}
	idEnvio := firstNonEmptyStr(req.ProviderTransactionID, req.ProviderIdempotencyKey)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(p.cfg.EfiApiBaseURL, "/")+"/v2/gn/pix/enviados/id-envio/"+idEnvio, nil)
	if err != nil {
		return mobilePaymentProviderResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.client.Do(request)
	if err != nil {
		return mobilePaymentProviderResult{}, classifyMobileEfiRequestError("efi pix sent lookup", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 400 {
		return mobilePaymentProviderResult{}, mobileEfiHTTPError("efi pix sent lookup", resp, body)
	}
	result, err := parseMobileEfiPixSendResult(body)
	if err != nil {
		return mobilePaymentProviderResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: err.Error(), Err: err}
	}
	result.ProviderTransactionID = firstNonEmptyStr(result.ProviderTransactionID, idEnvio)
	return result, nil
}

func (p *mobileEfiPixProvider) validate(req mobilePaymentProviderRequest) error {
	if p == nil || p.cfg == nil || strings.TrimSpace(p.cfg.EfiClientID) == "" ||
		strings.TrimSpace(p.cfg.EfiClientSecret) == "" || strings.TrimSpace(p.cfg.EfiPixKey) == "" {
		return &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "Efí Pix Send nao configurado"}
	}
	if strings.TrimSpace(req.ProviderIdempotencyKey) == "" {
		return &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "provider_idempotency_key ausente"}
	}
	if strings.TrimSpace(req.PixKey) == "" {
		return &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "pix key missing"}
	}
	if req.AmountBRL <= 0 {
		return &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "amount_brl invalido"}
	}
	return nil
}

func (p *mobileEfiPixProvider) doPixSend(ctx context.Context, req mobilePaymentProviderRequest, token string, retryAuth bool) (mobilePaymentProviderResult, error) {
	payload := buildMobileEfiPixSendPayload(p.cfg.EfiPixKey, req)
	body, _ := json.Marshal(payload)
	idEnvio := req.ProviderIdempotencyKey
	request, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimRight(p.cfg.EfiApiBaseURL, "/")+"/v3/gn/pix/"+idEnvio, bytes.NewReader(body))
	if err != nil {
		return mobilePaymentProviderResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.client.Do(request)
	if err != nil {
		return mobilePaymentProviderResult{}, classifyMobileEfiRequestError("efi pix send request", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode == http.StatusUnauthorized && retryAuth {
		token, tokenErr := p.getToken(ctx)
		if tokenErr != nil {
			return mobilePaymentProviderResult{}, tokenErr
		}
		return p.doPixSend(ctx, req, token, false)
	}
	if resp.StatusCode >= 400 {
		return mobilePaymentProviderResult{}, mobileEfiHTTPError("efi pix send", resp, respBody)
	}
	result, err := parseMobileEfiPixSendResult(respBody)
	if err != nil {
		return mobilePaymentProviderResult{}, &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: err.Error(), Err: err}
	}
	result.ProviderTransactionID = firstNonEmptyStr(result.ProviderTransactionID, idEnvio)
	return result, nil
}

func (p *mobileEfiPixProvider) getToken(ctx context.Context) (string, error) {
	raw := []byte(`{"grant_type":"client_credentials"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(p.cfg.EfiApiBaseURL, "/")+"/oauth/token", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(p.cfg.EfiClientID, p.cfg.EfiClientSecret)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", classifyMobileEfiRequestError("efi token request", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", mobileEfiHTTPError("efi token", resp, body)
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "efi token response invalida", Err: err}
	}
	if strings.TrimSpace(result.AccessToken) == "" {
		return "", &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "efi access_token vazio"}
	}
	return result.AccessToken, nil
}

func buildMobileEfiPixSendPayload(payerPixKey string, req mobilePaymentProviderRequest) map[string]any {
	payload := map[string]any{
		"valor": fmt.Sprintf("%.2f", req.AmountBRL),
		"pagador": map[string]any{
			"chave":       payerPixKey,
			"infoPagador": fmt.Sprintf("ChainFX Mobile %s", req.PaymentID),
		},
		"favorecido": map[string]any{
			"chave": req.PixKey,
		},
	}
	if doc := onlyDigitsMobile(req.Document); doc != "" {
		payload["favorecido"].(map[string]any)["cpf"] = doc
	}
	return payload
}

func parseMobileEfiPixSendResult(raw []byte) (mobilePaymentProviderResult, error) {
	var result struct {
		IDEnvio    string `json:"idEnvio"`
		E2EID      string `json:"e2eId"`
		EndToEndID string `json:"endToEndId"`
		Status     string `json:"status"`
		Valor      string `json:"valor"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return mobilePaymentProviderResult{}, fmt.Errorf("efi pix send response parse: %w", err)
	}
	ref := firstNonEmptyStr(result.E2EID, result.EndToEndID, result.IDEnvio)
	if ref == "" {
		return mobilePaymentProviderResult{}, fmt.Errorf("efi pix send: provider reference vazio")
	}
	status := strings.ToUpper(strings.TrimSpace(firstNonEmptyStr(result.Status, "SUBMITTED")))
	outcome := mobilePaymentProviderResultPending
	switch {
	case strings.Contains(status, "REALIZ") || strings.Contains(status, "CONCL") || strings.Contains(status, "CONFIRM") || strings.Contains(status, "EFETIV"):
		outcome = mobilePaymentProviderResultCompleted
	case strings.Contains(status, "REJEIT") || strings.Contains(status, "CANCEL") || strings.Contains(status, "FALH") || strings.Contains(status, "DEVOLV"):
		outcome = mobilePaymentProviderResultFailed
	}
	return mobilePaymentProviderResult{
		Outcome:               outcome,
		ProviderReference:     ref,
		ProviderTransactionID: firstNonEmptyStr(result.IDEnvio, ref),
		ProviderStatus:        status,
		Raw: map[string]any{
			"idEnvio": result.IDEnvio,
			"e2eId":   firstNonEmptyStr(result.E2EID, result.EndToEndID),
			"status":  status,
			"valor":   result.Valor,
		},
	}, nil
}

func mobileEfiHTTPError(op string, resp *http.Response, body []byte) error {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	class := mobilePaymentProviderErrorTransient
	switch {
	case status == http.StatusTooManyRequests || status >= 500:
		class = mobilePaymentProviderErrorTransient
	case status == http.StatusRequestTimeout:
		class = mobilePaymentProviderErrorAmbiguous
	case status == http.StatusBadRequest || status == http.StatusUnauthorized ||
		status == http.StatusForbidden || status == http.StatusUnprocessableEntity:
		class = mobilePaymentProviderErrorDefinitive
	case status == http.StatusNotFound:
		class = mobilePaymentProviderErrorDefinitive
	}
	return &mobilePaymentProviderError{
		Class:      class,
		Message:    fmt.Sprintf("%s status %d: %s", op, status, strings.TrimSpace(string(body))),
		RetryAfter: mobileRetryAfter(resp),
		HTTPStatus: status,
	}
}

func classifyMobileEfiRequestError(op string, err error) error {
	class := mobilePaymentProviderErrorTransient
	if isMobilePaymentAmbiguousError(err) {
		class = mobilePaymentProviderErrorAmbiguous
	}
	return &mobilePaymentProviderError{Class: class, Message: fmt.Sprintf("%s: %v", op, err), Err: err}
}

func isMobilePaymentAmbiguousError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "unexpected eof") ||
		(errors.As(err, &netErr) && netErr.Timeout())
}

func mobileRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

func mobileEfiHTTPClient(cfg *config.Config) *http.Client {
	if cfg == nil {
		return httpclient.Default()
	}
	cert, err := certutil.LoadCertificate(cfg.EfiCertificatePath, cfg.EfiCertificateKey, cfg.EfiCertificateP12, cfg.EfiCertificatePass)
	if err != nil {
		return httpclient.Default()
	}
	return &http.Client{
		Timeout: 25 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}},
	}
}
