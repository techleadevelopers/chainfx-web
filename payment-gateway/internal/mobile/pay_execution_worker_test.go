package mobile

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMobilePaymentExecutionSuccessCompletesIntent(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusPending))
	provider := &fakeMobilePaymentProvider{executeResult: mobilePaymentProviderResult{
		Outcome:               mobilePaymentProviderResultCompleted,
		ProviderReference:     "E123",
		ProviderTransactionID: "mpay-efi-test",
		ProviderStatus:        "REALIZADO",
	}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	ok, err := worker.runOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("expected one processed claim, ok=%v err=%v", ok, err)
	}
	if provider.executeCalls != 1 {
		t.Fatalf("expected one provider execution, got %d", provider.executeCalls)
	}
	if store.completed != 1 || store.intentStatus != "completed" {
		t.Fatalf("expected completed intent, completed=%d intent=%s", store.completed, store.intentStatus)
	}
}

func TestMobilePaymentExecutionDefinitiveErrorCreatesRefundPending(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusPending))
	provider := &fakeMobilePaymentProvider{executeErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "efi pix send status 422"}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.refundPending != 1 || store.intentStatus != "refund_pending" {
		t.Fatalf("expected refund_pending, refunds=%d intent=%s", store.refundPending, store.intentStatus)
	}
}

func TestMobilePaymentExecutionRetryable500SchedulesRetry(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusPending))
	provider := &fakeMobilePaymentProvider{executeErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorTransient, Message: "efi pix send status 500"}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.retry != 1 || store.executionStatus != mobilePaymentExecutionStatusRetryWait {
		t.Fatalf("expected retry_wait, retry=%d status=%s", store.retry, store.executionStatus)
	}
}

func TestMobilePaymentExecutionTimeoutGoesUnknownAndDoesNotPostAgain(t *testing.T) {
	claim := baseMobilePaymentClaim(mobilePaymentExecutionStatusPending)
	store := newFakeMobilePaymentExecutionStore(claim)
	provider := &fakeMobilePaymentProvider{executeErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "context deadline exceeded"}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.executeCalls != 1 || store.executionStatus != mobilePaymentExecutionStatusProviderUnknown {
		t.Fatalf("expected one POST then provider_unknown, execute=%d status=%s", provider.executeCalls, store.executionStatus)
	}

	store.claim = baseMobilePaymentClaim(mobilePaymentExecutionStatusProviderUnknown)
	store.claim.Action = "reconcile"
	provider.executeErr = nil
	provider.reconcileResult = mobilePaymentProviderResult{Outcome: mobilePaymentProviderResultPending, ProviderReference: "mpay-efi-test", ProviderStatus: "EM_PROCESSAMENTO"}
	_, err = worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.executeCalls != 1 {
		t.Fatalf("provider_unknown must reconcile before another POST, execute calls=%d", provider.executeCalls)
	}
	if provider.reconcileCalls != 1 {
		t.Fatalf("expected reconcile call, got %d", provider.reconcileCalls)
	}
}

func TestMobilePaymentReconcilerCompletedCompletesIntent(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusProviderUnknown))
	provider := &fakeMobilePaymentProvider{reconcileResult: mobilePaymentProviderResult{
		Outcome:           mobilePaymentProviderResultCompleted,
		ProviderReference: "E123",
		ProviderStatus:    "REALIZADO",
	}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.reconcileCalls != 1 || store.completed != 1 || store.intentStatus != "completed" {
		t.Fatalf("expected reconciled completed, reconcile=%d completed=%d intent=%s", provider.reconcileCalls, store.completed, store.intentStatus)
	}
}

func TestMobilePaymentReconcilerRejectedCreatesRefundPending(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusProviderPending))
	provider := &fakeMobilePaymentProvider{reconcileResult: mobilePaymentProviderResult{
		Outcome:           mobilePaymentProviderResultFailed,
		ProviderReference: "mpay-efi-test",
		ProviderStatus:    "REJEITADO",
	}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.refundPending != 1 || store.intentStatus != "refund_pending" {
		t.Fatalf("expected refund_pending from rejected reconcile, refunds=%d intent=%s", store.refundPending, store.intentStatus)
	}
}

