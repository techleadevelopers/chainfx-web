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
	"payment-gateway/internal/models"
)

func TestSellPayoutHappyPathCompletesWithOneSubmit(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	efi.putStatus = "REALIZADO"
	store := newFakeSellPayoutStore()
	worker := newTestSellPayoutWorker(store, efi.URL, 200*time.Millisecond)

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	if efi.putCount() != 1 {
		t.Fatalf("expected one submit, got %d", efi.putCount())
	}
	got := store.snapshot()
	if got.exec == nil || got.exec.Status != "completed" {
		t.Fatalf("expected completed execution, got %#v", got.exec)
	}
	if got.order.Status != models.StatusConcluida {
		t.Fatalf("expected concluded order, got %s", got.order.Status)
	}
}

func TestSellPayoutTimeoutAfterProviderAcceptPersistsUnknownNoRetry(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	store := newFakeSellPayoutStore()
	worker := newTestSellPayoutWorker(store, efi.URL, 500*time.Millisecond)

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	got := store.snapshot()
	if efi.putCount() != 1 {
		t.Fatalf("expected one submit, got %d", efi.putCount())
	}
	if got.exec == nil || got.exec.Status != "provider_unknown" {
		t.Fatalf("expected provider_unknown, got %#v", got.exec)
	}
	if got.exec.AttemptCount != 1 {
		t.Fatalf("expected one attempt, got %d", got.exec.AttemptCount)
	}
}

func TestSellPayoutWorkerAgainReconcilesBeforeSubmit(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeSellPayoutLookup{{status: "EM_PROCESSAMENTO"}}
	store := newFakeSellPayoutStore()
	worker := newTestSellPayoutWorker(store, efi.URL, 500*time.Millisecond)

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	efi.timeoutOnPut = false
	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	if efi.putCount() != 1 {
		t.Fatalf("expected no second submit, got %d", efi.putCount())
	}
	if efi.getCount() != 1 {
		t.Fatalf("expected one lookup, got %d", efi.getCount())
	}
	if got := store.snapshot().exec.Status; got != "provider_pending" {
		t.Fatalf("expected provider_pending, got %s", got)
	}
}

func TestSellPayoutTimeoutThenCompletedLookupCompletes(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeSellPayoutLookup{{status: "REALIZADO", e2e: "E999"}}
	store := newFakeSellPayoutStore()
	worker := newTestSellPayoutWorker(store, efi.URL, 500*time.Millisecond)

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	efi.timeoutOnPut = false
	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	got := store.snapshot()
	if efi.putCount() != 1 {
		t.Fatalf("expected no second submit, got %d", efi.putCount())
	}
	if got.exec.Status != "completed" || got.order.Status != models.StatusConcluida {
		t.Fatalf("expected completed execution/order, got %s/%s", got.exec.Status, got.order.Status)
	}
}

func TestSellPayoutTimeout404DoesNotResubmit(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeSellPayoutLookup{{httpStatus: http.StatusNotFound}}
	store := newFakeSellPayoutStore()
	worker := newTestSellPayoutWorker(store, efi.URL, 500*time.Millisecond)

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	efi.timeoutOnPut = false
	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	if efi.putCount() != 1 {
		t.Fatalf("404 after ambiguous submit must not resubmit, got %d", efi.putCount())
	}
	if got := store.snapshot().exec.Status; got != "provider_unknown" {
		t.Fatalf("expected provider_unknown, got %s", got)
	}
}

func TestSellPayoutRepeated404ManualReviewAfterGrace(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	efi.timeoutOnPut = true
	efi.getStatuses = []fakeSellPayoutLookup{
		{httpStatus: http.StatusNotFound},
		{httpStatus: http.StatusNotFound},
		{httpStatus: http.StatusNotFound},
	}
	store := newFakeSellPayoutStore()
	worker := newTestSellPayoutWorker(store, efi.URL, 500*time.Millisecond)
	worker.cfg.SellPayoutAmbiguousGraceSec = 1
	worker.cfg.SellPayoutNotFoundMinReconciliations = 3

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	store.forceAmbiguousAge(2 * time.Second)
	efi.timeoutOnPut = false
	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	got := store.snapshot()
	if efi.putCount() != 1 {
		t.Fatalf("repeated 404 must not resubmit, got %d", efi.putCount())
	}
	if got.exec.Status != "manual_review" || got.order.Status != models.StatusIncidenteValidacao {
		t.Fatalf("expected manual review, got %s/%s", got.exec.Status, got.order.Status)
	}
}

