package workers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

func TestBuildEfiPixSendPayloadUsesMerchantAmountNotTotalDebit(t *testing.T) {
	settlement := &database.MerchantSettlement{
		ID:              "nfc_settle_test",
		AuthorizationID: "nfc_auth_test",
		AmountBRLMinor:  10000,
		FeeBRLMinor:     400,
		TargetPixKey:    "merchant@example.com",
	}
	payload := buildEfiPixSendPayload("payer@example.com", settlement)
	if payload["valor"] != "100.00" {
		t.Fatalf("expected merchant amount 100.00, got %v", payload["valor"])
	}
	if payload["valor"] == "104.00" {
		t.Fatal("payout must not use total debit including ChainFX fee")
	}
}

func TestBuildEfiPixSendUsesStableIDEnvio(t *testing.T) {
	settlement := &database.MerchantSettlement{
		ID:             "nfc_settle_test",
		IdempotencyKey: "nfc_settle_test",
		AmountBRLMinor: 10000,
		TargetPixKey:   "merchant@example.com",
	}
	first := settlement.IdempotencyKey
	second := settlement.IdempotencyKey
	if first == "" || first != second {
		t.Fatalf("expected stable idEnvio, got %q and %q", first, second)
	}
}

func TestParseEfiPixSendResultDoesNotImplyConfirmed(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"idEnvio": "nfc_settle_test",
		"e2eId":   "E123",
		"status":  "EM_PROCESSAMENTO",
	})
	result, err := parseEfiPixSendResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status == database.MerchantSettlementStatusConfirmed {
		t.Fatal("initial Efí response must not be treated as confirmed")
	}
}

func TestRetryAfterParsesSeconds(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "7")
	if got := retryAfter(resp); got != 7*time.Second {
		t.Fatalf("expected 7s retry-after, got %s", got)
	}
}

func TestNFCSettlementHappyPathSubmitCompleted(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	efi.putStatus = "REALIZADO"
	efi.putE2E = "E123"
	store := newFakeNFCSettlementStore()
	worker := newTestNFCSettlementWorker(store, efi.URL, 200*time.Millisecond)

	worker.processOne(context.Background(), store.settlement.ID)

	if efi.putCount() != 1 {
		t.Fatalf("expected one PUT, got %d", efi.putCount())
	}
	got := store.snapshot()
	if got.Status != database.MerchantSettlementStatusConfirmed {
		t.Fatalf("expected CONFIRMED, got %s", got.Status)
	}
	if got.ProviderIDEnvio != got.IdempotencyKey {
		t.Fatalf("provider_id_envio must be stable idempotency key, got %q", got.ProviderIDEnvio)
	}
}

func TestNFCSettlementTimeoutAfterAcceptPersistsUnknownAndIDEnvio(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	store := newFakeNFCSettlementStore()
	worker := newTestNFCSettlementWorker(store, efi.URL, 500*time.Millisecond)

	worker.processOne(context.Background(), store.settlement.ID)

	got := store.snapshot()
	if efi.putCount() != 1 {
		t.Fatalf("expected one PUT, got %d", efi.putCount())
	}
	if got.Status != database.MerchantSettlementStatusSubmissionUnknown {
		t.Fatalf("expected SUBMISSION_UNKNOWN, got %s", got.Status)
	}
	if got.ProviderIDEnvio != got.IdempotencyKey {
		t.Fatalf("expected provider_id_envio persisted as idempotency key, got %q", got.ProviderIDEnvio)
	}
	if got.SubmitOutcome != "ambiguous" {
		t.Fatalf("expected ambiguous outcome, got %q", got.SubmitOutcome)
	}
}

func TestNFCSettlementSecondWorkerAfterTimeoutReconcilesBeforePUT(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeEfiLookup{{status: "EM_PROCESSAMENTO"}}
	store := newFakeNFCSettlementStore()
	worker := newTestNFCSettlementWorker(store, efi.URL, 500*time.Millisecond)

	worker.processOne(context.Background(), store.settlement.ID)
	efi.timeoutOnPut = false
	worker.processOne(context.Background(), store.settlement.ID)

	if efi.putCount() != 1 {
		t.Fatalf("expected no second PUT, got %d", efi.putCount())
	}
	if efi.getCount() != 1 {
		t.Fatalf("expected one GET reconciliation, got %d", efi.getCount())
	}
	got := store.snapshot()
	if got.Status != database.MerchantSettlementStatusSubmitted {
		t.Fatalf("expected SUBMITTED after pending lookup, got %s", got.Status)
	}
}

