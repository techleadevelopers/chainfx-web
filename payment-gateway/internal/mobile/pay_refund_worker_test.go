package mobile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"payment-gateway/internal/config"
)

func TestMobileRefundCreatedAfterEfiDefinitiveFailure(t *testing.T) {
	store := newFakeMobilePaymentExecutionStore(baseMobilePaymentClaim(mobilePaymentExecutionStatusPending))
	provider := &fakeMobilePaymentProvider{executeErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "efi rejected"}}
	worker := testMobilePaymentExecutionWorker(store, provider)
	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.refundCreated != 1 {
		t.Fatalf("expected refund to be created, got %d", store.refundCreated)
	}
}

func TestMobileRefundWorkerBroadcastCallsSignerOnce(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusPending))
	signer := &fakeRefundSigner{broadcastResult: mobilePaymentRefundSignerResult{TxHash: "0xabc"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if signer.broadcastCalls != 1 || store.status != mobilePaymentRefundStatusConfirming || store.txHash != "0xabc" {
		t.Fatalf("expected one signer broadcast and confirming status, calls=%d status=%s tx=%s", signer.broadcastCalls, store.status, store.txHash)
	}
}

func TestMobileRefundConfirmedMarksIntentRefunded(t *testing.T) {
	claim := baseMobileRefundClaim(mobilePaymentRefundStatusConfirming)
	claim.TxHash = "0xabc"
	store := newFakeRefundStore(claim)
	verifier := &fakeRefundVerifier{receipt: mobilePaymentRefundReceipt{TxHash: "0xabc", Status: 1, Confirmations: 6, AmountRaw: claim.AmountRaw}}
	worker := testMobileRefundWorker(store, &fakeRefundSigner{}, verifier)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.status != mobilePaymentRefundStatusCompleted || store.intentStatus != "refunded" {
		t.Fatalf("expected completed/refunded, status=%s intent=%s", store.status, store.intentStatus)
	}
}

func TestMobileRefundWorkerTwiceDoesNotSendTwice(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusPending))
	signer := &fakeRefundSigner{broadcastResult: mobilePaymentRefundSignerResult{TxHash: "0xabc"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	_, _ = worker.runOnce(context.Background())
	store.claim = nil
	_, _ = worker.runOnce(context.Background())
	if signer.broadcastCalls != 1 {
		t.Fatalf("expected one signer call, got %d", signer.broadcastCalls)
	}
}

func TestMobileRefundTimeoutAfterBroadcastDoesNotRetransmitBeforeReconcile(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusPending))
	signer := &fakeRefundSigner{broadcastErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "timeout"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if signer.broadcastCalls != 1 || store.status != mobilePaymentRefundStatusProviderUnknown {
		t.Fatalf("expected provider_unknown after one ambiguous broadcast, calls=%d status=%s", signer.broadcastCalls, store.status)
	}

	store.claim = baseMobileRefundClaim(mobilePaymentRefundStatusProviderUnknown)
	signer.broadcastErr = nil
	signer.reconcileResult = mobilePaymentRefundSignerResult{TxHash: "0xabc"}
	_, err = worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if signer.broadcastCalls != 1 || signer.reconcileCalls != 1 {
		t.Fatalf("expected reconcile before retransmit, broadcasts=%d reconciles=%d", signer.broadcastCalls, signer.reconcileCalls)
	}
}

func TestMobileRefundSignerConfirmedCompletesAfterReceipt(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusProviderUnknown))
	signer := &fakeRefundSigner{reconcileResult: mobilePaymentRefundSignerResult{TxHash: "0xabc", Status: "confirmed"}}
	verifier := &fakeRefundVerifier{receipt: mobilePaymentRefundReceipt{TxHash: "0xabc", Status: 1, Confirmations: 6, AmountRaw: "101364523000000000000"}}
	worker := testMobileRefundWorker(store, signer, verifier)

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if signer.broadcastCalls != 0 || signer.reconcileCalls != 1 || store.status != mobilePaymentRefundStatusCompleted || store.intentStatus != "refunded" {
		t.Fatalf("expected signer lookup + receipt completion, broadcasts=%d reconciles=%d status=%s intent=%s", signer.broadcastCalls, signer.reconcileCalls, store.status, store.intentStatus)
	}
}

