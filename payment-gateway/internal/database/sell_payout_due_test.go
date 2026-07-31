package database

import (
	"context"
	"os"
	"testing"
	"time"
)

func sellPayoutDueTestDB(t *testing.T) *DB {
	t.Helper()
	if os.Getenv("CHAINFX_SELL_PAYOUT_DB_TEST") != "1" {
		t.Skip("set CHAINFX_SELL_PAYOUT_DB_TEST=1 to run sell payout DB integration tests")
	}
	db := buyRecoveryTestDB(t)
	raw, err := os.ReadFile("../../migrations/045_sell_payout_executions.sql")
	if err != nil {
		t.Fatalf("read sell payout migration: %v", err)
	}
	if _, err := db.SQL.ExecContext(context.Background(), string(raw)); err != nil {
		t.Fatalf("apply sell payout migration: %v", err)
	}
	return db
}

func TestListDueSellPayoutExecutionsFiltersOrdersAndLimits(t *testing.T) {
	db := sellPayoutDueTestDB(t)
	ctx := context.Background()
	orderIDs := map[string]bool{}
	t.Cleanup(func() {
		for id := range orderIDs {
			_, _ = db.SQL.ExecContext(ctx, `DELETE FROM sell_payout_executions WHERE order_id=$1::uuid`, id)
			_, _ = db.SQL.ExecContext(ctx, `DELETE FROM orders WHERE id=$1::uuid`, id)
		}
	})

	expected := []string{
		createSellPayoutExecForDueTest(t, db, "10000000-0000-0000-0000-000000000001", "pending", -10*time.Minute),
		createSellPayoutExecForDueTest(t, db, "10000000-0000-0000-0000-000000000002", "provider_unknown", -9*time.Minute),
		createSellPayoutExecForDueTest(t, db, "10000000-0000-0000-0000-000000000003", "provider_pending", -8*time.Minute),
	}
	for _, id := range expected {
		orderIDs[id] = true
	}
	for _, id := range []string{
		createSellPayoutExecForDueTest(t, db, "10000000-0000-0000-0000-000000000004", "provider_unknown", 5*time.Minute),
		createSellPayoutExecForDueTest(t, db, "10000000-0000-0000-0000-000000000005", "completed", -7*time.Minute),
		createSellPayoutExecForDueTest(t, db, "10000000-0000-0000-0000-000000000006", "manual_review", -6*time.Minute),
		createSellPayoutExecForDueTest(t, db, "10000000-0000-0000-0000-000000000007", "failed", -5*time.Minute),
	} {
		orderIDs[id] = true
	}

	got, err := db.ListDueSellPayoutExecutions(ctx, 2)
	if err != nil {
		t.Fatalf("ListDueSellPayoutExecutions: %v", err)
	}
	got = filterSellPayoutExecsBySet(got, orderIDs)
	if len(got) != 2 {
		t.Fatalf("due len=%d want 2: %+v", len(got), got)
	}
	for i, exec := range got {
		if exec.OrderID != expected[i] {
			t.Fatalf("order[%d]=%s want %s", i, exec.OrderID, expected[i])
		}
		if exec.NextAttemptAt.Before(time.Now()) {
			t.Fatalf("leased next_attempt_at was not pushed forward: %+v", exec)
		}
	}

	got, err = db.ListDueSellPayoutExecutions(ctx, 10)
	if err != nil {
		t.Fatalf("ListDueSellPayoutExecutions second call: %v", err)
	}
	got = filterSellPayoutExecsBySet(got, orderIDs)
	if len(got) != 1 || got[0].OrderID != expected[2] {
		t.Fatalf("second due=%+v want only %s", got, expected[2])
	}
}

func TestListDueSellPayoutExecutionsConcurrentLease(t *testing.T) {
	db := sellPayoutDueTestDB(t)
	ctx := context.Background()
	orderID := "10000000-0000-0000-0000-000000000101"
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(ctx, `DELETE FROM sell_payout_executions WHERE order_id=$1::uuid`, orderID)
		_, _ = db.SQL.ExecContext(ctx, `DELETE FROM orders WHERE id=$1::uuid`, orderID)
	})
	createSellPayoutExecForDueTest(t, db, orderID, "pending", -time.Minute)

	results := make(chan []SellPayoutExecution, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			got, err := db.ListDueSellPayoutExecutions(ctx, 1)
			if err != nil {
				errs <- err
				return
			}
			results <- filterSellPayoutExecsBySet(got, map[string]bool{orderID: true})
		}()
	}

	total := 0
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			t.Fatalf("ListDueSellPayoutExecutions concurrent: %v", err)
		case got := <-results:
			if len(got) > 1 {
				t.Fatalf("worker leased too many executions: %+v", got)
			}
			if len(got) == 1 && got[0].OrderID != orderID {
				t.Fatalf("leased wrong order: %+v", got[0])
			}
			total += len(got)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for concurrent ListDue")
		}
	}
	if total != 1 {
		t.Fatalf("leased total=%d want 1", total)
	}
}

func createSellPayoutExecForDueTest(t *testing.T, db *DB, orderID, status string, dueOffset time.Duration) string {
	t.Helper()
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	_, err := db.SQL.ExecContext(ctx, `
                INSERT INTO orders (id, access_token, request_id, status, amount_brl, btc_amount, fee_brl, payout_brl, address, asset, network, rate_locked, rate_lock_expires_at, pix_key, created_at)
                VALUES ($1::uuid, $2, $3, 'processando_payout', 100, 0, 0, 100, '0x0000000000000000000000000000000000000001', 'USDT', 'BSC', 1, $4, 'seller@example.com', now())
                ON CONFLICT (id) DO NOTHING`, orderID, orderID+"_token", orderID+"_request", expires)
	if err != nil {
		t.Fatalf("insert order %s: %v", orderID, err)
	}
	exec, err := db.EnsureSellPayoutExecution(ctx, orderID, "efi", orderID, 10000, "seller@example.com")
	if err != nil {
		t.Fatalf("EnsureSellPayoutExecution %s: %v", orderID, err)
	}
	_, err = db.SQL.ExecContext(ctx, `
                UPDATE sell_payout_executions
                   SET status=$1,
                       next_attempt_at=now() + make_interval(secs => $2),
                       submit_outcome=CASE
                         WHEN $1 IN ('pending') THEN 'not_submitted'
                         WHEN $1 IN ('provider_unknown') THEN 'ambiguous'
                         WHEN $1 IN ('completed','provider_pending') THEN 'confirmed'
                         WHEN $1 IN ('failed','manual_review') THEN 'rejected'
                         ELSE 'started'
                       END,
                       updated_at=now()
                 WHERE id=$3`, status, int(dueOffset.Seconds()), exec.ID)
	if err != nil {
		t.Fatalf("update exec %s: %v", orderID, err)
	}
	return orderID
}

func filterSellPayoutExecsBySet(execs []SellPayoutExecution, allowed map[string]bool) []SellPayoutExecution {
	out := make([]SellPayoutExecution, 0, len(execs))
	for _, exec := range execs {
		if allowed[exec.OrderID] {
			out = append(out, exec)
		}
	}
	return out
}
