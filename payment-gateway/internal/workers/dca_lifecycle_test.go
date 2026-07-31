package workers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDCAOperationIDIsStablePerStrategyWindow(t *testing.T) {
	scheduled := time.Date(2026, 7, 30, 12, 0, 0, 123, time.FixedZone("BRT", -3*60*60))

	first := dcaOperationID("11111111-1111-1111-1111-111111111111", scheduled)
	second := dcaOperationID("11111111-1111-1111-1111-111111111111", scheduled.UTC())

	if first != second {
		t.Fatalf("operation id must be stable across timezone representations: %q != %q", first, second)
	}
	if !strings.Contains(first, "2026-07-30T15:00:00.000000123Z") {
		t.Fatalf("operation id must encode canonical UTC scheduled_at, got %q", first)
	}
}

func TestDCANextExecutionFromUsesScheduledWindow(t *testing.T) {
	base := time.Date(2026, 1, 31, 23, 59, 0, 0, time.FixedZone("BRT", -3*60*60))

	if got := nextExecutionFrom("daily", base); !got.Equal(base.UTC().Add(24 * time.Hour)) {
		t.Fatalf("daily next = %s", got)
	}
	if got := nextExecutionFrom("weekly", base); !got.Equal(base.UTC().Add(7 * 24 * time.Hour)) {
		t.Fatalf("weekly next = %s", got)
	}
	if got := nextExecutionFrom("monthly", base); !got.Equal(base.UTC().AddDate(0, 1, 0)) {
		t.Fatalf("monthly next = %s", got)
	}
}

func TestDCAMigrationDefinesEconomicInvariants(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "047_dca_lifecycle_hardening.sql")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	normalized := strings.Join(strings.Fields(sql), " ")
	for _, want := range []string{
		"uq_dca_executions_strategy_scheduled",
		"uq_dca_executions_operation_id",
		"uq_dca_active_strategy_config",
		"chk_dca_strategy_amount_positive",
		"chk_dca_execution_amount_positive",
		"cancelled_at",
		"reconciliation_hold_at",
		"mobile_wallet_ledger_entries",
		"dca_execution_reserve",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	for _, want := range []string{
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_dca_executions_strategy_scheduled ON dca_executions ( strategy_id, scheduled_at )",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_dca_executions_operation_id ON dca_executions ( operation_id )",
		"CREATE UNIQUE INDEX uq_dca_active_strategy_config ON dca_strategies ( user_id, token_symbol, network, amount_brl, frequency ) WHERE cancelled_at IS NULL AND reconciliation_hold_at IS NULL",
	} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("migration missing semantic invariant %q", want)
		}
	}
}

func TestDCAWorkerUsesAtomicFundingReserve(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "workers", "dca.go"))
	if err != nil {
		t.Fatalf("read worker: %v", err)
	}
	source := string(raw)
	for _, want := range []string{
		"reserveDCAFunding",
		"txCaptureDCAFunding",
		"txReleaseDCAFunding",
		"available_usdt_micro >= $2",
		"dca_execution_reserve",
		"dca_execution_capture",
		"dca_execution_release",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("worker missing %q", want)
		}
	}
}

func TestDCAMobileHandlersRemainOwnerScoped(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "mobile", "db.go"))
	if err != nil {
		t.Fatalf("read mobile db: %v", err)
	}
	source := string(raw)
	for _, want := range []string{
		"WHERE id=$1 AND user_id=$2 AND cancelled_at IS NULL",
		"return sql.ErrNoRows",
		"WHERE e.strategy_id=$1::uuid AND e.user_id=$2::uuid AND s.user_id=$2::uuid",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("mobile DCA ownership guard missing %q", want)
		}
	}
}