func TestMobileRefundSignerFailedBeforeBroadcastReturnsRetry(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusProviderUnknown))
	signer := &fakeRefundSigner{reconcileErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorTransient, Message: "signer failed_before_broadcast"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if signer.broadcastCalls != 0 || signer.reconcileCalls != 1 || store.status != mobilePaymentRefundStatusRetryWait {
		t.Fatalf("expected safe retry after failed_before_broadcast, broadcasts=%d reconciles=%d status=%s", signer.broadcastCalls, signer.reconcileCalls, store.status)
	}
}

func TestMobileRefundSignerBroadcastUnknownDoesNotSecondSend(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusProviderUnknown))
	signer := &fakeRefundSigner{reconcileResult: mobilePaymentRefundSignerResult{TxHash: "0xabc", Status: "broadcast_unknown"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if signer.broadcastCalls != 0 || signer.reconcileCalls != 1 || store.status != mobilePaymentRefundStatusConfirming {
		t.Fatalf("expected broadcast_unknown recovery without send, broadcasts=%d reconciles=%d status=%s", signer.broadcastCalls, signer.reconcileCalls, store.status)
	}
}

func TestMobileRefundSignerOperationNotFoundDoesNotBlindResend(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusProviderUnknown))
	signer := &fakeRefundSigner{reconcileErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorAmbiguous, Message: "signer operation not found"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if signer.broadcastCalls != 0 || signer.reconcileCalls != 1 || store.status != mobilePaymentRefundStatusProviderUnknown {
		t.Fatalf("operation not found must not blind resend, broadcasts=%d reconciles=%d status=%s", signer.broadcastCalls, signer.reconcileCalls, store.status)
	}
}