func TestSellPayoutTwoWorkersProduceOneExecutionAndOneSubmit(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	efi.putStatus = "EM_PROCESSAMENTO"
	store := newFakeSellPayoutStore()
	worker := newTestSellPayoutWorker(store, efi.URL, 200*time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	}()
	go func() {
		defer wg.Done()
		worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})
	}()
	wg.Wait()

	if efi.putCount() != 1 {
		t.Fatalf("expected one submit, got %d", efi.putCount())
	}
	if store.executionCreateCount != 1 {
		t.Fatalf("expected one execution, got %d", store.executionCreateCount)
	}
}

func TestSellPayoutCrashRestartUsesSameIDEnvio(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	store := newFakeSellPayoutStore()
	store.order.Status = models.StatusProcessandoPayout
	store.exec = &database.SellPayoutExecution{
		ID:                     "exec_sell_order_test",
		OrderID:                store.order.ID,
		Provider:               "efi",
		ProviderIDempotencyKey: "sell-payout-" + store.order.ID,
		ProviderIDEnvio:        store.order.ID,
		AmountBRLMinor:         10000,
		RecipientReference:     store.order.PixKey,
		Status:                 "submit_started",
		AttemptCount:           0,
		SubmitOutcome:          "started",
		CreatedAt:              time.Now(),
		UpdatedAt:              time.Now(),
	}
	worker := newTestSellPayoutWorker(store, efi.URL, 200*time.Millisecond)

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	if efi.lastIDEnvio() != store.order.ID {
		t.Fatalf("expected stable idEnvio %q, got %q", store.order.ID, efi.lastIDEnvio())
	}
}

func TestSellPayoutCompletedTerminalDoesNotSubmitAgain(t *testing.T) {
	efi := newFakeSellPayoutEfi(t)
	defer efi.Close()
	store := newFakeSellPayoutStore()
	store.order.Status = models.StatusProcessandoPayout
	store.exec = &database.SellPayoutExecution{
		ID:                 "exec_sell_order_test",
		OrderID:            store.order.ID,
		ProviderIDEnvio:    store.order.ID,
		AmountBRLMinor:     10000,
		RecipientReference: store.order.PixKey,
		Status:             "completed",
		AttemptCount:       1,
	}
	worker := newTestSellPayoutWorker(store, efi.URL, 50*time.Millisecond)

	worker.processPayoutHardened(context.Background(), Event{Type: "payout.requested", OrderID: store.order.ID})

	if efi.putCount() != 0 {
		t.Fatalf("completed execution must not submit again, got %d", efi.putCount())
	}
}

type fakeSellPayoutSnapshot struct {
	order models.Order
	exec  *database.SellPayoutExecution
}

type fakeSellPayoutStore struct {
	mu                   sync.Mutex
	order                models.Order
	exec                 *database.SellPayoutExecution
	claimed              bool
	executionCreateCount int
}

func newFakeSellPayoutStore() *fakeSellPayoutStore {
	return &fakeSellPayoutStore{order: models.Order{
		ID:        "sell_order_test",
		Status:    models.StatusPago,
		PayoutBRL: 100,
		PixKey:    "seller@example.com",
	}}
}

func (s *fakeSellPayoutStore) snapshot() fakeSellPayoutSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cp *database.SellPayoutExecution
	if s.exec != nil {
		next := *s.exec
		cp = &next
	}
	return fakeSellPayoutSnapshot{order: s.order, exec: cp}
}

func (s *fakeSellPayoutStore) forceAmbiguousAge(age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now().Add(-age)
	s.exec.FirstAmbiguousAt = &at
	s.exec.SubmitStartedAt = &at
}

func (s *fakeSellPayoutStore) GetOrder(context.Context, string) (*models.Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.order
	return &cp, nil
}

func (s *fakeSellPayoutStore) ClaimOrderForPayout(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.order.Status != models.StatusPago || s.claimed {
		return false, nil
	}
	s.claimed = true
	s.order.Status = models.StatusProcessandoPayout
	return true, nil
}

