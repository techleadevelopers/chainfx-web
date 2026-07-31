package mobile

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/workers"

	_ "github.com/lib/pq"
)

const giftCardIntegrationEnv = "CHAINFX_GIFTCARD_DB_TEST_URL"

type giftCardFlowEnv struct {
	t         *testing.T
	ctx       context.Context
	db        *database.DB
	server    *Server
	userID    string
	wallet    string
	productID string
	bitrefill *fakeBitrefillServer
}

type giftCardBalance struct {
	Available int64
	Locked    int64
}

type giftCardFlowState struct {
	OrderID              string
	IntentStatus         string
	ExecutionStatus      string
	OrderStatus          string
	ProviderReference    string
	ProviderIDKey        string
	ReserveLedger        int
	CaptureLedger        int
	ReleaseLedger        int
	ProviderRefundLedger int
	IntentCount          int
	ExecutionCount       int
	OrderCount           int
}

func TestGiftCardPaymentEngineLocalValidation_HappyPathDoubleTapAndRecovery(t *testing.T) {
	env := newGiftCardFlowEnv(t, "main")
	before := env.balance()

	quote := env.createQuote(30_00)
	orderID := env.purchase(quote, "same-tap-key", http.StatusAccepted)
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 30_000_000})

	env.processOutbox()
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 0})
	state := env.state(orderID)
	if state.IntentStatus != "completed" || state.ExecutionStatus != "completed" || state.OrderStatus != "delivered" {
		t.Fatalf("happy path final states mismatch: %+v", state)
	}
	if state.ReserveLedger != 1 || state.CaptureLedger != 1 || state.ProviderReference == "" {
		t.Fatalf("happy path ledger/reference mismatch: %+v", state)
	}
	if got := env.bitrefill.postCount(); got != 1 {
		t.Fatalf("happy path must POST Bitrefill once, got %d", got)
	}

	after := env.balance()
	if before.Available != 100_000_000 || before.Locked != 0 || after.Available != 70_000_000 || after.Locked != 0 {
		t.Fatalf("unexpected balance before=%+v after=%+v", before, after)
	}

	env.applyResult(orderID, "delivered", "br_inv_main", "br_order_main")
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 0})
	if state := env.state(orderID); state.CaptureLedger != 1 {
		t.Fatalf("duplicate delivered must not create second capture: %+v", state)
	}

	env.applyResult(orderID, "refunded", "br_inv_main", "br_order_main")
	env.applyResult(orderID, "refunded", "br_inv_main", "br_order_main")
	env.applyResult(orderID, "refunded", "br_inv_main", "br_order_main")
	env.assertBalance(giftCardBalance{Available: 100_000_000, Locked: 0})
	if state := env.state(orderID); state.ProviderRefundLedger != 1 || state.IntentStatus != "refunded" {
		t.Fatalf("duplicate refund must credit once and mark refunded: %+v", state)
	}
}

func TestGiftCardPaymentEngineLocalValidation_DoubleTapSameIdempotencyKey(t *testing.T) {
	env := newGiftCardFlowEnv(t, "doubletap")
	quote := env.createQuote(30_00)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = env.purchaseAny(quote, "double-tap-key")
		}()
	}
	wg.Wait()
	env.processOutbox()

	orderID := env.onlyOrderID()
	state := env.state(orderID)
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 0})
	if state.IntentCount != 1 || state.ExecutionCount != 1 || state.OrderCount != 1 || state.ReserveLedger != 1 || state.CaptureLedger != 1 {
		t.Fatalf("double tap duplicated economic objects: %+v", state)
	}
	if got := env.bitrefill.postCount(); got != 1 {
		t.Fatalf("double tap must POST Bitrefill once, got %d", got)
	}
}

func TestGiftCardPaymentEngineLocalValidation_ConcurrentPurchasesReserveOnlyOne(t *testing.T) {
	env := newGiftCardFlowEnv(t, "concurrent")
	quoteA := env.createQuote(80_00)
	quoteB := env.createQuote(80_00)

	var wg sync.WaitGroup
	for i, quote := range []map[string]any{quoteA, quoteB} {
		wg.Add(1)
		go func(i int, quote map[string]any) {
			defer wg.Done()
			_ = env.purchaseAny(quote, fmt.Sprintf("concurrent-%d", i))
		}(i, quote)
	}
	wg.Wait()

	bal := env.balance()
	if bal.Available < 0 || bal.Locked < 0 || bal.Locked > 100_000_000 {
		t.Fatalf("balance invariant violated after concurrent purchase: %+v", bal)
	}
	if got := env.countRows("mobile_wallet_ledger_entries", "source='gift_card_purchase_lock'"); got != 1 {
		t.Fatalf("expected only one reserve ledger, got %d", got)
	}
	if bal.Available != 20_000_000 || bal.Locked != 80_000_000 {
		t.Fatalf("expected one 80 USDT reserve, got %+v", bal)
	}
}

