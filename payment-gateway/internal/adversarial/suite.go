// Package adversarial is the ChainFX Adversarial Engine: a suite of
// destructive, concurrent tests that attack the real HTTP surface and the
// real internal invariants of the hybrid payment gateway (fiduciary Human
// Rail via Pix/Efí + programmatic Machine Layer via MCP agents + on-chain
// settlement) instead of asserting against mocked behaviour.
//
// Every test in this package either:
//  1. Drives the real *server.Server / *workers.WorkerManager wiring over a
//     real httptest.Server backed by the project's configured Postgres
//     instance (DATABASE_URL), or
//  2. White-box tests a specific internal safety invariant (a lock, a floor,
//     a rate limiter) directly, with no network involved.
//
// Tests that need Postgres call requireTestDB(t), which skips (not fails)
// when DATABASE_URL / the DB is unreachable, so this package stays safe to
// run in environments without a live database while still exercising the
// real code path whenever one is available.
package adversarial

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/server"
	"payment-gateway/internal/workers"
)

// testSecret is the shared PIX webhook HMAC secret used by adversarial
// requests that need to look like a legitimate signed Efí callback.
const testSecret = "adversarial-suite-shared-secret-do-not-use-in-prod"

// testConfig builds a *config.Config equivalent to development mode (never
// production), so the same auth-bypass and API-key-optional code paths the
// real dev/staging environment exercises are what gets attacked here.
func testConfig() *config.Config {
	cfg := config.LoadConfig()
	cfg.Environment = "test"
	cfg.PixWebhookSecret = testSecret
	cfg.ChainFXRequireAPIKey = false
	return cfg
}

// harness bundles the real, fully-wired HTTP surface under test.
type harness struct {
	Server *httptest.Server
	DB     *database.DB
	Cfg    *config.Config
}

// newHarness boots the real server.New(...) + workers.WorkerManager wiring
// against the project's Postgres instance and exposes it over a real
// httptest.Server, so attacks travel through the exact same net/http
// routing, middleware and handlers production traffic does.
//
// Skips the calling test (not fails) if DATABASE_URL is unreachable, since
// this suite must not turn "no DB configured in this sandbox" into a false
// signal of a broken defense.
func newHarness(t *testing.T) *harness {
	t.Helper()
	cfg := testConfig()
	if cfg.DatabaseURL == "" {
		t.Skip("DATABASE_URL não configurado — pulando teste adversarial que exige Postgres real")
	}
	db, err := database.ConnectPostgres(cfg)
	if err != nil {
		t.Skipf("Postgres indisponível para o suite adversarial: %v", err)
	}
	t.Cleanup(db.Close)

	mailer := email.NewService(cfg)
	workerMgr := workers.NewWorkerManager(db, cfg, mailer, nil)
	srv := server.New(cfg, db, workerMgr, mailer)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &harness{Server: ts, DB: db, Cfg: cfg}
}

// signHMAC computes the hex-encoded HMAC-SHA256 the real handlers expect in
// x-efi-signature / x-chainfx-signature, using the harness's shared secret.
func signHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// seedBuyOrder creates a minimal, real "aguardando_pix" buy order through the
// same database.CreateBuyOrder path the actual /api/buy flow uses, so webhook
// attacks have a genuine settlement target and exercise the real
// ApplyBuyProviderWebhook / unique-index dedup path.
func seedBuyOrder(t *testing.T, db *database.DB) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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
		t.Fatalf("falha ao semear buy_order real para o ataque: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.Exec(`DELETE FROM buy_order_events WHERE buy_order_id = $1`, order.ID)
		_, _ = db.SQL.Exec(`DELETE FROM buy_orders WHERE id = $1`, order.ID)
	})
	return order.ID
}
