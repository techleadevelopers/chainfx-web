package workers

import (
	"context"
	"database/sql"
	"math/big"
	"os"
	"testing"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/models"

	_ "github.com/lib/pq"
)

func sellFundingTestDB(t *testing.T) *database.DB {
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
	db := &database.DB{SQL: sqlDB}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

func TestSellFundingAfterRateLockExpiryDoesNotPublishPayout(t *testing.T) {
	db := sellFundingTestDB(t)
	ctx := context.Background()
	order, err := db.CreateOrder(ctx, database.OrderInput{
		Status:            string(models.StatusAguardandoDeposito),
		AmountBRL:         50,
		AmountUSDT:        10,
		FeeBRL:            1,
		PayoutBRL:         49,
		Address:           "0x1111111111111111111111111111111111111111",
		Asset:             "USDT",
		Network:           "BSC",
		RateLocked:        5,
		RateLockExpiresAt: time.Now().Add(-1 * time.Second),
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	bus := NewEventBus()
	payouts := bus.Subscribe("payout.requested")
	worker := &OnchainWorker{
		bus: bus,
		db:  db,
		cfg: &config.Config{BscDepositTolerancePct: 0.02},
	}
	worker.confirmDeposit(ctx, onchainNetworkConfig{
		Name:                  "BSC",
		TokenContract:         "0x2222222222222222222222222222222222222222",
		TokenDecimals:         18,
		RequiredConfirmations: 3,
	}, order.ID, "0xlatefunding", new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)), 123)

	select {
	case ev := <-payouts:
		t.Fatalf("unexpected payout.requested: %+v", ev)
	default:
	}
	updated, err := db.GetOrder(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if updated.Status == models.StatusPago {
		t.Fatalf("expired funding moved to pago")
	}
	if updated.DepositTx == nil || *updated.DepositTx != "0xlatefunding" {
		t.Fatalf("deposit evidence not persisted: %+v", updated.DepositTx)
	}
}

func TestSellFundingBeforeRateLockExpiryPublishesPayout(t *testing.T) {
	db := sellFundingTestDB(t)
	ctx := context.Background()
	order, err := db.CreateOrder(ctx, database.OrderInput{
		Status:            string(models.StatusAguardandoDeposito),
		AmountBRL:         50,
		AmountUSDT:        10,
		FeeBRL:            1,
		PayoutBRL:         49,
		Address:           "0x3333333333333333333333333333333333333333",
		Asset:             "USDT",
		Network:           "BSC",
		RateLocked:        5,
		RateLockExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	bus := NewEventBus()
	payouts := bus.Subscribe("payout.requested")
	worker := &OnchainWorker{
		bus: bus,
		db:  db,
		cfg: &config.Config{BscDepositTolerancePct: 0.02},
	}
	worker.confirmDeposit(ctx, onchainNetworkConfig{
		Name:                  "BSC",
		TokenContract:         "0x2222222222222222222222222222222222222222",
		TokenDecimals:         18,
		RequiredConfirmations: 3,
	}, order.ID, "0xearlyfunding", new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)), 124)

	select {
	case ev := <-payouts:
		if ev.OrderID != order.ID {
			t.Fatalf("payout for wrong order: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected payout.requested")
	}
}
