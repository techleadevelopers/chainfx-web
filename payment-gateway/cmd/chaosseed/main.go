// chaosseed pre-populates real "aguardando_pix" buy orders through the same
// database.CreateBuyOrder path the actual /api/buy flow uses, then prints
// their IDs as a JSON array to stdout (or a file, via -out). It exists so
// tests/chaos_suite.sh can feed k6's Human Rail scenario real settlement
// targets — k6 itself has no Postgres driver, so seeding happens here,
// through the real Go code path, instead of hand-written SQL in a shell
// script that could silently drift from the actual schema.
//
// Test-only tooling: never imported by cmd/api.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

func main() {
	count := flag.Int("count", 50, "number of buy orders to seed")
	out := flag.String("out", "", "file to write the JSON array of IDs to (default: stdout)")
	cleanup := flag.String("cleanup", "", "instead of seeding, read a JSON array of IDs from this file and delete those buy orders + their events")
	flag.Parse()

	cfg := config.LoadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("chaosseed: DATABASE_URL not configured")
	}

	db, err := database.ConnectPostgres(cfg)
	if err != nil {
		log.Fatalf("chaosseed: failed to connect to Postgres: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if *cleanup != "" {
		raw, err := os.ReadFile(*cleanup)
		if err != nil {
			log.Fatalf("chaosseed: failed to read %s: %v", *cleanup, err)
		}
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			log.Fatalf("chaosseed: failed to parse %s: %v", *cleanup, err)
		}
		for _, id := range ids {
			_, _ = db.SQL.ExecContext(ctx, `DELETE FROM buy_order_events WHERE buy_order_id = $1`, id)
			_, _ = db.SQL.ExecContext(ctx, `DELETE FROM buy_orders WHERE id = $1`, id)
		}
		log.Printf("chaosseed: cleaned up %d seeded buy orders", len(ids))
		return
	}

	ids := make([]string, 0, *count)
	for i := 0; i < *count; i++ {
		order, err := db.CreateBuyOrder(ctx, database.BuyOrderInput{
			Status:            "aguardando_pix",
			AmountBRL:         1000.00,
			AmountFiat:        1000.00,
			FiatCurrency:      "BRL",
			PaymentMethod:     "pix",
			CryptoAmount:      10.0,
			Asset:             "USDT",
			DestAddress:       "0x000000000000000000000000000000000000dEaD",
			RateLocked:        100.0,
			RateLockExpiresAt: time.Now().Add(time.Hour),
		})
		if err != nil {
			log.Fatalf("chaosseed: failed to seed buy order %d/%d: %v", i+1, *count, err)
		}
		ids = append(ids, order.ID)
	}

	payload, err := json.Marshal(ids)
	if err != nil {
		log.Fatalf("chaosseed: failed to marshal IDs: %v", err)
	}

	if *out == "" {
		fmt.Println(string(payload))
		return
	}
	if err := os.WriteFile(*out, payload, 0o644); err != nil {
		log.Fatalf("chaosseed: failed to write %s: %v", *out, err)
	}
	log.Printf("chaosseed: seeded %d buy orders -> %s", len(ids), *out)
}