func TestNFCSettlementTimeout404DoesNotResubmit(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeEfiLookup{{httpStatus: http.StatusNotFound}}
	store := newFakeNFCSettlementStore()
	worker := newTestNFCSettlementWorker(store, efi.URL, 500*time.Millisecond)

	worker.processOne(context.Background(), store.settlement.ID)
	efi.timeoutOnPut = false
	worker.processOne(context.Background(), store.settlement.ID)

	if efi.putCount() != 1 {
		t.Fatalf("404 after ambiguous submit must not allow new PUT, got %d", efi.putCount())
	}
	got := store.snapshot()
	if got.Status != database.MerchantSettlementStatusSubmissionUnknown {
		t.Fatalf("expected conservative SUBMISSION_UNKNOWN, got %s", got.Status)
	}
}

func TestNFCSettlementRepeated404ManualReviewAfterGrace(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeEfiLookup{
		{httpStatus: http.StatusNotFound},
		{httpStatus: http.StatusNotFound},
		{httpStatus: http.StatusNotFound},
	}
	store := newFakeNFCSettlementStore()
	worker := newTestNFCSettlementWorker(store, efi.URL, 500*time.Millisecond)
	worker.cfg.NFCSettlementAmbiguousGraceSec = 1
	worker.cfg.NFCSettlementNotFoundMinReconciliations = 3

	worker.processOne(context.Background(), store.settlement.ID)
	store.forceAmbiguousAge(2 * time.Second)
	efi.timeoutOnPut = false
	worker.processOne(context.Background(), store.settlement.ID)
	worker.processOne(context.Background(), store.settlement.ID)
	worker.processOne(context.Background(), store.settlement.ID)

	if efi.putCount() != 1 {
		t.Fatalf("repeated 404 must not resubmit, got %d PUTs", efi.putCount())
	}
	got := store.snapshot()
	if got.Status != database.MerchantSettlementStatusManualReview {
		t.Fatalf("expected MANUAL_REVIEW after grace and repeated 404, got %s", got.Status)
	}
}

func TestNFCSettlementTimeoutThenCompletedReconcilesWithoutSecondPUT(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeEfiLookup{{status: "REALIZADO", e2e: "E999"}}
	store := newFakeNFCSettlementStore()
	worker := newTestNFCSettlementWorker(store, efi.URL, 500*time.Millisecond)

	worker.processOne(context.Background(), store.settlement.ID)
	efi.timeoutOnPut = false
	worker.processOne(context.Background(), store.settlement.ID)

	if efi.putCount() != 1 {
		t.Fatalf("expected no second PUT, got %d", efi.putCount())
	}
	got := store.snapshot()
	if got.Status != database.MerchantSettlementStatusConfirmed {
		t.Fatalf("expected CONFIRMED, got %s", got.Status)
	}
}

func TestNFCSettlementTwoWorkersProduceOnePUT(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	efi.putStatus = "EM_PROCESSAMENTO"
	store := newFakeNFCSettlementStore()
	worker := newTestNFCSettlementWorker(store, efi.URL, 200*time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		worker.processOne(context.Background(), store.settlement.ID)
	}()
	go func() {
		defer wg.Done()
		worker.processOne(context.Background(), store.settlement.ID)
	}()
	wg.Wait()

	if efi.putCount() != 1 {
		t.Fatalf("expected one PUT with two workers, got %d", efi.putCount())
	}
}

func TestNFCSettlementCompletedTerminalDoesNotSubmitAgain(t *testing.T) {
	efi := newFakeNFCSettlementEfi(t)
	defer efi.Close()
	store := newFakeNFCSettlementStore()
	store.settlement.Status = database.MerchantSettlementStatusConfirmed
	store.settlement.SubmitOutcome = "confirmed"
	worker := newTestNFCSettlementWorker(store, efi.URL, 50*time.Millisecond)

	worker.processOne(context.Background(), store.settlement.ID)

	if efi.putCount() != 0 {
		t.Fatalf("completed settlement must not submit again, got %d PUTs", efi.putCount())
	}
}

func TestNFCSettlementProviderIDEnvioStableAcrossAttempts(t *testing.T) {
	settlement := &database.MerchantSettlement{IdempotencyKey: "nfc_settle_stable"}
	if got := stableNFCSettlementIDEnvio(settlement); got != "nfc_settle_stable" {
		t.Fatalf("expected idempotency key fallback, got %q", got)
	}
	settlement.ProviderIDEnvio = "nfc_settle_stable"
	if got := stableNFCSettlementIDEnvio(settlement); got != "nfc_settle_stable" {
		t.Fatalf("expected persisted provider_id_envio, got %q", got)
	}
}

type fakeNFCSettlementStore struct {
	mu         sync.Mutex
	settlement database.MerchantSettlement
	claimed    bool
}