func TestMobilePaymentPostTimeoutThen404DoesNotCreateRefundPending(t *testing.T) {
	claim := baseMobilePaymentClaim(mobilePaymentExecutionStatusProviderUnknown)
	claim.Action = "reconcile"
	claim.FirstAmbiguousAt = sqlNullTime(time.Unix(990, 0).UTC())
	store := newFakeMobilePaymentExecutionStore(claim)
	provider := &fakeMobilePaymentProvider{reconcileErr: &mobilePaymentProviderError{
		Class:      mobilePaymentProviderErrorDefinitive,
		Message:    "efi pix sent lookup status 404",
		HTTPStatus: 404,
	}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.refundPending != 0 || store.executionStatus != mobilePaymentExecutionStatusProviderUnknown {
		t.Fatalf("single 404 after ambiguous submit must stay provider_unknown, refunds=%d status=%s", store.refundPending, store.executionStatus)
	}
}

func TestMobilePaymentRepeated404InsideGraceStaysUnknown(t *testing.T) {
	claim := baseMobilePaymentClaim(mobilePaymentExecutionStatusProviderUnknown)
	claim.Action = "reconcile"
	claim.FirstAmbiguousAt = sqlNullTime(time.Unix(950, 0).UTC())
	claim.ConsecutiveNotFound = 2
	claim.ReconciliationAttempts = 2
	store := newFakeMobilePaymentExecutionStore(claim)
	provider := &fakeMobilePaymentProvider{reconcileErr: &mobilePaymentProviderError{
		Class:      mobilePaymentProviderErrorDefinitive,
		Message:    "efi pix sent lookup status 404",
		HTTPStatus: 404,
	}}
	worker := testMobilePaymentExecutionWorker(store, provider)
	worker.ambiguousGrace = 10 * time.Minute

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.manual != 0 || store.refundPending != 0 || store.executionStatus != mobilePaymentExecutionStatusProviderUnknown {
		t.Fatalf("404 inside grace must not manual/refund, manual=%d refunds=%d status=%s", store.manual, store.refundPending, store.executionStatus)
	}
}

func TestMobilePaymentRepeated404AfterGraceGoesManualReviewNotRefund(t *testing.T) {
	claim := baseMobilePaymentClaim(mobilePaymentExecutionStatusProviderUnknown)
	claim.Action = "reconcile"
	claim.FirstAmbiguousAt = sqlNullTime(time.Unix(0, 0).UTC())
	claim.ConsecutiveNotFound = 2
	claim.ReconciliationAttempts = 2
	store := newFakeMobilePaymentExecutionStore(claim)
	provider := &fakeMobilePaymentProvider{reconcileErr: &mobilePaymentProviderError{
		Class:      mobilePaymentProviderErrorDefinitive,
		Message:    "efi pix sent lookup status 404",
		HTTPStatus: 404,
	}}
	worker := testMobilePaymentExecutionWorker(store, provider)
	worker.ambiguousGrace = time.Minute
	worker.maxNotFoundChecks = 3

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.manual != 1 || store.refundPending != 0 || store.intentStatus != "manual_review" {
		t.Fatalf("persistent not_found must manual_review without refund, manual=%d refunds=%d intent=%s", store.manual, store.refundPending, store.intentStatus)
	}
}

func TestMobilePayment404ThenWebhookStyleCompletedCompletes(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusProviderUnknown))
	provider := &fakeMobilePaymentProvider{reconcileResult: mobilePaymentProviderResult{
		Outcome:           mobilePaymentProviderResultCompleted,
		ProviderReference: "E123",
		ProviderStatus:    "REALIZADO",
	}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.completed != 1 || store.refundPending != 0 || store.intentStatus != "completed" {
		t.Fatalf("completed reconcile after not_found must complete only, completed=%d refunds=%d intent=%s", store.completed, store.refundPending, store.intentStatus)
	}
}

func TestMobilePaymentWorkerTwiceDoesNotExecuteCompletedAgain(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusPending))
	provider := &fakeMobilePaymentProvider{executeResult: mobilePaymentProviderResult{Outcome: mobilePaymentProviderResultCompleted, ProviderReference: "E123"}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, _ = worker.runOnce(context.Background())
	store.claim = nil
	_, _ = worker.runOnce(context.Background())
	if provider.executeCalls != 1 {
		t.Fatalf("expected exactly one provider execution, got %d", provider.executeCalls)
	}
}

