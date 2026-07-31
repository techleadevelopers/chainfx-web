package workers

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"payment-gateway/internal/database"

	_ "github.com/lib/pq"
)

func TestDCAOperationIDIsStablePerScheduledCycle(t *testing.T) {
	scheduledAt := time.Date(2026, 7, 30, 12, 0, 0, 123, time.FixedZone("BRT", -3*60*60))
	first := dcaOperationID("11111111-1111-1111-1111-111111111111", scheduledAt)
	second := dcaOperationID("11111111-1111-1111-1111-111111111111", scheduledAt.UTC())

	if first != second {
		t.Fatalf("operation id must be timezone-stable: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "dca:11111111-1111-1111-1111-111111111111:") {
		t.Fatalf("operation id must bind strategy and scheduled window, got %q", first)
	}
}

func TestNextExecutionFromUsesClaimedScheduleNotWallClock(t *testing.T) {
	scheduledAt := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)

	if got := nextExecutionFrom("daily", scheduledAt); !got.Equal(scheduledAt.Add(24 * time.Hour)) {
		t.Fatalf("daily next execution drifted: %s", got)
	}
	if got := nextExecutionFrom("weekly", scheduledAt); !got.Equal(scheduledAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("weekly next execution drifted: %s", got)
	}
	if got := nextExecutionFrom("monthly", scheduledAt); !got.Equal(scheduledAt.AddDate(0, 1, 0)) {
		t.Fatalf("monthly next execution drifted: %s", got)
	}
}

func TestDCAClaimExecutionWindowIsIdempotentInPostgres(t *testing.T) {
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
	migration, err := os.ReadFile("../../migrations/047_dca_lifecycle_hardening.sql")
	if err != nil {
		t.Fatalf("read dca hardening migration: %v", err)
	}
	if _, err := db.SQL.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply dca hardening migration: %v", err)
	}

	userID := "20000000-0000-0000-0000-000000000047"
	strategyID := "20000000-0000-0000-0000-000000000048"
	scheduledAt := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM dca_executions WHERE strategy_id=$1::uuid`, strategyID)
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM dca_strategies WHERE id=$1::uuid`, strategyID)
		_, _ = db.SQL.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, userID)
	})
	_, _ = db.SQL.ExecContext(ctx, `DELETE FROM dca_executions WHERE strategy_id=$1::uuid`, strategyID)
	_, _ = db.SQL.ExecContext(ctx, `DELETE FROM dca_strategies WHERE id=$1::uuid`, strategyID)
	_, _ = db.SQL.ExecContext(ctx, `DELETE FROM users WHERE id=$1::uuid`, userID)
	if _, err := db.SQL.ExecContext(ctx, `
		INSERT INTO users (id, email, wallet_address, created_at)
		VALUES ($1::uuid, 'dca-hardening@example.com', '0x0000000000000000000000000000000000000047', NOW())`, userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.SQL.ExecContext(ctx, `
		INSERT INTO dca_strategies (id, user_id, token_symbol, network, amount_brl, frequency, active, next_execution)
		VALUES ($1::uuid, $2::uuid, 'USDT', 'BSC', 100, 'daily', true, $3)`, strategyID, userID, scheduledAt); err != nil {
		t.Fatalf("insert strategy: %v", err)
	}

	worker := &DCAWorker{db: db}
	strategy := dcaStrategy{
		ID: strategyID, UserID: userID, TokenSymbol: "USDT", Network: "BSC",
		AmountBRL: 100, Frequency: "daily", ScheduledAt: scheduledAt,
	}

	tx, err := db.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin first tx: %v", err)
	}
	execID, claimed, err := worker.claimExecutionWindow(ctx, tx, strategy)
	if err != nil || !claimed || execID == "" {
		_ = tx.Rollback()
		t.Fatalf("first claim = id %q claimed %v err %v", execID, claimed, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit first claim: %v", err)
	}

	tx, err = db.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin second tx: %v", err)
	}
	secondID, secondClaimed, err := worker.claimExecutionWindow(ctx, tx, strategy)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if secondClaimed || secondID != "" {
		t.Fatalf("duplicate scheduled cycle must not be claimed: id %q claimed %v", secondID, secondClaimed)
	}

	var count int
	if err := db.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM dca_executions WHERE strategy_id=$1::uuid AND scheduled_at=$2`, strategyID, scheduledAt).Scan(&count); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one economic execution for scheduled cycle, got %d", count)
	}
}
