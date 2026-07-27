package workers

import (
	"context"
	"log/slog"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/metrics"
)

const nfcSettlementReconciliationInterval = 30 * time.Second

type NFCSettlementReconciliationWorker struct {
	db  *database.DB
	cfg *config.Config
}

func NewNFCSettlementReconciliationWorker(db *database.DB, cfg *config.Config) *NFCSettlementReconciliationWorker {
	return &NFCSettlementReconciliationWorker{db: db, cfg: cfg}
}

func (w *NFCSettlementReconciliationWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(nfcSettlementReconciliationInterval)
	defer ticker.Stop()
	slog.Info("NFCSettlementReconciliationWorker iniciado", "interval", nfcSettlementReconciliationInterval)
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			slog.Info("NFCSettlementReconciliationWorker: encerrando")
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *NFCSettlementReconciliationWorker) runOnce(ctx context.Context) {
	start := time.Now()
	if w == nil || w.db == nil {
		return
	}
	balance, minBuffer := 0.0, 0.0
	if w.cfg != nil {
		balance = w.cfg.NFCEfiBalanceBRL
		minBuffer = w.cfg.NFCEfiMinBufferBRL
	}
	report, err := w.db.ReconcileNFCMerchantSettlements(ctx, balance, minBuffer)
	if err != nil {
		slog.Error("NFC settlement reconciliation failed", "err", err)
		return
	}
	metrics.SetNFCSettlementSnapshot(metrics.NFCSettlementSnapshot{
		Counts:                     report.Snapshot.Counts,
		AnomalyCounts:              report.Snapshot.AnomalyCounts,
		QueueAgeSeconds:            report.Snapshot.QueueAgeSeconds,
		SubmitLatencySeconds:       report.Snapshot.SubmitLatencySeconds,
		ConfirmationLatencySeconds: report.Snapshot.ConfirmationLatencySeconds,
		EndToEndSeconds:            report.Snapshot.EndToEndSeconds,
		TreasurySnapshotAgeSeconds: report.Snapshot.TreasurySnapshotAgeSeconds,
		EfiBalanceBRL:              report.Snapshot.EfiBalanceBRL,
		EfiPendingBRL:              report.Snapshot.PendingBRL,
		EfiSubmittedBRL:            report.Snapshot.SubmittedBRL,
		EfiReservedBRL:             report.Snapshot.ReservedBRL,
		EfiMinBufferBRL:            report.Snapshot.EfiMinBufferBRL,
		EfiAvailableRealBRL:        report.Snapshot.EfiAvailableRealBRL,
		ReconciliationLastSuccess:  float64(time.Now().Unix()),
		ReconciliationDuration:     time.Since(start).Seconds(),
	})
	for _, issue := range report.Issues {
		slog.Warn("NFC settlement reconciliation anomaly",
			"type", issue.Type,
			"severity", issue.Severity,
			"authorization_id", issue.AuthorizationID.String,
			"settlement_id", issue.SettlementID.String,
			"merchant_id", issue.MerchantID.String,
			"details", issue.Details)
	}
	if report.Snapshot.EfiAvailableRealBRL < 0 {
		slog.Warn("NFC Efí treasury liquidity below projected buffer",
			"efi_balance_brl", report.Snapshot.EfiBalanceBRL,
			"pending_brl", report.Snapshot.PendingBRL,
			"submitted_brl", report.Snapshot.SubmittedBRL,
			"min_buffer_brl", report.Snapshot.EfiMinBufferBRL,
			"available_real_brl", report.Snapshot.EfiAvailableRealBRL)
	}
}
