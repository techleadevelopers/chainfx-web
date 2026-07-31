package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"payment-gateway/internal/config"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func TestNFCHoldExpiredAtBoundary(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	past := now.Add(-time.Nanosecond)
	same := now
	future := now.Add(time.Nanosecond)

	if !nfcHoldExpiredAt(&past, now) {
		t.Fatal("past hold must be expired")
	}
	if !nfcHoldExpiredAt(&same, now) {
		t.Fatal("hold_expires_at == now must be expired")
	}
	if nfcHoldExpiredAt(&future, now) {
		t.Fatal("future hold must still be valid")
	}
	if nfcHoldExpiredAt(nil, now) {
		t.Fatal("nil hold expiration must not be treated as expired")
	}
}

func TestNFCCaptureExpiryGuardAgainstConfiguredPostgres(t *testing.T) {
	if os.Getenv("CHAINFX_NFC_DB_TEST") != "1" {
		t.Skip("set CHAINFX_NFC_DB_TEST=1 to run NFC capture expiry integration tests against configured DATABASE_URL")
	}
	db := openNFCTestDB(t)
	ctx := context.Background()

	t.Run("future hold captures", func(t *testing.T) {
		fixture := seedNFCCaptureFixture(t, db, NFCStatusApproved, time.Now().UTC().Add(time.Minute), 70_000_000, 30_000_000, 30_000_000)
		result, err := db.CaptureNFCAuthorization(ctx, fixture.authID)
		if err != nil {
			t.Fatalf("CaptureNFCAuthorization valid hold: %v", err)
		}
		if result == nil || result.Authorization == nil || result.Authorization.Status != NFCStatusCaptured {
			t.Fatalf("expected captured result, got %#v", result)
		}
		assertNFCBalance(t, db, fixture.wallet, 70_000_000, 0)
		assertNFCSettlementCount(t, db, fixture.authID, 1)
	})

	t.Run("expired approved hold expires inline without settlement", func(t *testing.T) {
		fixture := seedNFCCaptureFixture(t, db, NFCStatusApproved, time.Now().UTC().Add(-time.Minute), 70_000_000, 30_000_000, 30_000_000)
		if result, err := db.CaptureNFCAuthorization(ctx, fixture.authID); err == nil || result != nil {
			t.Fatalf("expected expired capture rejection, result=%#v err=%v", result, err)
		}
		assertNFCAuthorizationStatus(t, db, fixture.authID, NFCStatusExpired)
		assertNFCBalance(t, db, fixture.wallet, 100_000_000, 0)
		assertNFCSettlementCount(t, db, fixture.authID, 0)

		if result, err := db.CaptureNFCAuthorization(ctx, fixture.authID); err == nil || result != nil {
			t.Fatalf("expected repeated expired capture rejection, result=%#v err=%v", result, err)
		}
		assertNFCBalance(t, db, fixture.wallet, 100_000_000, 0)
		expired, err := db.ExpireNFCHolds(ctx, 10)
		if err != nil {
			t.Fatalf("ExpireNFCHolds after inline expiry: %v", err)
		}
		if len(expired) != 0 {
			t.Fatalf("expected no second expiry, got %d", len(expired))
		}
		assertNFCBalance(t, db, fixture.wallet, 100_000_000, 0)
		assertNFCSettlementCount(t, db, fixture.authID, 0)
	})

	t.Run("already expired hold rejects capture", func(t *testing.T) {
		fixture := seedNFCCaptureFixture(t, db, NFCStatusExpired, time.Now().UTC().Add(-time.Minute), 100_000_000, 0, 30_000_000)
		if result, err := db.CaptureNFCAuthorization(ctx, fixture.authID); err == nil || result != nil {
			t.Fatalf("expected status expired capture rejection, result=%#v err=%v", result, err)
		}
		assertNFCBalance(t, db, fixture.wallet, 100_000_000, 0)
		assertNFCSettlementCount(t, db, fixture.authID, 0)
	})

	t.Run("reversed hold rejects capture", func(t *testing.T) {
		fixture := seedNFCCaptureFixture(t, db, NFCStatusReversed, time.Now().UTC().Add(time.Minute), 100_000_000, 0, 30_000_000)
		if result, err := db.CaptureNFCAuthorization(ctx, fixture.authID); err == nil || result != nil {
			t.Fatalf("expected reversed capture rejection, result=%#v err=%v", result, err)
		}
		assertNFCBalance(t, db, fixture.wallet, 100_000_000, 0)
		assertNFCSettlementCount(t, db, fixture.authID, 0)
	})

	t.Run("capture and expiry race releases once", func(t *testing.T) {
		fixture := seedNFCCaptureFixture(t, db, NFCStatusApproved, time.Now().UTC().Add(-time.Minute), 70_000_000, 30_000_000, 30_000_000)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = db.CaptureNFCAuthorization(ctx, fixture.authID)
		}()
		go func() {
			defer wg.Done()
			_, _ = db.ExpireNFCHolds(ctx, 10)
		}()
		wg.Wait()
		assertNFCAuthorizationStatus(t, db, fixture.authID, NFCStatusExpired)
		assertNFCBalance(t, db, fixture.wallet, 100_000_000, 0)
		assertNFCSettlementCount(t, db, fixture.authID, 0)
	})

	t.Run("database now boundary is expired", func(t *testing.T) {
		fixture := seedNFCCaptureFixtureWithDBNow(t, db)
		if result, err := db.CaptureNFCAuthorization(ctx, fixture.authID); err == nil || result != nil {
			t.Fatalf("expected boundary capture rejection, result=%#v err=%v", result, err)
		}
		assertNFCAuthorizationStatus(t, db, fixture.authID, NFCStatusExpired)
		assertNFCBalance(t, db, fixture.wallet, 100_000_000, 0)
		assertNFCSettlementCount(t, db, fixture.authID, 0)
	})
}