func (s *fakeSellPayoutStore) ClaimOrderForManualPayout(context.Context, string, map[string]any) (bool, error) {
	return false, nil
}

func (s *fakeSellPayoutStore) UpdateOrderStatus(_ context.Context, _ string, status string, _ map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order.Status = models.OrderStatus(status)
	return nil
}

func (s *fakeSellPayoutStore) OpenOrderIncident(context.Context, string, string, string, string, any) error {
	return nil
}

func (s *fakeSellPayoutStore) EnsureSellPayoutExecution(_ context.Context, orderID, provider, providerIDEnvio string, amountBRLMinor int64, recipientReference string) (*database.SellPayoutExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exec == nil {
		s.executionCreateCount++
		now := time.Now()
		s.exec = &database.SellPayoutExecution{
			ID:                     "exec_" + orderID,
			OrderID:                orderID,
			Provider:               provider,
			ProviderIDempotencyKey: "sell-payout-" + providerIDEnvio,
			ProviderIDEnvio:        providerIDEnvio,
			AmountBRLMinor:         amountBRLMinor,
			RecipientReference:     recipientReference,
			Status:                 "pending",
			SubmitOutcome:          "not_submitted",
			CreatedAt:              now,
			UpdatedAt:              now,
			NextAttemptAt:          now,
		}
	}
	cp := *s.exec
	return &cp, nil
}

func (s *fakeSellPayoutStore) GetSellPayoutExecutionByOrder(context.Context, string) (*database.SellPayoutExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exec == nil {
		return nil, nil
	}
	cp := *s.exec
	return &cp, nil
}

func (s *fakeSellPayoutStore) ListDueSellPayoutExecutions(context.Context, int) ([]database.SellPayoutExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exec == nil {
		return nil, nil
	}
	switch s.exec.Status {
	case "pending", "submit_started", "submitted", "provider_pending", "provider_unknown":
		if !s.exec.NextAttemptAt.IsZero() && s.exec.NextAttemptAt.After(time.Now()) {
			return nil, nil
		}
		cp := *s.exec
		cp.NextAttemptAt = time.Now().Add(30 * time.Second)
		s.exec.NextAttemptAt = cp.NextAttemptAt
		return []database.SellPayoutExecution{cp}, nil
	default:
		return nil, nil
	}
}

func (s *fakeSellPayoutStore) MarkSellPayoutSubmitStarted(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.exec.Status = "submit_started"
	s.exec.AttemptCount++
	s.exec.SubmitStartedAt = &now
	s.exec.SubmitOutcome = "started"
	s.exec.UpdatedAt = now
	return nil
}

func (s *fakeSellPayoutStore) MarkSellPayoutSubmitted(_ context.Context, _, providerReference, providerE2EID, providerStatus string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.exec.Status = "provider_pending"
	s.exec.ProviderReference = providerReference
	s.exec.ProviderE2EID = providerE2EID
	s.exec.LastError = providerStatus
	s.exec.SubmitOutcome = "confirmed"
	s.exec.SubmitCompletedAt = &now
	return nil
}

func (s *fakeSellPayoutStore) MarkSellPayoutProviderUnknown(_ context.Context, _, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.exec.Status = "provider_unknown"
	s.exec.SubmitOutcome = "ambiguous"
	if s.exec.FirstAmbiguousAt == nil {
		s.exec.FirstAmbiguousAt = &now
	}
	s.exec.LastError = errMsg
	s.claimed = false
	return nil
}

func (s *fakeSellPayoutStore) MarkSellPayoutFailed(_ context.Context, _, _ string, errMsg string, manual bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if manual {
		s.exec.Status = "manual_review"
		s.order.Status = models.StatusIncidenteValidacao
	} else {
		s.exec.Status = "failed"
		s.order.Status = models.StatusErro
	}
	s.exec.LastError = errMsg
	s.claimed = false
	return nil
}

