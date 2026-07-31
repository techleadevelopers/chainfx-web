package database

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func buyRecoveryTestDB(t *testing.T) *DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL/DATABASE_URL not set")
	}
	sqlDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db := &DB{SQL: sqlDB}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

func TestClaimBuyOrderForSendRecoversOnlyStaleSending(t *testing.T) {
	db := buyRecoveryTestDB(t)
	ctx := context.Background()
	buy, err := db.CreateBuyOrder(ctx, BuyOrderInput{
		Status:            "pago_pix",
		AmountBRL:         100,
		AmountFiat:        100,
		FiatCurrency:      "BRL",
		PaymentMethod:     "pix",
		PayoutBRL:         100,
		CryptoAmount:      10,
		Asset:             "USDT",
		Network:           "BSC",
		DestAddress:       "0x0000000000000000000000000000000000000001",
		RateLocked:        5,
		RateLockExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateBuyOrder: %v", err)
	}
	claimed, err := db.ClaimBuyOrderForSend(ctx, buy.ID)
	if err != nil || !claimed {
		t.Fatalf("initial claim = %v, %v", claimed, err)
	}
	claimed, err = db.ClaimBuyOrderForSend(ctx, buy.ID)
	if err != nil {
		t.Fatalf("recent sending claim: %v", err)
	}
	if claimed {
		t.Fatal("recent enviando order must not be reclaimed")
	}
	if _, err := db.SQL.ExecContext(ctx, `UPDATE buy_orders SET updated_at = now() - interval '2 minutes' WHERE id=$1`, buy.ID); err != nil {
		t.Fatalf("age order: %v", err)
	}
	claimed, err = db.ClaimBuyOrderForSend(ctx, buy.ID)
	if err != nil || !claimed {
		t.Fatalf("stale sending claim = %v, %v", claimed, err)
	}
}