type nfcCaptureFixture struct {
	authID string
	wallet string
}

func openNFCTestDB(t *testing.T) *DB {
	t.Helper()
	_ = godotenv.Load("../../.env")
	cfg := config.LoadConfig()
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		t.Skip("DATABASE_URL is empty")
	}
	sqlDB, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db := &DB{SQL: sqlDB}
	if err := db.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

func seedNFCCaptureFixture(t *testing.T, db *DB, status string, holdExpiresAt time.Time, available, locked, required int64) nfcCaptureFixture {
	t.Helper()
	return seedNFCCaptureFixtureWithHoldExpr(t, db, status, "$10", []any{holdExpiresAt.UTC()}, available, locked, required)
}

func seedNFCCaptureFixtureWithDBNow(t *testing.T, db *DB) nfcCaptureFixture {
	t.Helper()
	return seedNFCCaptureFixtureWithHoldExpr(t, db, NFCStatusApproved, "NOW()", nil, 70_000_000, 30_000_000, 30_000_000)
}

func seedNFCCaptureFixtureWithHoldExpr(t *testing.T, db *DB, status, holdExpr string, holdArgs []any, available, locked, required int64) nfcCaptureFixture {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(NewID(), "-", "")
	fixture := nfcCaptureFixture{
		authID: "nfc_auth_exp_" + suffix,
		wallet: "0x0000000000000000000000000000000000" + suffix[len(suffix)-6:],
	}
	merchantID := "merchant_exp_" + suffix
	terminalID := "terminal_exp_" + suffix
	args := []any{merchantID, terminalID, fixture.wallet, available, locked, fixture.authID, "idem_" + suffix, "tok_" + suffix, "hash_" + suffix}
	args = append(args, holdArgs...)

	if _, err := db.SQL.ExecContext(ctx, `INSERT INTO nfc_merchants (id, display_name, status, settlement_pix_key) VALUES ($1,$1,'active','merchant@example.com')`, merchantID); err != nil {
		t.Fatalf("insert merchant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM nfc_merchants WHERE id=$1`, merchantID)
	})
	if _, err := db.SQL.ExecContext(ctx, `INSERT INTO nfc_wallet_balances (wallet_address, network, asset, available_usdt_micro, locked_usdt_micro) VALUES ($1,'BSC','USDT',$2,$3)`, fixture.wallet, available, locked); err != nil {
		t.Fatalf("insert balance: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM nfc_wallet_balances WHERE wallet_address=$1`, fixture.wallet)
	})
	requiredParam := len(args) + 1
	args = append(args, required)
	statusParam := len(args) + 1
	args = append(args, status)
	query := fmt.Sprintf(`
INSERT INTO nfc_authorizations
  (id, idempotency_key, token_id, token_hash, wallet_address, network, merchant_id, terminal_id,
   amount_brl_minor, fee_brl_minor, total_brl_minor, fee_bps, usdt_rate, required_usdt_micro,
   status, response_code, reason, hold_expires_at)
VALUES ($6,$7,$8,$9,$3,'BSC',$1,$2,30000,0,30000,0,1.0,$%d,$%d,'00','test',%s)`, requiredParam, statusParam, holdExpr)
	if _, err := db.SQL.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("insert authorization: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM merchant_settlements WHERE authorization_id=$1`, fixture.authID)
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM nfc_authorizations WHERE id=$1`, fixture.authID)
	})
	return fixture
}

func assertNFCBalance(t *testing.T, db *DB, wallet string, wantAvailable, wantLocked int64) {
	t.Helper()
	bal, err := db.GetNFCBalance(context.Background(), wallet, "BSC")
	if err != nil {
		t.Fatalf("GetNFCBalance: %v", err)
	}
	if bal.AvailableMicro != wantAvailable || bal.LockedMicro != wantLocked {
		t.Fatalf("balance mismatch: available=%d locked=%d, want available=%d locked=%d", bal.AvailableMicro, bal.LockedMicro, wantAvailable, wantLocked)
	}
}

func assertNFCAuthorizationStatus(t *testing.T, db *DB, authID, want string) {
	t.Helper()
	auth, err := db.GetNFCAuthorization(context.Background(), authID)
	if err != nil {
		t.Fatalf("GetNFCAuthorization: %v", err)
	}
	if auth == nil || auth.Status != want {
		t.Fatalf("authorization status = %#v, want %s", auth, want)
	}
}

func assertNFCSettlementCount(t *testing.T, db *DB, authID string, want int) {
	t.Helper()
	var got int
	if err := db.SQL.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM merchant_settlements WHERE authorization_id=$1`, authID).Scan(&got); err != nil {
		t.Fatalf("count merchant_settlements: %v", err)
	}
	if got != want {
		t.Fatalf("merchant_settlements count=%d, want %d", got, want)
	}
}