func TestGiftCardPaymentEngineLocalValidation_QuoteReplayAndExpiredQuote(t *testing.T) {
	env := newGiftCardFlowEnv(t, "quotes")
	quote := env.createQuote(30_00)
	env.purchase(quote, "first-use", http.StatusAccepted)
	env.purchaseExpect(quote, "quote-replay", http.StatusConflict)

	expired := env.createQuote(30_00)
	env.expireQuote(expired["quote_id"].(string))
	env.purchaseExpect(expired, "expired-quote", http.StatusConflict)

	if got := env.countRows("mobile_wallet_ledger_entries", "source='gift_card_purchase_lock'"); got != 1 {
		t.Fatalf("quote replay/expired quote created extra reserve ledger: %d", got)
	}
	if got := env.bitrefill.postCount(); got != 0 {
		t.Fatalf("Bitrefill must not be called before worker, got %d", got)
	}
}

func TestGiftCardPaymentEngineLocalValidation_DefinitiveFailureReleaseIdempotent(t *testing.T) {
	env := newGiftCardFlowEnv(t, "failure")
	env.bitrefill.invoiceStatus.Store("failed")
	quote := env.createQuote(30_00)
	orderID := env.purchase(quote, "terminal-failure", http.StatusAccepted)
	env.processOutbox()
	env.assertBalance(giftCardBalance{Available: 100_000_000, Locked: 0})
	env.applyResult(orderID, "failed", "br_inv_failure", "br_order_failure")
	env.assertBalance(giftCardBalance{Available: 100_000_000, Locked: 0})
	if state := env.state(orderID); state.ReleaseLedger != 1 || state.IntentStatus != "failed" || state.ExecutionStatus != "failed" {
		t.Fatalf("definitive failure/release mismatch: %+v", state)
	}
}

func TestGiftCardPaymentEngineLocalValidation_Retryable500DoesNotCreateSecondEconomicPurchase(t *testing.T) {
	env := newGiftCardFlowEnv(t, "retry500")
	env.bitrefill.failFirstInvoice.Store(true)
	quote := env.createQuote(30_00)
	orderID := env.purchase(quote, "retryable-500", http.StatusAccepted)

	env.processOutbox()
	stateAfter500 := env.state(orderID)
	if stateAfter500.ReserveLedger != 1 || stateAfter500.IntentCount != 1 || stateAfter500.ExecutionCount != 1 {
		t.Fatalf("500 retry created duplicate local economic object: %+v", stateAfter500)
	}
	env.makeOutboxDue()
	env.processOutbox()
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 0})
	state := env.state(orderID)
	if state.ProviderIDKey == "" || state.ProviderIDKey != stateAfter500.ProviderIDKey {
		t.Fatalf("provider idempotency key changed across retry: before=%q after=%q", stateAfter500.ProviderIDKey, state.ProviderIDKey)
	}
	if got := env.bitrefill.postCount(); got > 2 {
		t.Fatalf("retryable 500 made too many purchase POSTs: %d", got)
	}
}

func TestGiftCardPaymentEngineLocalValidation_TimeoutUnknownNoSecondBlindPost(t *testing.T) {
	env := newGiftCardFlowEnv(t, "timeout")
	env.bitrefill.timeoutFirstInvoice.Store(true)
	quote := env.createQuote(30_00)
	orderID := env.purchase(quote, "timeout-submit", http.StatusAccepted)

	env.processOutbox()
	if got := env.bitrefill.postCount(); got != 1 {
		t.Fatalf("timeout submit must have one POST, got %d", got)
	}
	state := env.state(orderID)
	if state.ExecutionStatus != "provider_unknown" || state.OrderStatus != "provider_unknown" {
		t.Fatalf("timeout must leave provider_unknown, got %+v", state)
	}
	env.makeOutboxDue()
	env.processOutbox()
	if got := env.bitrefill.postCount(); got != 1 {
		t.Fatalf("provider_unknown must not blind POST again, got %d", got)
	}
}