func newFakeNFCSettlementStore() *fakeNFCSettlementStore {
	now := time.Now().Add(-time.Second)
	return &fakeNFCSettlementStore{settlement: database.MerchantSettlement{
		ID:              "nfc_settle_test",
		MerchantID:      "merchant_a",
		TerminalID:      "terminal_a",
		AuthorizationID: "nfc_auth_test",
		CaptureID:       "nfc_auth_test",
		AmountBRLMinor:  10000,
		Provider:        "efi",
		Rail:            "pix_send",
		Status:          database.MerchantSettlementStatusPending,
		IdempotencyKey:  "nfc_settle_test",
		TargetPixKey:    "merchant@example.com",
		NextRetryAt:     now,
		SubmitOutcome:   "not_submitted",
		CreatedAt:       now,
		UpdatedAt:       now,
	}}
}

func (s *fakeNFCSettlementStore) snapshot() database.MerchantSettlement {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settlement
}

func (s *fakeNFCSettlementStore) forceAmbiguousAge(age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now().Add(-age)
	s.settlement.FirstAmbiguousAt = &at
	s.settlement.SubmitStartedAt = &at
}

func (s *fakeNFCSettlementStore) GetDueMerchantSettlements(context.Context, int) ([]database.MerchantSettlement, error) {
	return []database.MerchantSettlement{s.snapshot()}, nil
}

func (s *fakeNFCSettlementStore) ClaimMerchantSettlement(context.Context, string) (*database.MerchantSettlement, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.settlement.Status {
	case database.MerchantSettlementStatusConfirmed, database.MerchantSettlementStatusRejected, database.MerchantSettlementStatusManualReview:
		return nil, false, nil
	case database.MerchantSettlementStatusPending, database.MerchantSettlementStatusRetryable:
		if s.claimed {
			return nil, false, nil
		}
		s.claimed = true
		s.settlement.Status = database.MerchantSettlementStatusProcessing
	case database.MerchantSettlementStatusProcessing:
		if s.claimed {
			return nil, false, nil
		}
		s.claimed = true
	}
	cp := s.settlement
	return &cp, true, nil
}

func (s *fakeNFCSettlementStore) MarkMerchantSettlementManualRequired(context.Context, string, string) error {
	return nil
}

func (s *fakeNFCSettlementStore) MarkMerchantSettlementSubmitStarted(_ context.Context, _, idEnvio string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.settlement.ProviderIDEnvio == "" {
		s.settlement.ProviderIDEnvio = idEnvio
	}
	s.settlement.ProviderReference = s.settlement.ProviderIDEnvio
	s.settlement.SubmitStartedAt = &now
	s.settlement.SubmitOutcome = "started"
	return nil
}

func (s *fakeNFCSettlementStore) MarkMerchantSettlementSubmitted(_ context.Context, _, idEnvio, e2eID, providerStatus string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.settlement.Status = database.MerchantSettlementStatusSubmitted
	s.settlement.ProviderIDEnvio = firstNonEmpty(s.settlement.ProviderIDEnvio, idEnvio)
	s.settlement.ProviderE2EID = e2eID
	s.settlement.ProviderStatus = providerStatus
	s.settlement.SubmittedAt = &now
	s.settlement.SubmitCompletedAt = &now
	s.settlement.SubmitOutcome = "confirmed"
	s.claimed = false
	return nil
}

func (s *fakeNFCSettlementStore) MarkMerchantSettlementSubmissionUnknown(_ context.Context, _, idEnvio, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.settlement.Status = database.MerchantSettlementStatusSubmissionUnknown
	s.settlement.ProviderIDEnvio = firstNonEmpty(s.settlement.ProviderIDEnvio, idEnvio)
	s.settlement.ProviderReference = s.settlement.ProviderIDEnvio
	s.settlement.SubmitOutcome = "ambiguous"
	s.settlement.FirstAmbiguousAt = &now
	s.settlement.ErrorMessage = errMsg
	s.claimed = false
	return nil
}

func (s *fakeNFCSettlementStore) MarkMerchantSettlementRetryable(_ context.Context, _, errMsg string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlement.Status = database.MerchantSettlementStatusRetryable
	s.settlement.ErrorMessage = errMsg
	s.claimed = false
	return nil
}

func (s *fakeNFCSettlementStore) MarkMerchantSettlementManualReview(_ context.Context, _, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlement.Status = database.MerchantSettlementStatusManualReview
	s.settlement.ManualReviewReason = errMsg
	s.claimed = false
	return nil
}