func TestMobileRefundSignerSignedRecoverableUsesRecoveryEndpoint(t *testing.T) {
	var lookupCalls, recoverCalls, transferCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/operations/"):
			lookupCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"operation_id":"refund:mpay_test","status":"signed","tx_hash":"0xabc","from":"0x1111111111111111111111111111111111111111","network":"BSC","recoverable":true}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/recover"):
			recoverCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"txHash":"0xabc","from":"0x1111111111111111111111111111111111111111","network":"BSC","status":"broadcast","operationId":"refund:mpay_test","nonce":7,"chainId":56}`))
		case r.Method == http.MethodPost && r.URL.Path == "/hd/transfer":
			transferCalls++
			http.Error(w, "unexpected transfer", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := &mobilePaymentRefundSignerClient{
		cfg:    &config.Config{SignerUrl: srv.URL, SignerHmacSecret: "0123456789abcdef0123456789abcdef"},
		client: srv.Client(),
	}

	result, err := client.ReconcileRefund(context.Background(), mobilePaymentRefundSignerRequest{
		IdempotencyKey:    "refund:mpay_test",
		SignerOperationID: "refund:mpay_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TxHash != "0xabc" || result.Status != "broadcast" {
		t.Fatalf("unexpected recovery result: %+v", result)
	}
	if lookupCalls != 1 || recoverCalls != 1 || transferCalls != 0 {
		t.Fatalf("expected lookup+recover without transfer, lookup=%d recover=%d transfer=%d", lookupCalls, recoverCalls, transferCalls)
	}
}

func TestMobileRefundReceiptRevertedDoesNotMarkRefunded(t *testing.T) {
	claim := baseMobileRefundClaim(mobilePaymentRefundStatusConfirming)
	claim.TxHash = "0xabc"
	store := newFakeRefundStore(claim)
	worker := testMobileRefundWorker(store, &fakeRefundSigner{}, &fakeRefundVerifier{err: errMobileRefundReceiptReverted})

	_, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.status != mobilePaymentRefundStatusManualReview || store.intentStatus == "refunded" {
		t.Fatalf("reverted receipt must go manual_review, status=%s intent=%s", store.status, store.intentStatus)
	}
}

func TestMobileWebhookCompletedMapsToCompleted(t *testing.T) {
	result := parseMobileEfiWebhookResult("mpay-efi-test", "E123", "REALIZADO")
	if result.Outcome != mobilePaymentProviderResultCompleted {
		t.Fatalf("expected completed, got %s", result.Outcome)
	}
}

func TestMobileWebhookDuplicateHashIsStable(t *testing.T) {
	event := EfiMobileWebhookEvent{IDEnvio: "mpay-efi-test", E2EID: "E123", Status: "REALIZADO", Raw: []byte(`{"idEnvio":"mpay-efi-test"}`)}
	if mobileEfiWebhookHash(event) != mobileEfiWebhookHash(event) {
		t.Fatal("webhook hash must be stable for duplicate detection")
	}
}

func TestMobileWebhookCompletedBeforeBroadcastCancelsRefund(t *testing.T) {
	claim := baseMobileRefundClaim(mobilePaymentRefundStatusPending)
	claim.ExecutionStatus = mobilePaymentExecutionStatusCompleted
	claim.IntentStatus = "completed"
	store := newFakeRefundStore(claim)
	worker := testMobileRefundWorker(store, &fakeRefundSigner{}, &fakeRefundVerifier{})

	ok, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || store.status != mobilePaymentRefundStatusCancelled {
		t.Fatalf("expected refund cancelled before broadcast, ok=%v status=%s", ok, store.status)
	}
}

func TestMobileLateEfiSuccessAfterRefundBroadcastGoesManualReview(t *testing.T) {
	claim := baseMobileRefundClaim(mobilePaymentRefundStatusConfirming)
	claim.ExecutionStatus = mobilePaymentExecutionStatusCompleted
	claim.IntentStatus = "completed"
	claim.TxHash = "0xabc"
	store := newFakeRefundStore(claim)
	worker := testMobileRefundWorker(store, &fakeRefundSigner{}, &fakeRefundVerifier{})

	ok, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || store.status != mobilePaymentRefundStatusManualReview || store.intentStatus != "manual_review" {
		t.Fatalf("late success after broadcast must be manual_review, ok=%v status=%s intent=%s", ok, store.status, store.intentStatus)
	}
}

func TestMobileRefundDestinationIsOriginalFundingSender(t *testing.T) {
	claim := baseMobilePaymentClaim(mobilePaymentExecutionStatusPending)
	claim.WalletAddress = "0x9999999999999999999999999999999999999999"
	claim.FundingFromAddress = "0x1111111111111111111111111111111111111111"
	store := newFakeMobilePaymentExecutionStore(claim)
	provider := &fakeMobilePaymentProvider{executeErr: &mobilePaymentProviderError{Class: mobilePaymentProviderErrorDefinitive, Message: "efi rejected"}}
	worker := testMobilePaymentExecutionWorker(store, provider)

	_, _ = worker.runOnce(context.Background())
	if store.refundWallet != claim.FundingFromAddress {
		t.Fatalf("refund destination must be funding sender, got %s", store.refundWallet)
	}
}

func TestMobileRefundAmountUsesPersistedFundingRawNotFrontend(t *testing.T) {
	if got := mobilePayRawToDecimal("101364523000000000000", 18); got != "101.364523" {
		t.Fatalf("expected exact persisted funding amount, got %s", got)
	}
}

func TestMobileRefundProviderUnknownIntentNotEligible(t *testing.T) {
	claim := baseMobileRefundClaim(mobilePaymentRefundStatusPending)
	claim.IntentStatus = "provider_unknown"
	if mobileRefundEligibleForBroadcast(claim) {
		t.Fatal("provider_unknown intent must not be refund eligible")
	}
}

func TestMobileRefundLegacyPendingWithProviderUnknownExecutionDoesNotBroadcast(t *testing.T) {
	claim := baseMobileRefundClaim(mobilePaymentRefundStatusPending)
	claim.ExecutionStatus = mobilePaymentExecutionStatusProviderUnknown
	claim.ProviderStatus = "not_found_unconfirmed"
	store := newFakeRefundStore(claim)
	signer := &fakeRefundSigner{broadcastResult: mobilePaymentRefundSignerResult{TxHash: "0xabc"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	ok, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || signer.broadcastCalls != 0 || store.status != mobilePaymentRefundStatusManualReview {
		t.Fatalf("ambiguous legacy refund must be blocked, ok=%v calls=%d status=%s", ok, signer.broadcastCalls, store.status)
	}
}

func TestMobileRefundLegitimateDefinitiveFailureStillBroadcasts(t *testing.T) {
	claim := baseMobileRefundClaim(mobilePaymentRefundStatusPending)
	claim.ExecutionStatus = mobilePaymentExecutionStatusFailed
	claim.ProviderStatus = "REJEITADO"
	store := newFakeRefundStore(claim)
	signer := &fakeRefundSigner{broadcastResult: mobilePaymentRefundSignerResult{TxHash: "0xabc"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	ok, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || signer.broadcastCalls != 1 || store.status != mobilePaymentRefundStatusConfirming {
		t.Fatalf("definitive failure must remain refundable, ok=%v calls=%d status=%s", ok, signer.broadcastCalls, store.status)
	}
}

func TestMobileRefundCompletedNeverReturnsToProcessing(t *testing.T) {
	store := newFakeRefundStore(baseMobileRefundClaim(mobilePaymentRefundStatusCompleted))
	signer := &fakeRefundSigner{broadcastResult: mobilePaymentRefundSignerResult{TxHash: "0xabc"}}
	worker := testMobileRefundWorker(store, signer, &fakeRefundVerifier{})

	ok, err := worker.runOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok || signer.broadcastCalls != 0 {
		t.Fatalf("completed refund must not run, ok=%v calls=%d", ok, signer.broadcastCalls)
	}
}

func TestMobileRefundStaleProcessingRecoveryDoesNotBlindSend(t *testing.T) {
	store := newFakeRefundStore(nil)
	store.staleRecovered = 1
	worker := testMobileRefundWorker(store, &fakeRefundSigner{}, &fakeRefundVerifier{})
	worker.runTick(context.Background())
	if store.recoverCalls != 1 {
		t.Fatalf("expected stale recovery, got %d", store.recoverCalls)
	}
}

func testMobileRefundWorker(store *fakeRefundStore, signer *fakeRefundSigner, verifier *fakeRefundVerifier) *mobilePaymentRefundWorker {
	return &mobilePaymentRefundWorker{
		store:       store,
		signer:      signer,
		verifier:    verifier,
		pollEvery:   time.Hour,
		staleAfter:  time.Minute,
		maxAttempts: 6,
		now: func() time.Time {
			return time.Unix(1000, 0).UTC()
		},
	}
}

func baseMobileRefundClaim(status string) *mobilePaymentRefundClaim {
	return &mobilePaymentRefundClaim{
		RefundID:          "mprefund_test",
		PaymentID:         "mpay_test",
		ExecutionID:       "mpexec_test",
		UserID:            "00000000-0000-0000-0000-000000000001",
		WalletAddress:     "0x1111111111111111111111111111111111111111",
		Asset:             "USDT",
		Network:           "BSC",
		TokenContract:     "0x55d398326f99059fF775485246999027B3197955",
		TokenDecimals:     18,
		AmountMicro:       101_364_523,
		AmountRaw:         "101364523000000000000",
		StatusBefore:      status,
		RefundReason:      "efi rejected",
		IdempotencyKey:    "refund:mpay_test",
		SignerOperationID: "refund:mpay_test",
		ExecutionStatus:   mobilePaymentExecutionStatusFailed,
		IntentStatus:      "refund_pending",
		ProviderStatus:    "REJEITADO",
		FundingTxHash:     "0xfund",
		TreasuryAddress:   "0x2222222222222222222222222222222222222222",
	}
}

type fakeRefundStore struct {
	claim          *mobilePaymentRefundClaim
	status         string
	intentStatus   string
	txHash         string
	recoverCalls   int
	staleRecovered int64
}

func newFakeRefundStore(claim *mobilePaymentRefundClaim) *fakeRefundStore {
	return &fakeRefundStore{claim: claim}
}

func (s *fakeRefundStore) RecoverStaleRefunds(context.Context, time.Duration) (int64, error) {
	s.recoverCalls++
	return s.staleRecovered, nil
}

func (s *fakeRefundStore) ClaimNextRefund(context.Context, int) (*mobilePaymentRefundClaim, error) {
	if s.claim == nil || s.claim.StatusBefore == mobilePaymentRefundStatusCompleted {
		return nil, nil
	}
	claim := *s.claim
	s.claim = nil
	if claim.ExecutionStatus == mobilePaymentExecutionStatusCompleted || claim.IntentStatus == "completed" {
		if claim.TxHash != "" || claim.StatusBefore == mobilePaymentRefundStatusConfirming || claim.StatusBefore == mobilePaymentRefundStatusBroadcast {
			s.status = mobilePaymentRefundStatusManualReview
			s.intentStatus = "manual_review"
		} else {
			s.status = mobilePaymentRefundStatusCancelled
		}
		return nil, nil
	}
	if !mobileRefundEligibleForBroadcast(&claim) {
		s.status = mobilePaymentRefundStatusManualReview
		return nil, nil
	}
	switch claim.StatusBefore {
	case mobilePaymentRefundStatusBroadcast, mobilePaymentRefundStatusConfirming:
		claim.Action = mobileRefundActionConfirm
	case mobilePaymentRefundStatusProviderUnknown:
		claim.Action = mobileRefundActionReconcile
	default:
		claim.Action = mobileRefundActionBroadcast
	}
	claim.Attempt++
	s.status = mobilePaymentRefundStatusProcessing
	return &claim, nil
}

func (s *fakeRefundStore) MarkRefundBroadcast(_ context.Context, _ *mobilePaymentRefundClaim, txHash string) error {
	s.status = mobilePaymentRefundStatusConfirming
	s.txHash = txHash
	return nil
}

func (s *fakeRefundStore) MarkRefundRetry(context.Context, *mobilePaymentRefundClaim, string, time.Time) error {
	s.status = mobilePaymentRefundStatusRetryWait
	return nil
}

func (s *fakeRefundStore) MarkRefundUnknown(context.Context, *mobilePaymentRefundClaim, string, time.Time) error {
	s.status = mobilePaymentRefundStatusProviderUnknown
	return nil
}

func (s *fakeRefundStore) CompleteRefund(context.Context, *mobilePaymentRefundClaim, mobilePaymentRefundReceipt) error {
	s.status = mobilePaymentRefundStatusCompleted
	s.intentStatus = "refunded"
	return nil
}

func (s *fakeRefundStore) MarkRefundManualReview(context.Context, *mobilePaymentRefundClaim, string) error {
	s.status = mobilePaymentRefundStatusManualReview
	return nil
}

func (s *fakeRefundStore) CancelRefund(context.Context, *mobilePaymentRefundClaim, string) error {
	s.status = mobilePaymentRefundStatusCancelled
	return nil
}

type fakeRefundSigner struct {
	broadcastCalls  int
	reconcileCalls  int
	broadcastResult mobilePaymentRefundSignerResult
	broadcastErr    error
	reconcileResult mobilePaymentRefundSignerResult
	reconcileErr    error
}

func (s *fakeRefundSigner) BroadcastRefund(context.Context, mobilePaymentRefundSignerRequest) (mobilePaymentRefundSignerResult, error) {
	s.broadcastCalls++
	if s.broadcastErr != nil {
		return mobilePaymentRefundSignerResult{}, s.broadcastErr
	}
	return s.broadcastResult, nil
}

func (s *fakeRefundSigner) ReconcileRefund(context.Context, mobilePaymentRefundSignerRequest) (mobilePaymentRefundSignerResult, error) {
	s.reconcileCalls++
	if s.reconcileErr != nil {
		return mobilePaymentRefundSignerResult{}, s.reconcileErr
	}
	return s.reconcileResult, nil
}

type fakeRefundVerifier struct {
	receipt mobilePaymentRefundReceipt
	err     error
}

func (v *fakeRefundVerifier) VerifyRefundReceipt(context.Context, mobilePaymentRefundClaim) (mobilePaymentRefundReceipt, error) {
	if v.err != nil {
		return mobilePaymentRefundReceipt{}, v.err
	}
	if v.receipt.TxHash == "" {
		return mobilePaymentRefundReceipt{}, errors.New("missing fake receipt")
	}
	return v.receipt, nil
}