func TestGiftCardPaymentEngineLocalValidation_ProviderUnknownWithAndWithoutReference(t *testing.T) {
	env := newGiftCardFlowEnv(t, "unknownref")
	orderID := env.purchase(env.createQuote(30_00), "unknown-with-ref", http.StatusAccepted)
	env.setProviderUnknown(orderID, "br_order_unknown")
	env.bitrefill.orderStatus.Store("delivered")
	env.processOutbox()
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 0})
	if state := env.state(orderID); state.IntentStatus != "completed" || state.CaptureLedger != 1 {
		t.Fatalf("provider_unknown with reference did not reconcile to completed: %+v", state)
	}
	if got := env.bitrefill.postCount(); got != 0 {
		t.Fatalf("reconciliation with reference must not purchase POST, got %d", got)
	}

	env2 := newGiftCardFlowEnv(t, "unknownnoref")
	orderID2 := env2.purchase(env2.createQuote(30_00), "unknown-no-ref", http.StatusAccepted)
	env2.setProviderUnknown(orderID2, "")
	env2.processOutbox()
	env2.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 30_000_000})
	if state := env2.state(orderID2); state.ExecutionStatus != "manual_review" || state.IntentStatus != "manual_review" || state.CaptureLedger != 0 || state.ReleaseLedger != 0 {
		t.Fatalf("provider_unknown without reference must go conservative manual_review: %+v", state)
	}
	if got := env2.bitrefill.postCount(); got != 0 {
		t.Fatalf("provider_unknown without reference must not POST, got %d", got)
	}
}

func TestGiftCardPaymentEngineLocalValidation_DuplicateWorkerAndCrashRecovery(t *testing.T) {
	env := newGiftCardFlowEnv(t, "workerdup")
	orderID := env.purchase(env.createQuote(30_00), "worker-dup", http.StatusAccepted)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.processOutbox()
		}()
	}
	wg.Wait()
	if got := env.bitrefill.postCount(); got != 1 {
		t.Fatalf("duplicate commerce workers must make one purchase POST, got %d", got)
	}
	if state := env.state(orderID); state.CaptureLedger != 1 || state.ReserveLedger != 1 {
		t.Fatalf("duplicate worker economic state mismatch: %+v", state)
	}

	env2 := newGiftCardFlowEnv(t, "crashbeforepost")
	orderID2 := env2.purchase(env2.createQuote(30_00), "crash-before-post", http.StatusAccepted)
	env2.processOutbox()
	if state := env2.state(orderID2); state.IntentStatus != "completed" || state.ReserveLedger != 1 || state.CaptureLedger != 1 {
		t.Fatalf("restart after reserve/execution pending failed: %+v", state)
	}
}

func TestGiftCardPaymentEngineLocalValidation_RefundConcurrentCreditsOnce(t *testing.T) {
	env := newGiftCardFlowEnv(t, "refundrace")
	orderID := env.purchase(env.createQuote(30_00), "refund-race", http.StatusAccepted)
	env.processOutbox()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.applyResult(orderID, "refunded", "br_inv_refundrace", "br_order_refundrace")
		}()
	}
	wg.Wait()
	env.assertBalance(giftCardBalance{Available: 100_000_000, Locked: 0})
	if state := env.state(orderID); state.ProviderRefundLedger != 1 {
		t.Fatalf("concurrent refund credited more than once: %+v", state)
	}
}

func TestGiftCardPaymentEngineLocalValidation_LateFailureAfterDeliveryDoesNotReleaseOtherHold(t *testing.T) {
	env := newGiftCardFlowEnv(t, "latefail")
	deliveredOrderID := env.purchase(env.createQuote(30_00), "latefail-delivered", http.StatusAccepted)
	env.processOutbox()

	heldOrderID := env.purchase(env.createQuote(40_00), "latefail-held", http.StatusAccepted)
	env.assertBalance(giftCardBalance{Available: 30_000_000, Locked: 40_000_000})

	env.applyResult(deliveredOrderID, "failed", "br_inv_latefail", "br_order_latefail")
	env.assertBalance(giftCardBalance{Available: 30_000_000, Locked: 40_000_000})
	if delivered := env.state(deliveredOrderID); delivered.ReleaseLedger != 0 || delivered.CaptureLedger != 1 || delivered.OrderStatus != "delivered" {
		t.Fatalf("late failure mutated delivered order: %+v", delivered)
	}
	if held := env.state(heldOrderID); held.ReleaseLedger != 0 || held.CaptureLedger != 0 || held.OrderStatus != "funds_locked" {
		t.Fatalf("late failure consumed another order hold: %+v", held)
	}
}

func TestGiftCardPaymentEngineLocalValidation_LedgerFailureAtomicity(t *testing.T) {
	env := newGiftCardFlowEnv(t, "ledgerfail")
	orderID := env.purchase(env.createQuote(30_00), "ledger-fail", http.StatusAccepted)

	tx, err := env.db.SQL.BeginTx(env.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := requestWithContext(env.ctx)
	err = txCaptureGiftCardLocked(req, tx, env.wallet, orderID, 30_000_000)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("setup capture failed: %v", err)
	}
	if _, err := tx.ExecContext(env.ctx, `INSERT INTO mobile_wallet_ledger_entries
		(id,wallet_address,network,asset,source,reference_id,available_delta_micro,locked_delta_micro)
		VALUES ($1,$2,'BSC','USDT','gift_card_purchase_capture',$3,0,-30000000)`,
		"mwle_"+mobilePayHash(orderID + ":gift_card_purchase_capture")[:24], env.wallet, orderID); err == nil {
		_ = tx.Rollback()
		t.Fatal("expected duplicate ledger insert to fail")
	}
	_ = tx.Rollback()
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 30_000_000})
}

