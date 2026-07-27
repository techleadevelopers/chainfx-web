package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"payment-gateway/internal/config"

	"github.com/joho/godotenv"
)

func TestCreateQuoteAgainstConfiguredPostgres(t *testing.T) {
	if os.Getenv("CHAINFX_DB_PROBE") != "1" {
		t.Skip("set CHAINFX_DB_PROBE=1 to run against configured DATABASE_URL")
	}
	_ = godotenv.Load("../../.env")
	cfg := config.LoadConfig()
	db, err := ConnectPostgres(cfg)
	if err != nil {
		t.Fatalf("ConnectPostgres: %v", err)
	}
	defer db.Close()

	id := "qt_probe_" + strings.ReplaceAll(NewID(), "-", "")
	q, err := db.CreateQuote(context.Background(), QuoteInput{
		ID:                id,
		Side:              "buy",
		Asset:             "USDT",
		Network:           "BSC",
		FiatCurrency:      "BRL",
		PaymentMethod:     "pix",
		AmountMinor:       10000,
		CryptoAmountUnits: "19.567174",
		Rate:              5.1106,
		MarketRate:        5.06,
		FeeMinor:          749,
		ExpiresAt:         time.Now().UTC().Add(5 * time.Minute),
		BodyHash:          "probe_hash",
	})
	if err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q == nil || q.ID != id || q.Network != "BSC" {
		t.Fatalf("unexpected quote: %#v", q)
	}
}