func (s *fakeNFCSettlementStore) MarkMerchantSettlementReconcileNotFound(_ context.Context, _, errMsg string, _ time.Duration, minAttempts int, grace time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlement.ReconciliationAttemptCount++
	s.settlement.ConsecutiveNotFound++
	now := time.Now()
	s.settlement.LastReconciledAt = &now
	s.settlement.ErrorMessage = errMsg
	start := s.settlement.FirstAmbiguousAt
	if start == nil {
		start = s.settlement.SubmitStartedAt
	}
	if start != nil && time.Since(*start) >= grace && s.settlement.ReconciliationAttemptCount >= minAttempts {
		s.settlement.Status = database.MerchantSettlementStatusManualReview
		s.settlement.ManualReviewReason = errMsg
	}
	s.claimed = false
	return nil
}

func (s *fakeNFCSettlementStore) ApplyMerchantSettlementProviderEvent(_ context.Context, _, idEnvio, e2eID, status string, _ map[string]any) (bool, *database.MerchantSettlement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settlement.ProviderIDEnvio = firstNonEmpty(s.settlement.ProviderIDEnvio, idEnvio)
	s.settlement.ProviderE2EID = firstNonEmpty(e2eID, s.settlement.ProviderE2EID)
	s.settlement.ProviderStatus = status
	switch strings.ToUpper(status) {
	case "REALIZADO", "CONCLUIDA", "CONCLUÍDA", "COMPLETED", "CONFIRMED":
		s.settlement.Status = database.MerchantSettlementStatusConfirmed
		s.settlement.SubmitOutcome = "confirmed"
	case "REJEITADO", "REJECTED", "FAILED", "CANCELLED", "CANCELED", "DENIED":
		s.settlement.Status = database.MerchantSettlementStatusRejected
		s.settlement.SubmitOutcome = "rejected"
	default:
		s.settlement.Status = database.MerchantSettlementStatusSubmitted
	}
	s.settlement.ReconciliationAttemptCount++
	s.settlement.ConsecutiveNotFound = 0
	s.claimed = false
	cp := s.settlement
	return false, &cp, nil
}

type fakeEfiLookup struct {
	httpStatus int
	status     string
	e2e        string
}

type fakeNFCSettlementEfi struct {
	*httptest.Server
	mu           sync.Mutex
	puts         int
	gets         int
	timeoutOnPut bool
	putStatus    string
	putE2E       string
	getStatuses  []fakeEfiLookup
}

func newFakeNFCSettlementEfi(t *testing.T) *fakeNFCSettlementEfi {
	t.Helper()
	f := &fakeNFCSettlementEfi{putStatus: "EM_PROCESSAMENTO", putE2E: "E123"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeNFCSettlementEfi) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

func (f *fakeNFCSettlementEfi) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

func (f *fakeNFCSettlementEfi) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "fake-token"})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v3/gn/pix/"):
		f.mu.Lock()
		f.puts++
		timeout := f.timeoutOnPut
		status := f.putStatus
		e2e := f.putE2E
		f.mu.Unlock()
		if timeout {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			time.Sleep(200 * time.Millisecond)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"idEnvio": pathTail(r.URL.Path),
			"e2eId":   e2e,
			"status":  status,
			"valor":   "100.00",
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/gn/pix/enviados/id-envio/"):
		f.mu.Lock()
		f.gets++
		lookup := fakeEfiLookup{status: "EM_PROCESSAMENTO", e2e: "E123"}
		if len(f.getStatuses) > 0 {
			lookup = f.getStatuses[0]
			f.getStatuses = f.getStatuses[1:]
		}
		f.mu.Unlock()
		if lookup.httpStatus != 0 && lookup.httpStatus != http.StatusOK {
			http.Error(w, http.StatusText(lookup.httpStatus), lookup.httpStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"idEnvio": pathTail(r.URL.Path),
			"e2eId":   firstNonEmpty(lookup.e2e, "E123"),
			"status":  firstNonEmpty(lookup.status, "EM_PROCESSAMENTO"),
			"valor":   "100.00",
		})
	default:
		http.NotFound(w, r)
	}
}

func newTestNFCSettlementWorker(store *fakeNFCSettlementStore, baseURL string, timeout time.Duration) *NFCMerchantSettlementWorker {
	return &NFCMerchantSettlementWorker{
		bus:   NewEventBus(),
		store: store,
		cfg: &config.Config{
			EfiClientID:                             "client",
			EfiClientSecret:                         "secret",
			EfiPixKey:                               "payer@example.com",
			EfiApiBaseURL:                           baseURL,
			NFCSettlementMode:                       "efi",
			NFCSettlementAmbiguousGraceSec:          900,
			NFCSettlementNotFoundMinReconciliations: 3,
		},
		client: &http.Client{Timeout: timeout},
		dlq:    NewDLQ(100, nil),
		sem:    make(chan struct{}, 4),
	}
}

func pathTail(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return path
	}
	return path[idx+1:]
}