func TestGiftCardPaymentEngineLocalValidation_SmallLocalLoad(t *testing.T) {
	env := newGiftCardFlowEnv(t, "load")
	const attempts = 50
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		quote := env.createQuote(3_00)
		wg.Add(1)
		go func(i int, quote map[string]any) {
			defer wg.Done()
			_ = env.purchaseAny(quote, fmt.Sprintf("load-%02d", i))
		}(i, quote)
	}
	wg.Wait()
	bal := env.balance()
	if bal.Available < 0 || bal.Locked < 0 {
		t.Fatalf("load test negative balance: %+v", bal)
	}
	reserves := env.countRows("mobile_wallet_ledger_entries", "source='gift_card_purchase_lock'")
	if reserves != 33 {
		t.Fatalf("expected 33 approved 3 USDT reserves from 100 USDT, got %d balance=%+v", reserves, bal)
	}
}

func TestMobileTopupPaymentEngineValidation_PhonePriceIdempotencyAndProviderOnce(t *testing.T) {
	env := newGiftCardFlowEnv(t, "topup")
	env.seedTopupProduct()

	invalidPhone := env.createTopupQuoteExpect("11999999999", 1_00, http.StatusBadRequest)
	if invalidPhone != nil {
		t.Fatalf("invalid denomination unexpectedly returned quote: %+v", invalidPhone)
	}

	quote := env.createTopupQuote("11999999999", 30_00)
	if quote["order_type"] != "mobile_topup" {
		t.Fatalf("expected mobile_topup quote, got %+v", quote)
	}
	orderID := env.purchaseTopup(quote, "11999999999", "topup-key", http.StatusAccepted)
	env.processOutbox()
	if got := env.bitrefill.postCount(); got != 1 {
		t.Fatalf("topup provider submit count = %d, want 1", got)
	}
	state := env.state(orderID)
	if state.IntentCount != 1 || state.ExecutionCount != 1 || state.OrderCount != 1 || state.ReserveLedger != 1 || state.CaptureLedger != 1 {
		t.Fatalf("topup economic invariants failed: %+v", state)
	}

	env.purchaseTopupExpect(quote, "21999999999", "topup-key", http.StatusConflict)
	env.assertBalance(giftCardBalance{Available: 70_000_000, Locked: 0})
	if got := env.bitrefill.postCount(); got != 1 {
		t.Fatalf("payload mismatch must not resubmit provider, got %d", got)
	}
}

func TestMobileTopupPaymentEngineValidation_QuotePhoneSnapshotAndReplay(t *testing.T) {
	env := newGiftCardFlowEnv(t, "topupquote")
	env.seedTopupProduct()

	quote := env.createTopupQuote("11999999999", 30_00)
	env.purchaseTopupExpect(quote, "21999999999", "wrong-phone", http.StatusConflict)
	env.purchaseTopup(quote, "11999999999", "right-phone", http.StatusAccepted)
	env.purchaseTopupExpect(quote, "11999999999", "quote-replay-topup", http.StatusConflict)
	if got := env.countRows("mobile_wallet_ledger_entries", "source='gift_card_purchase_lock'"); got != 1 {
		t.Fatalf("topup quote replay/phone mismatch created extra reserve ledger: %d", got)
	}
	if got := env.bitrefill.postCount(); got != 0 {
		t.Fatalf("Bitrefill must not be called before worker, got %d", got)
	}
}

