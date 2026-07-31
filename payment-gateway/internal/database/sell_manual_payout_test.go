package database

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/models"
)

func TestManualPixPaidRequiresManualPayoutQueue(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	order := createManualSellOrder(t, db, "20000000-0000-0000-0000-000000000001", models.StatusAguardandoDeposito, "")

	ok, err := db.MarkManualPixPaid(ctx, order.ID, "pix-proof-early", "admin@chainfx.local", "")
	if err != nil {
		t.Fatalf("MarkManualPixPaid without funding: %v", err)
	}
	if ok {
		t.Fatal("manual payout confirmed before funding/manual queue")
	}

	_, err = db.SQL.ExecContext(ctx, `UPDATE orders SET status='pago', deposit_tx='0xfundingseen' WHERE id=$1::uuid`, order.ID)
	if err != nil {
		t.Fatalf("seed funding seen: %v", err)
	}
	ok, err = db.MarkManualPixPaid(ctx, order.ID, "pix-proof-seen", "admin@chainfx.local", "")
	if err != nil {
		t.Fatalf("MarkManualPixPaid funding_seen: %v", err)
	}
	if ok {
		t.Fatal("manual payout confirmed from funding_seen/pago before manual queue")
	}
}

func TestManualPixPaidCompletesOnlyFromManualQueueAndIsIdempotent(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	order := createManualSellOrder(t, db, "20000000-0000-0000-0000-000000000002", models.StatusAguardandoPixManual, "0xfundingconfirmed")

	ok, err := db.MarkManualPixPaid(ctx, order.ID, "pix-proof-1", "admin@chainfx.local", "paid manually")
	if err != nil || !ok {
		t.Fatalf("first MarkManualPixPaid = %v, %v", ok, err)
	}
	updated, err := db.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if updated.Status != models.StatusConcluida {
		t.Fatalf("status=%s want concluida", updated.Status)
	}
	if updated.TxHash == nil || *updated.TxHash != "pix-proof-1" {
		t.Fatalf("manual provider reference not persisted: %+v", updated.TxHash)
	}

	ok, err = db.MarkManualPixPaid(ctx, order.ID, "pix-proof-2", "admin2@chainfx.local", "double click")
	if err != nil || !ok {
		t.Fatalf("idempotent MarkManualPixPaid = %v, %v", ok, err)
	}
	updated, err = db.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder after idempotency: %v", err)
	}
	if updated.TxHash == nil || *updated.TxHash != "pix-proof-1" {
		t.Fatalf("idempotent call changed economic reference: %+v", updated.TxHash)
	}
	if got := countOrderEvents(t, db, order.ID, "order.pix_manual_paid"); got != 1 {
		t.Fatalf("manual payout audit events=%d want 1", got)
	}
}

func TestManualPixPaidConcurrentAdminsSingleTransition(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	order := createManualSellOrder(t, db, "20000000-0000-0000-0000-000000000003", models.StatusAguardandoPixManual, "0xfundingconfirmed2")

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := db.MarkManualPixPaid(ctx, order.ID, fmt.Sprintf("pix-proof-concurrent-%d", i), fmt.Sprintf("admin%d@chainfx.local", i), "")
			if err != nil {
				errs <- err
				return
			}
			if !ok {
				errs <- fmt.Errorf("admin %d got non-idempotent rejection", i)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := countOrderEvents(t, db, order.ID, "order.pix_manual_paid"); got != 1 {
		t.Fatalf("manual payout audit events=%d want 1", got)
	}
}

func TestSellDepositTxCannotFundTwoOrders(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	first := createManualSellOrder(t, db, "20000000-0000-0000-0000-000000000004", models.StatusPago, "0xsametx")
	second := createManualSellOrder(t, db, "20000000-0000-0000-0000-000000000005", models.StatusAguardandoDeposito, "")

	used, err := db.HasDepositTxForOtherOrder(ctx, second.ID, "0xsametx")
	if err != nil {
		t.Fatalf("HasDepositTxForOtherOrder: %v", err)
	}
	if !used {
		t.Fatalf("deposit tx on %s was not detected as used by another order", first.ID)
	}
}

func TestBTCSellExternalFundingLifecycleAndGuards(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	userID := createBTCSellUser(t, db, "btc-lifecycle")
	order := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000101", userID, "tb1qchainfxexternaldeposit000000000000001", 100000, time.Now().Add(5*time.Minute))

	wrong, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, "tb1qwrongaddress000000000000000000000000", "tx-wrong", 0, 100000, 6, 3)
	if err != nil {
		t.Fatalf("wrong address event: %v", err)
	}
	if wrong != nil {
		t.Fatal("wrong deposit address must not match BTC SELL")
	}

	pending, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, order.Address, "tx-below-min", 0, 100000, 1, 3)
	if err != nil {
		t.Fatalf("below confirmations: %v", err)
	}
	if pending == nil || pending.Ready || pending.Status != "pending_confirmations" {
		t.Fatalf("below min confirmations result=%+v", pending)
	}
	if ok, err := db.MarkManualPixPaid(ctx, order.ID, "pix-before-btc-confirmed", "admin@chainfx.local", ""); err != nil || ok {
		t.Fatalf("admin confirm before BTC confirmed ok=%v err=%v", ok, err)
	}

	ready, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, order.Address, "tx-below-min", 0, 100000, 3, 3)
	if err != nil {
		t.Fatalf("min confirmations reached: %v", err)
	}
	if ready == nil || !ready.Ready || ready.DepositKey != "tx-below-min:0" {
		t.Fatalf("ready result=%+v", ready)
	}
	confirmed, err := db.ConfirmSellDepositForPayout(ctx, order.ID, ready.DepositKey, 0.001, map[string]any{"asset": "BTC"})
	if err != nil || !confirmed {
		t.Fatalf("ConfirmSellDepositForPayout BTC ok=%v err=%v", confirmed, err)
	}
	queued, err := db.ClaimOrderForManualPayout(ctx, order.ID, map[string]any{"mode": "manual", "asset": "BTC"})
	if err != nil || !queued {
		t.Fatalf("ClaimOrderForManualPayout BTC ok=%v err=%v", queued, err)
	}
	ok, err := db.MarkManualPixPaid(ctx, order.ID, "pix-paid-btc", "admin@chainfx.local", "")
	if err != nil || !ok {
		t.Fatalf("admin confirm after BTC funding ok=%v err=%v", ok, err)
	}
	ok, err = db.MarkManualPixPaid(ctx, order.ID, "pix-paid-btc-again", "admin@chainfx.local", "")
	if err != nil || !ok {
		t.Fatalf("double admin confirm must be idempotent ok=%v err=%v", ok, err)
	}
}