func (s *fakeSellPayoutStore) MarkSellPayoutReconcileNotFound(_ context.Context, _, errMsg string, _ time.Duration, minAttempts int, grace time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exec.ConsecutiveNotFound++
	now := time.Now()
	s.exec.LastReconciledAt = &now
	s.exec.LastError = errMsg
	start := s.exec.FirstAmbiguousAt
	if start != nil && time.Since(*start) >= grace && s.exec.ConsecutiveNotFound >= minAttempts {
		s.exec.Status = "manual_review"
		s.order.Status = models.StatusIncidenteValidacao
	}
	s.claimed = false
	return nil
}

func (s *fakeSellPayoutStore) ApplySellPayoutProviderEvent(_ context.Context, idEnvio, providerReference, providerE2EID, providerStatus string, _ map[string]any) (bool, *database.SellPayoutExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	duplicate := s.exec.Status == "completed"
	s.exec.ProviderIDEnvio = firstNonEmpty(s.exec.ProviderIDEnvio, idEnvio)
	s.exec.ProviderReference = providerReference
	s.exec.ProviderE2EID = providerE2EID
	switch strings.ToUpper(providerStatus) {
	case "REALIZADO", "CONCLUIDA", "COMPLETED", "CONFIRMED":
		s.exec.Status = "completed"
		s.exec.SubmitOutcome = "confirmed"
		s.order.Status = models.StatusConcluida
	default:
		s.exec.Status = "provider_pending"
	}
	s.exec.ConsecutiveNotFound = 0
	s.claimed = false
	cp := *s.exec
	return duplicate, &cp, nil
}

type fakeSellPayoutLookup struct {
	httpStatus int
	status     string
	e2e        string
}

type fakeSellPayoutEfi struct {
	*httptest.Server
	mu           sync.Mutex
	puts         int
	gets         int
	timeoutOnPut bool
	putStatus    string
	putE2E       string
	lastEnvio    string
	getStatuses  []fakeSellPayoutLookup
}

func newFakeSellPayoutEfi(t *testing.T) *fakeSellPayoutEfi {
	t.Helper()
	f := &fakeSellPayoutEfi{putStatus: "EM_PROCESSAMENTO", putE2E: "E123"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeSellPayoutEfi) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

func (f *fakeSellPayoutEfi) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

func (f *fakeSellPayoutEfi) lastIDEnvio() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastEnvio
}

func (f *fakeSellPayoutEfi) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "fake-token"})
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v3/gn/pix/"):
		idEnvio := pathTail(r.URL.Path)
		f.mu.Lock()
		f.puts++
		f.lastEnvio = idEnvio
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
		_ = json.NewEncoder(w).Encode(map[string]string{"idEnvio": idEnvio, "e2eId": e2e, "status": status, "valor": "100.00"})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/gn/pix/enviados/id-envio/"):
		idEnvio := pathTail(r.URL.Path)
		f.mu.Lock()
		f.gets++
		lookup := fakeSellPayoutLookup{status: "EM_PROCESSAMENTO", e2e: "E123"}
		if len(f.getStatuses) > 0 {
			lookup = f.getStatuses[0]
			f.getStatuses = f.getStatuses[1:]
		}
		f.mu.Unlock()
		if lookup.httpStatus != 0 && lookup.httpStatus != http.StatusOK {
			http.Error(w, http.StatusText(lookup.httpStatus), lookup.httpStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"idEnvio": idEnvio, "e2eId": firstNonEmpty(lookup.e2e, "E123"), "status": firstNonEmpty(lookup.status, "EM_PROCESSAMENTO"), "valor": "100.00"})
	default:
		http.NotFound(w, r)
	}
}

func newTestSellPayoutWorker(store *fakeSellPayoutStore, baseURL string, timeout time.Duration) *PayoutWorker {
	return &PayoutWorker{
		bus:   NewEventBus(),
		store: store,
		cfg: &config.Config{
			Environment:                          "production",
			SellPayoutMode:                       "efi",
			EfiClientID:                          "client",
			EfiClientSecret:                      "secret",
			EfiPixKey:                            "payer@example.com",
			EfiApiBaseURL:                        baseURL,
			SellPayoutAmbiguousGraceSec:          900,
			SellPayoutNotFoundMinReconciliations: 3,
		},
		client: &http.Client{Timeout: timeout},
		dlq:    NewDLQ(100, nil),
		sem:    make(chan struct{}, 4),
	}
}