func newGiftCardFlowEnv(t *testing.T, name string) *giftCardFlowEnv {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(giftCardIntegrationEnv))
	if url == "" {
		t.Skipf("set %s to a local disposable Postgres URL to run gift-card Payment Engine integration tests", giftCardIntegrationEnv)
	}
	ctx := context.Background()
	sqlDB, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(20)
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("test database unavailable: %v", err)
	}
	db := &database.DB{SQL: sqlDB}
	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("base schema: %v", err)
	}
	if err := mobileDB(db).InitSchema(ctx); err != nil {
		t.Fatalf("mobile schema: %v", err)
	}

	fake := newFakeBitrefillServer(t)
	t.Setenv("BITREFILL_BASE_URL", fake.URL)
	t.Setenv("BITREFILL_API_KEY", "test-bitrefill-key")
	t.Setenv("BITREFILL_LIVE_PURCHASES_ENABLED", "true")
	t.Setenv("BITREFILL_CATALOG_SYNC_ENABLED", "false")
	t.Setenv("BITREFILL_TIMEOUT_SECONDS", "1")
	t.Setenv("GIFT_CARD_PROVIDER_MODE", "live")

	priceWorker := workers.NewPriceWorker(workers.NewEventBus())
	setTestPrice(priceWorker, "BRL", 1)
	prefix := "gcpe_" + name + "_" + mobilePayHash(t.Name() + time.Now().String())[:10]
	userID := "00000000-0000-0000-0000-" + mobilePayHash(prefix)[:12]
	wallet := "0x" + mobilePayHash(prefix + ":wallet")[:40]
	productID := prefix + "_product"
	server := &Server{
		cfg:     &config.Config{LGPDSecret: "0123456789abcdef0123456789abcdef", NFCFeeBps: 0, M2MPixFeeBps: 0},
		db:      db,
		workers: &workers.WorkerManager{PriceWorker: priceWorker},
		cache:   make(map[string]mobileCacheEntry),
	}

	env := &giftCardFlowEnv{t: t, ctx: ctx, db: db, server: server, userID: userID, wallet: wallet, productID: productID, bitrefill: fake}
	env.seed()
	t.Cleanup(env.cleanup)
	return env
}

func (e *giftCardFlowEnv) seed() {
	e.t.Helper()
	if _, err := e.db.SQL.ExecContext(e.ctx, `
INSERT INTO users (id,email,password_hash,full_name,wallet_address,kyc_status)
VALUES ($1::uuid,$2,'test','Gift Card Test',$3,'approved')`,
		e.userID, e.userID+"@chainfx.local", e.wallet); err != nil {
		e.t.Fatalf("seed user: %v", err)
	}
	if _, err := e.db.SQL.ExecContext(e.ctx, `
INSERT INTO kyc_requests (user_id, level, status, submitted_at, reviewed_at)
VALUES ($1::uuid, 1, 'approved', NOW(), NOW())`, e.userID); err != nil {
		e.t.Fatalf("seed kyc: %v", err)
	}
	if _, err := e.db.AddNFCBalance(e.ctx, database.NFCFundingInput{Wallet: e.wallet, Network: "BSC", Asset: "USDT", DeltaMicro: 100_000_000}); err != nil {
		e.t.Fatalf("seed balance: %v", err)
	}
	if _, err := e.db.SQL.ExecContext(e.ctx, `
INSERT INTO gift_card_providers (id, slug, name, status)
VALUES ('provider_bitrefill','bitrefill','Bitrefill','active')
ON CONFLICT (id) DO UPDATE SET status='active'`); err != nil {
		e.t.Fatalf("seed provider: %v", err)
	}
	if _, err := e.db.SQL.ExecContext(e.ctx, `
INSERT INTO gift_card_provider_products
  (id, provider_id, product_id, external_sku, brand, title, category, currency, face_value_minor, price_brl, product_type, delivery_mode, active, requires_kyc, metadata)
VALUES ($1,'provider_bitrefill',$2,'test-gift-card-code','Test Brand','Test Gift Card','gift_cards','BRL',3000,30.00,'gift_card','code',true,true,'{}'::jsonb)
ON CONFLICT (id) DO UPDATE SET product_id=EXCLUDED.product_id, active=true, updated_at=NOW()`,
		"gcpp_bitrefill_test-gift-card-code", e.productID); err != nil {
		e.t.Fatalf("seed product: %v", err)
	}
	if _, err := e.db.SQL.ExecContext(e.ctx, `
INSERT INTO mobile_gift_cards (id, provider_product_id, brand, title, sort_order, active)
VALUES ($1,'gcpp_bitrefill_test-gift-card-code','Test Brand','Test Gift Card',1,true)
ON CONFLICT (id) DO UPDATE SET active=true, updated_at=NOW()`,
		"mgc_"+e.productID); err != nil {
		e.t.Fatalf("seed mobile gift card: %v", err)
	}
}