func TestMobilePaymentStaleProcessingGoesToReconciliation(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(nil)
	store.staleRecovered = 1
	worker := testMobilePaymentExecutionWorker(store, &fakeMobilePaymentProvider{})
	worker.runTick(context.Background())
	if store.recoverCalls != 1 {
		t.Fatalf("expected stale recovery call, got %d", store.recoverCalls)
	}
}

func TestMobilePaymentCompletedExecutionIsIgnored(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusCompleted))
	provider := &fakeMobilePaymentProvider{executeResult: mobilePaymentProviderResult{Outcome: mobilePaymentProviderResultCompleted}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	ok, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || provider.executeCalls != 0 {
		t.Fatalf("completed execution must not run, ok=%v execute=%d", ok, provider.executeCalls)
	}
}

func TestMobilePaymentIntentWithoutFundingDoesNotCallProvider(t *testing.T) {
	claim := baseMobilePaymentClaim(mobilePaymentExecutionStatusPending)
	claim.IntentStatus = "awaiting_funding"
	claim.FundingTxHash = ""
	store := newFakeMobilePaymentExecutionStore(claim)
	provider := &fakeMobilePaymentProvider{executeResult: mobilePaymentProviderResult{Outcome: mobilePaymentProviderResultCompleted}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	ok, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || provider.executeCalls != 0 || store.executionStatus != mobilePaymentExecutionStatusFailed {
		t.Fatalf("intent without funding must be blocked before provider, ok=%v execute=%d status=%s", ok, provider.executeCalls, store.executionStatus)
	}
}

func testMobilePaymentExecutionWorker(store *fakeMobilePaymentExecutionStore, provider *fakeMobilePaymentProvider) *mobilePaymentExecutionWorker {
	return &mobilePaymentExecutionWorker{
		store:             store,
		provider:          provider,
		pollEvery:         time.Hour,
		staleAfter:        time.Minute,
		maxAttempts:       6,
		ambiguousGrace:    15 * time.Minute,
		maxNotFoundChecks: 3,
		now: func() time.Time {
			return time.Unix(1000, 0).UTC()
		},
	}
}

func baseMobilePaymentClaim(status string) *mobilePaymentExecutionClaim {
	return &mobilePaymentExecutionClaim{
		ExecutionID:            "mpexec_test",
		PaymentID:              "mpay_test",
		UserID:                 "00000000-0000-0000-0000-000000000001",
		WalletAddress:          "0x1111111111111111111111111111111111111111",
		Provider:               "efi",
		ProviderIdempotencyKey: "mpay-efi-test",
		StatusBefore:           status,
		IntentStatus:           "funding_confirmed",
		FundingTxHash:          "0xabc",
		FundingNetwork:         "BSC",
		FundingTokenContract:   "0x55d398326f99059fF775485246999027B3197955",
		FundingTokenDecimals:   18,
		FundingAmountRaw:       "2000000000000000000",
		FundingFromAddress:     "0x1111111111111111111111111111111111111111",
		PaymentType:            "pix",
		RawCode:                "00020126330014BR.GOV.BCB.PIX0111pix@example520400005303986540610.005802BR5904Loja6304ABCD",
		BeneficiaryName:        "Loja",
		AmountBRL:              10,
		RequiredUSDTMic:        2_000_000,
	}
}

type fakeMobilePaymentExecutionStore struct {
	claim           *mobilePaymentExecutionClaim
	executionStatus string
	intentStatus    string
	completed       int
	refundPending   int
	refundCreated   int
	refundWallet    string
	retry           int
	unknown         int
	pending         int
	manual          int
	recoverCalls    int
	staleRecovered  int64
}

func newFakeMobilePaymentExecutionStore(claim *mobilePaymentExecutionClaim) *fakeMobilePaymentExecutionStore {
	return &fakeMobilePaymentExecutionStore{claim: claim}
}

func (s *fakeMobilePaymentExecutionStore) RecoverStaleProcessing(context.Context, time.Duration) (int64, error) {
	s.recoverCalls++
	return s.staleRecovered, nil
}

func (s *fakeMobilePaymentExecutionStore) ClaimNext(context.Context, int) (*mobilePaymentExecutionClaim, error) {
	if s.claim == nil || s.claim.StatusBefore == mobilePaymentExecutionStatusCompleted {
		return nil, nil
	}
	claim := *s.claim
	if !mobilePaymentIntentFundedForExecution(&claim) {
		s.executionStatus = mobilePaymentExecutionStatusFailed
		s.claim = nil
		return nil, nil
	}
	if claim.StatusBefore == mobilePaymentExecutionStatusProviderPending || claim.StatusBefore == mobilePaymentExecutionStatusProviderUnknown {
		claim.Action = "reconcile"
	} else {
		claim.Action = "execute"
	}
	claim.Attempt++
	s.executionStatus = mobilePaymentExecutionStatusProcessing
	s.claim = nil
	return &claim, nil
}

func (s *fakeMobilePaymentExecutionStore) CompleteExecution(context.Context, *mobilePaymentExecutionClaim, mobilePaymentProviderResult) error {
	s.completed++
	s.executionStatus = mobilePaymentExecutionStatusCompleted
	s.intentStatus = "completed"
	return nil
}

func (s *fakeMobilePaymentExecutionStore) FailExecutionForRefund(_ context.Context, claim *mobilePaymentExecutionClaim, _ string, _ mobilePaymentProviderResult) error {
	s.refundPending++
	s.refundCreated++
	s.refundWallet = strings.TrimSpace(claim.FundingFromAddress)
	s.executionStatus = mobilePaymentExecutionStatusFailed
	s.intentStatus = "refund_pending"
	return nil
}

func (s *fakeMobilePaymentExecutionStore) RetryExecution(context.Context, *mobilePaymentExecutionClaim, string, time.Time) error {
	s.retry++
	s.executionStatus = mobilePaymentExecutionStatusRetryWait
	return nil
}

func (s *fakeMobilePaymentExecutionStore) MarkProviderUnknown(context.Context, *mobilePaymentExecutionClaim, string, time.Time) error {
	s.unknown++
	s.executionStatus = mobilePaymentExecutionStatusProviderUnknown
	return nil
}

func (s *fakeMobilePaymentExecutionStore) MarkProviderPending(context.Context, *mobilePaymentExecutionClaim, mobilePaymentProviderResult, time.Time) error {
	s.pending++
	s.executionStatus = mobilePaymentExecutionStatusProviderPending
	s.intentStatus = "provider_pending"
	return nil
}

func (s *fakeMobilePaymentExecutionStore) MarkReconcileNotFound(_ context.Context, _ *mobilePaymentExecutionClaim, _ string, _ time.Time, manualReview bool) error {
	s.unknown++
	if manualReview {
		s.manual++
		s.executionStatus = mobilePaymentExecutionStatusManualReview
		s.intentStatus = "manual_review"
		return nil
	}
	s.executionStatus = mobilePaymentExecutionStatusProviderUnknown
	s.intentStatus = "provider_pending"
	return nil
}

func (s *fakeMobilePaymentExecutionStore) MarkManualReview(context.Context, *mobilePaymentExecutionClaim, string) error {
	s.manual++
	s.executionStatus = mobilePaymentExecutionStatusManualReview
	s.intentStatus = "manual_review"
	return nil
}

func sqlNullTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: true}
}

type fakeMobilePaymentProvider struct {
	executeCalls    int
	reconcileCalls  int
	executeResult   mobilePaymentProviderResult
	executeErr      error
	reconcileResult mobilePaymentProviderResult
	reconcileErr    error
}

func (p *fakeMobilePaymentProvider) Execute(context.Context, mobilePaymentProviderRequest) (mobilePaymentProviderResult, error) {
	p.executeCalls++
	if p.executeErr != nil {
		return mobilePaymentProviderResult{}, p.executeErr
	}
	if p.executeResult.Outcome == "" {
		return mobilePaymentProviderResult{}, errors.New("missing fake execute result")
	}
	return p.executeResult, nil
}

func (p *fakeMobilePaymentProvider) Reconcile(context.Context, mobilePaymentProviderRequest) (mobilePaymentProviderResult, error) {
	p.reconcileCalls++
	if p.reconcileErr != nil {
		return mobilePaymentProviderResult{}, p.reconcileErr
	}
	if p.reconcileResult.Outcome == "" {
		return mobilePaymentProviderResult{}, errors.New("missing fake reconcile result")
	}
	return p.reconcileResult, nil
}