func TestBTCSellRejectsUnderOverLateAndDuplicateUTXO(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	userID := createBTCSellUser(t, db, "btc-guards")

	under := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000102", userID, "tb1qchainfxexternaldeposit000000000000102", 100000, time.Now().Add(5*time.Minute))
	underMatch, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, under.Address, "tx-under", 0, 99999, 6, 3)
	if err != nil || underMatch == nil || underMatch.Ready || underMatch.Status != "manual_review" {
		t.Fatalf("underpayment match=%+v err=%v", underMatch, err)
	}

	over := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000103", userID, "tb1qchainfxexternaldeposit000000000000103", 100000, time.Now().Add(5*time.Minute))
	overMatch, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, over.Address, "tx-over", 0, 100001, 6, 3)
	if err != nil || overMatch == nil || overMatch.Ready || overMatch.Status != "manual_review" {
		t.Fatalf("overpayment match=%+v err=%v", overMatch, err)
	}

	late := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000104", userID, "tb1qchainfxexternaldeposit000000000000104", 100000, time.Now().Add(-time.Minute))
	lateMatch, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, late.Address, "tx-late", 0, 100000, 6, 3)
	if err != nil || lateMatch == nil || lateMatch.Ready || lateMatch.Status != "manual_review" {
		t.Fatalf("late funding match=%+v err=%v", lateMatch, err)
	}

	first := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000105", userID, "tb1qchainfxexternaldeposit000000000000105", 100000, time.Now().Add(5*time.Minute))
	second := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000106", userID, "tb1qchainfxexternaldeposit000000000000106", 100000, time.Now().Add(5*time.Minute))
	if _, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, first.Address, "tx-same-utxo", 7, 100000, 6, 3); err != nil {
		t.Fatalf("first same utxo: %v", err)
	}
	if _, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, second.Address, "tx-same-utxo", 7, 100000, 6, 3); err == nil {
		t.Fatal("same txid+vout must not fund two BTC SELL orders")
	}
}