func (e *giftCardFlowEnv) seedTopupProduct() {
	e.t.Helper()
	e.productID = "bitrefill_test_mobile_topup_" + mobilePayHash(e.userID)[:10]
	product := commerceProduct{
		ID:                e.productID,
		Provider:          "bitrefill",
		ProviderProductID: "test-mobile-topup-code",
		Type:              "phone_refill",
		Brand:             "Claro Brazil",
		Title:             "Claro Brazil",
		CountryCode:       "BR",
		Currency:          "BRL",
		Categories:        []string{"topups", "mobile"},
		Packages:          []commerceProductPackage{{ID: "pkg30", ValueMinor: 30_00, Value: "30.00"}},
		Available:         true,
	}
	raw, _ := json.Marshal(product)
	if _, err := e.db.SQL.ExecContext(e.ctx, `
INSERT INTO gift_card_provider_products
  (id, provider_id, product_id, external_sku, brand, title, category, currency, face_value_minor, price_brl, product_type, delivery_mode, active, requires_kyc, metadata)
VALUES ('gcpp_bitrefill_test-mobile-topup-code','provider_bitrefill',$1,'test-mobile-topup-code','Claro Brazil','Claro Brazil','topups','BRL',3000,30.00,'phone_refill','topup',true,true,$2::jsonb)
ON CONFLICT (id) DO UPDATE SET product_id=EXCLUDED.product_id, product_type='phone_refill', metadata=EXCLUDED.metadata, active=true, updated_at=NOW()`,
		e.productID, string(raw)); err != nil {
		e.t.Fatalf("seed topup product: %v", err)
	}
	if _, err := e.db.SQL.ExecContext(e.ctx, `
INSERT INTO mobile_gift_cards (id, provider_product_id, brand, title, sort_order, active)
VALUES ($1,'gcpp_bitrefill_test-mobile-topup-code','Claro Brazil','Claro Brazil',1,true)
ON CONFLICT (id) DO UPDATE SET active=true, updated_at=NOW()`,
		"mgc_"+e.productID); err != nil {
		e.t.Fatalf("seed mobile topup: %v", err)
	}
}

func (e *giftCardFlowEnv) cleanup() {
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM operation_ids WHERE user_id=$1::uuid`, e.userID)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM mobile_gift_card_orders WHERE user_id=$1::uuid`, e.userID)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM mobile_payment_quotes WHERE user_id=$1::uuid`, e.userID)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM gift_card_quotes WHERE user_id=$1::uuid`, e.userID)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM mobile_payment_intents WHERE user_id=$1::uuid`, e.userID)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM mobile_wallet_ledger_entries WHERE lower(wallet_address)=lower($1)`, e.wallet)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM nfc_wallet_balances WHERE lower(wallet_address)=lower($1)`, e.wallet)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM kyc_requests WHERE user_id=$1::uuid`, e.userID)
	_, _ = e.db.SQL.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, e.userID)
}

func (e *giftCardFlowEnv) createQuote(unitPriceMinor int64) map[string]any {
	body := map[string]any{"product_id": e.productID, "quantity": 1, "unit_price": brlMinorString(unitPriceMinor), "funding_method": "internal_usdt"}
	rec := e.mobileRequest(http.MethodPost, "/api/mobile/commerce/quote", "", body, e.server.handleCommerceQuote)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("quote status=%d body=%s", rec.Code, rec.Body.String())
	}
	return decodeMap(e.t, rec.Body.Bytes())
}

func (e *giftCardFlowEnv) createTopupQuote(phone string, unitPriceMinor int64) map[string]any {
	e.t.Helper()
	quote := e.createTopupQuoteExpect(phone, unitPriceMinor, http.StatusOK)
	if quote == nil {
		e.t.Fatal("expected topup quote")
	}
	return quote
}

func (e *giftCardFlowEnv) createTopupQuoteExpect(phone string, unitPriceMinor int64, want int) map[string]any {
	e.t.Helper()
	body := map[string]any{"product_id": e.productID, "quantity": 1, "unit_price": brlMinorString(unitPriceMinor), "funding_method": "internal_usdt", "recipient_phone": phone}
	rec := e.mobileRequest(http.MethodPost, "/api/mobile/commerce/quote", "", body, e.server.handleCommerceQuote)
	if rec.Code != want {
		e.t.Fatalf("topup quote want status %d got %d body=%s", want, rec.Code, rec.Body.String())
	}
	if rec.Code < 200 || rec.Code >= 300 {
		return nil
	}
	return decodeMap(e.t, rec.Body.Bytes())
}

func (e *giftCardFlowEnv) purchase(quote map[string]any, key string, want int) string {
	e.t.Helper()
	rec := e.purchaseExpect(quote, key, want)
	if rec.Code < 200 || rec.Code >= 300 {
		return ""
	}
	payload := decodeMap(e.t, rec.Body.Bytes())
	id, _ := payload["order_id"].(string)
	if id == "" {
		e.t.Fatalf("purchase response missing order_id: %s", rec.Body.String())
	}
	return id
}

func (e *giftCardFlowEnv) purchaseAny(quote map[string]any, key string) int {
	return e.purchaseExpect(quote, key, 0).Code
}

func (e *giftCardFlowEnv) purchaseExpect(quote map[string]any, key string, want int) *httptest.ResponseRecorder {
	body := map[string]any{
		"quote_id":       quote["quote_id"],
		"product_id":     e.productID,
		"quantity":       1,
		"unit_price":     quote["amount_brl"],
		"funding_method": "internal_usdt",
	}
	rec := e.mobileRequest(http.MethodPost, "/api/mobile/gift-cards/orders", key, body, e.server.requireIdempotency("mobile.gift_cards.purchase", e.server.handleGiftCardPurchase))
	if want != 0 && rec.Code != want {
		e.t.Fatalf("purchase want status %d got %d body=%s", want, rec.Code, rec.Body.String())
	}
	return rec
}

