package mobile

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"payment-gateway/internal/database"

	_ "github.com/lib/pq"
)

func mobileTradeQuoteTestDB(t *testing.T) *database.DB {
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
	migrationPath := filepath.Join("..", "..", "migrations", "044_mobile_trade_quotes.sql")
	raw, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, string(raw)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return db
}

func insertMobileTradeQuoteUser(t *testing.T, db *database.DB, suffix string) string {
	t.Helper()
	var id string
	err := db.SQL.QueryRowContext(context.Background(), `
INSERT INTO users (email, password_hash, kyc_status)
VALUES ($1, 'test-hash', 'approved')
ON CONFLICT (email) DO UPDATE SET deleted_at=NULL
RETURNING id::text`, "mobile-trade-quote-"+suffix+"@example.test").Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

func newTradeQuoteServer(db *database.DB) *Server {
	return &Server{
		db:   db,
		mcfg: &MobileConfig{JWTSecret: "test-mobile-quote-secret-32-bytes-long"},
	}
}

func TestMobileTradeQuoteUserBindingAndSingleConsumption(t *testing.T) {
	db := mobileTradeQuoteTestDB(t)
	s := newTradeQuoteServer(db)
	ctx := context.Background()
	userA := insertMobileTradeQuoteUser(t, db, "a")
	userB := insertMobileTradeQuoteUser(t, db, "b")

	quote, err := s.issueMobileTradeQuote(ctx, userA, mobileQuoteClaims{
		Side: "buy", Asset: "USDT", Network: "BSC", Amount: 100, Rate: 5.25, Fee: 4.99, Total: 104.99,
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("issue quote: %v", err)
	}
	if _, err := s.consumeMobileTradeQuote(ctx, userB, quote, "buy", "USDT", "BSC", 100, "idem-b", time.Now()); !errors.Is(err, errMobileTradeQuoteOwnerMismatch) {
		t.Fatalf("cross-user consume err=%v, want owner mismatch", err)
	}
	if _, err := s.consumeMobileTradeQuote(ctx, userA, quote, "buy", "USDT", "BSC", 100, "idem-a", time.Now()); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if _, err := s.consumeMobileTradeQuote(ctx, userA, quote, "buy", "USDT", "BSC", 100, "idem-a", time.Now()); err != nil {
		t.Fatalf("same idempotency retry should pass: %v", err)
	}
	if _, err := s.consumeMobileTradeQuote(ctx, userA, quote, "buy", "USDT", "BSC", 100, "idem-a-2", time.Now()); !errors.Is(err, errMobileTradeQuoteConsumed) {
		t.Fatalf("second operation err=%v, want consumed", err)
	}
}

func TestMobileTradeQuoteExpiryBoundary(t *testing.T) {
	db := mobileTradeQuoteTestDB(t)
	s := newTradeQuoteServer(db)
	userID := insertMobileTradeQuoteUser(t, db, "expiry")
	now := time.Now().UTC()

	quote, err := s.issueMobileTradeQuote(context.Background(), userID, mobileQuoteClaims{
		Side: "sell", Asset: "USDT", Network: "BSC", Amount: 10, Rate: 5.1, Fee: 1, Total: 50,
		ExpiresAt: now.Unix(),
	})
	if err != nil {
		t.Fatalf("issue quote: %v", err)
	}
	if _, err := s.consumeMobileTradeQuote(context.Background(), userID, quote, "sell", "USDT", "BSC", 10, "idem-exp", now); err == nil {
		t.Fatal("expires_at == now must be expired")
	}
}

func TestBTCTradeQuoteAmountUsesSatoshis(t *testing.T) {
	cases := []struct {
		name string
		btc  float64
		sats int64
	}{
		{"one_sat", 0.00000001, 1},
		{"hundred_sats", 0.000001, 100},
		{"thousand_sats", 0.00001, 1000},
		{"hundred_thousand_sats", 0.001, 100000},
		{"point_001_btc", 0.001, 100000},
		{"point_01_btc", 0.01, 1000000},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			minor, raw := canonicalTradeAmountForAsset("sell", "BTC", tt.btc)
			if minor != tt.sats {
				t.Fatalf("minor=%d want %d", minor, tt.sats)
			}
			if raw != stringInt64(tt.sats) {
				t.Fatalf("raw=%s want %d", raw, tt.sats)
			}
		})
	}
}

func TestMobileBTCPriceFallsBackToBTCUSDT(t *testing.T) {
	pw := fakeMobilePriceCache{
		"BRL":     5.5,
		"BTCUSDT": 65000,
	}
	if got, want := mobileAssetPriceBRL(pw, "BTC"), 357500.0; got != want {
		t.Fatalf("mobileAssetPriceBRL(BTC)=%v want %v", got, want)
	}
	if got, want := mobileAssetPriceUSD(pw, "BTC"), 65000.0; got != want {
		t.Fatalf("mobileAssetPriceUSD(BTC)=%v want %v", got, want)
	}
}

type fakeMobilePriceCache map[string]float64

func (f fakeMobilePriceCache) GetPrice(key string) float64 {
	return f[key]
}

func stringInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