func TestBTCSellDerivedAddressSelectsExactAmountAndBlocksAmbiguity(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	userID := createBTCSellUser(t, db, "btc-same-derived-address")
	address := "tb1qchainfxderiveduserdeposit000000000001"

	first := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000107", userID, address, 100000, time.Now().Add(5*time.Minute))
	second := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000108", userID, address, 200000, time.Now().Add(5*time.Minute))
	exact, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, address, "tx-exact-second", 1, 200000, 6, 3)
	if err != nil {
		t.Fatalf("exact amount on shared derived address: %v", err)
	}
	if exact == nil || !exact.Ready || exact.OrderID != second.ID {
		t.Fatalf("exact amount matched wrong order: match=%+v second=%s", exact, second.ID)
	}
	var firstStatus string
	if err := db.SQL.QueryRowContext(ctx, `SELECT status FROM btc_sell_fundings WHERE order_id=$1::uuid`, first.ID).Scan(&firstStatus); err != nil {
		t.Fatalf("first funding status: %v", err)
	}
	if firstStatus != "awaiting_deposit" {
		t.Fatalf("first order was touched by second deposit: status=%s", firstStatus)
	}

	ambiguousA := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000109", userID, address, 300000, time.Now().Add(5*time.Minute))
	ambiguousB := createBTCSellOrder(t, db, "20000000-0000-0000-0000-000000000110", userID, address, 300000, time.Now().Add(5*time.Minute))
	ambiguous, err := db.ApplyBTCSellFundingEvent(ctx, "testnet", userID, address, "tx-ambiguous", 0, 300000, 6, 3)
	if err != nil {
		t.Fatalf("ambiguous same amount: %v", err)
	}
	if ambiguous != nil {
		t.Fatalf("ambiguous same amount must not auto-match: %+v", ambiguous)
	}
	for _, order := range []*models.Order{ambiguousA, ambiguousB} {
		var status string
		if err := db.SQL.QueryRowContext(ctx, `SELECT status FROM btc_sell_fundings WHERE order_id=$1::uuid`, order.ID).Scan(&status); err != nil {
			t.Fatalf("ambiguous funding status: %v", err)
		}
		if status != "awaiting_deposit" {
			t.Fatalf("ambiguous order %s was touched: status=%s", order.ID, status)
		}
	}
}

func createManualSellOrder(t *testing.T, db *DB, id string, status models.OrderStatus, depositTx string) *models.Order {
	t.Helper()
	ctx := context.Background()
	if db.cfg == nil {
		db.cfg = &config.Config{}
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(ctx, `DELETE FROM order_events WHERE order_id=$1::uuid`, id)
		_, _ = db.SQL.ExecContext(ctx, `DELETE FROM orders WHERE id=$1::uuid`, id)
	})
	order, err := db.CreateOrder(ctx, OrderInput{
		ID:                id,
		Status:            string(status),
		AmountBRL:         100,
		AmountUSDT:        20,
		FeeBRL:            10,
		PayoutBRL:         90,
		Address:           "0x1111111111111111111111111111111111111111",
		Asset:             "USDT",
		Network:           "BSC",
		RateLocked:        4.5,
		RateLockExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if depositTx != "" {
		if _, err := db.SQL.ExecContext(ctx, `UPDATE orders SET deposit_tx=$2 WHERE id=$1::uuid`, id, depositTx); err != nil {
			t.Fatalf("seed deposit tx: %v", err)
		}
		order.DepositTx = &depositTx
	}
	return order
}

func createBTCSellUser(t *testing.T, db *DB, suffix string) string {
	t.Helper()
	var id string
	email := fmt.Sprintf("btc-sell-%s@example.test", suffix)
	if err := db.SQL.QueryRowContext(context.Background(), `
INSERT INTO users (email, password_hash, kyc_status)
VALUES ($1, 'test-hash', 'approved')
ON CONFLICT (email) DO UPDATE SET deleted_at=NULL
RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatalf("create BTC sell user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, id)
	})
	return id
}

func createBTCSellOrder(t *testing.T, db *DB, id, userID, address string, expectedSats int64, expiresAt time.Time) *models.Order {
	t.Helper()
	ctx := context.Background()
	if db.cfg == nil {
		db.cfg = &config.Config{}
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(ctx, `DELETE FROM order_events WHERE order_id=$1::uuid`, id)
		_, _ = db.SQL.ExecContext(ctx, `DELETE FROM btc_sell_fundings WHERE order_id=$1::uuid`, id)
		_, _ = db.SQL.ExecContext(ctx, `DELETE FROM orders WHERE id=$1::uuid`, id)
	})
	order, err := db.CreateOrder(ctx, OrderInput{
		ID:                id,
		Status:            string(models.StatusAguardandoDeposito),
		AmountBRL:         450,
		AmountUSDT:        float64(expectedSats) / 100000000,
		FeeBRL:            5,
		PayoutBRL:         445,
		Address:           address,
		Asset:             "BTC",
		Network:           "BITCOIN",
		RateLocked:        450000,
		RateLockExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("Create BTC order: %v", err)
	}
	if _, err := db.SQL.ExecContext(ctx, `UPDATE orders SET user_id=$2::uuid WHERE id=$1::uuid`, id, userID); err != nil {
		t.Fatalf("tag BTC order user: %v", err)
	}
	if err := db.CreateBTCSellFunding(ctx, BTCSellFundingInput{
		OrderID:         id,
		UserID:          userID,
		WalletAddressID: "btc-wallet-address-" + id[len(id)-3:],
		BTCAddress:      address,
		BTCNetwork:      "testnet",
		ExpectedSats:    expectedSats,
		QuoteID:         "quote-" + id[len(id)-3:],
	}); err != nil {
		t.Fatalf("CreateBTCSellFunding: %v", err)
	}
	return order
}

func countOrderEvents(t *testing.T, db *DB, orderID, eventType string) int {
	t.Helper()
	var count int
	if err := db.SQL.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM order_events WHERE order_id=$1::uuid AND type=$2`, orderID, eventType).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return count
}