func (e *giftCardFlowEnv) purchaseTopup(quote map[string]any, phone, key string, want int) string {
	e.t.Helper()
	rec := e.purchaseTopupExpect(quote, phone, key, want)
	if rec.Code < 200 || rec.Code >= 300 {
		return ""
	}
	payload := decodeMap(e.t, rec.Body.Bytes())
	id, _ := payload["order_id"].(string)
	if id == "" {
		e.t.Fatalf("topup purchase response missing order_id: %s", rec.Body.String())
	}
	return id
}

func (e *giftCardFlowEnv) purchaseTopupExpect(quote map[string]any, phone, key string, want int) *httptest.ResponseRecorder {
	e.t.Helper()
	body := map[string]any{
		"quote_id":        quote["quote_id"],
		"product_id":      e.productID,
		"quantity":        1,
		"unit_price":      quote["amount_brl"],
		"funding_method":  "internal_usdt",
		"recipient_phone": phone,
	}
	rec := e.mobileRequest(http.MethodPost, "/api/mobile/commerce/orders", key, body, e.server.requireIdempotency("mobile.commerce.orders", e.server.handleCommerceOrders))
	if want != 0 && rec.Code != want {
		e.t.Fatalf("topup purchase want status %d got %d body=%s", want, rec.Code, rec.Body.String())
	}
	return rec
}

func (e *giftCardFlowEnv) mobileRequest(method, path, idempotency string, payload map[string]any, handler http.HandlerFunc) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	ctx := context.WithValue(req.Context(), ctxUserID, e.userID)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func (e *giftCardFlowEnv) processOutbox() {
	e.server.processNextCommerceOutbox(e.ctx)
}

func (e *giftCardFlowEnv) makeOutboxDue() {
	if _, err := e.db.SQL.ExecContext(e.ctx, `UPDATE commerce_outbox_events SET status='pending', next_attempt_at=NOW()-interval '1 second'
WHERE aggregate_id IN (SELECT id FROM mobile_gift_card_orders WHERE user_id=$1::uuid)`, e.userID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *giftCardFlowEnv) expireQuote(quoteID string) {
	if _, err := e.db.SQL.ExecContext(e.ctx, `UPDATE mobile_payment_quotes SET expires_at=NOW()-interval '1 second' WHERE quote_id=$1`, quoteID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *giftCardFlowEnv) setProviderUnknown(orderID, reference string) {
	if _, err := e.db.SQL.ExecContext(e.ctx, `
UPDATE mobile_gift_card_orders SET status='provider_unknown', provider_status='provider_unknown', provider_reference=$2, provider_order_id=$2 WHERE id=$1;
UPDATE mobile_payment_executions SET status='provider_unknown', provider_reference=$2, provider_transaction_id=$2 WHERE payment_intent_id=$1;
UPDATE commerce_outbox_events SET status='pending', next_attempt_at=NOW()-interval '1 second' WHERE aggregate_id=$1`, orderID, reference); err != nil {
		e.t.Fatal(err)
	}
}

func (e *giftCardFlowEnv) applyResult(orderID, status, providerRef, txID string) {
	order, err := e.server.loadCommerceOrderForProvider(e.ctx, orderID)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = e.server.applyCommerceProviderResult(e.ctx, order, &commercePurchaseResult{
		Status: status, ProviderStatus: status, ProviderReference: providerRef, TransactionID: txID, RedemptionCode: "CODE",
	})
}

func (e *giftCardFlowEnv) balance() giftCardBalance {
	bal, err := e.db.GetNFCBalance(e.ctx, e.wallet, "BSC")
	if err != nil {
		e.t.Fatal(err)
	}
	return giftCardBalance{Available: bal.AvailableMicro, Locked: bal.LockedMicro}
}

func (e *giftCardFlowEnv) assertBalance(want giftCardBalance) {
	e.t.Helper()
	if got := e.balance(); got != want {
		e.t.Fatalf("balance got %+v want %+v", got, want)
	}
}

func (e *giftCardFlowEnv) onlyOrderID() string {
	var id string
	if err := e.db.SQL.QueryRowContext(e.ctx, `SELECT id FROM mobile_gift_card_orders WHERE user_id=$1::uuid`, e.userID).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *giftCardFlowEnv) state(orderID string) giftCardFlowState {
	var s giftCardFlowState
	s.OrderID = orderID
	_ = e.db.SQL.QueryRowContext(e.ctx, `SELECT status, provider_reference FROM mobile_gift_card_orders WHERE id=$1`, orderID).Scan(&s.OrderStatus, &s.ProviderReference)
	_ = e.db.SQL.QueryRowContext(e.ctx, `SELECT status FROM mobile_payment_intents WHERE id=$1`, orderID).Scan(&s.IntentStatus)
	_ = e.db.SQL.QueryRowContext(e.ctx, `SELECT status, provider_idempotency_key FROM mobile_payment_executions WHERE payment_intent_id=$1`, orderID).Scan(&s.ExecutionStatus, &s.ProviderIDKey)
	_ = e.db.SQL.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM mobile_payment_intents WHERE id=$1`, orderID).Scan(&s.IntentCount)
	_ = e.db.SQL.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM mobile_payment_executions WHERE payment_intent_id=$1`, orderID).Scan(&s.ExecutionCount)
	_ = e.db.SQL.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM mobile_gift_card_orders WHERE id=$1`, orderID).Scan(&s.OrderCount)
	s.ReserveLedger = e.countLedger(orderID, "gift_card_purchase_lock")
	s.CaptureLedger = e.countLedger(orderID, "gift_card_purchase_capture")
	s.ReleaseLedger = e.countLedger(orderID, "gift_card_purchase_release")
	s.ProviderRefundLedger = e.countLedger(orderID, "gift_card_provider_refund")
	return s
}

func (e *giftCardFlowEnv) countLedger(orderID, source string) int {
	var count int
	_ = e.db.SQL.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM mobile_wallet_ledger_entries WHERE reference_id=$1 AND source=$2`, orderID, source).Scan(&count)
	return count
}

func (e *giftCardFlowEnv) countRows(table, where string) int {
	var count int
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s AND (wallet_address=$1 OR user_id=$2::uuid OR reference_id IN (SELECT id FROM mobile_gift_card_orders WHERE user_id=$2::uuid))`, table, where)
	if !strings.Contains(table, "mobile_wallet_ledger_entries") {
		query = fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s AND user_id=$1::uuid`, table, where)
		_ = e.db.SQL.QueryRowContext(e.ctx, query, e.userID).Scan(&count)
		return count
	}
	_ = e.db.SQL.QueryRowContext(e.ctx, query, e.wallet, e.userID).Scan(&count)
	return count
}

func setTestPrice(pw *workers.PriceWorker, currency string, price float64) {
	v := reflect.ValueOf(pw).Elem()
	prices := v.FieldByName("prices")
	reflect.NewAt(prices.Type(), unsafe.Pointer(prices.UnsafeAddr())).Elem().Set(reflect.ValueOf(map[string]float64{currency: price}))
}

func decodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response: %v raw=%s", err, string(raw))
	}
	return out
}

type fakeBitrefillServer struct {
	*httptest.Server
	posts               atomic.Int64
	failFirstInvoice    atomic.Bool
	timeoutFirstInvoice atomic.Bool
	invoiceStatus       atomic.Value
	orderStatus         atomic.Value
}

func newFakeBitrefillServer(t *testing.T) *fakeBitrefillServer {
	f := &fakeBitrefillServer{}
	f.invoiceStatus.Store("payment_confirmed")
	f.orderStatus.Store("delivered")
	mux := http.NewServeMux()
	mux.HandleFunc("/products/test-gift-card-code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"id": "test-gift-card-code", "name": "Test Gift Card", "country": "BR", "currency": "BRL",
			"type": "gift_card", "in_stock": true, "packages": []map[string]any{{"package_id": "pkg30", "value": 30}},
		}})
	})
	mux.HandleFunc("/products/test-mobile-topup-code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"id": "test-mobile-topup-code", "name": "Claro Brazil", "country": "BR", "currency": "BRL",
			"type": "phone_refill", "in_stock": true, "packages": []map[string]any{{"package_id": "pkg30", "value": 30}},
		}})
	})
	mux.HandleFunc("/invoices", func(w http.ResponseWriter, r *http.Request) {
		f.posts.Add(1)
		if f.failFirstInvoice.CompareAndSwap(true, false) {
			http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusInternalServerError)
			return
		}
		if f.timeoutFirstInvoice.CompareAndSwap(true, false) {
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				_ = conn.Close()
				return
			}
		}
		status, _ := f.invoiceStatus.Load().(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"id": "br_inv_" + mobilePayHash(time.Now().String())[:8], "status": status, "orders": []map[string]any{{"id": "br_order_" + mobilePayHash(time.Now().String())[:8]}},
		}})
	})
	mux.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		status, _ := f.orderStatus.Load().(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"id": strings.TrimPrefix(r.URL.Path, "/orders/"), "status": status, "redemption_info": map[string]any{"code": "CODE"},
		}})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeBitrefillServer) postCount() int {
	return int(f.posts.Load())
}
