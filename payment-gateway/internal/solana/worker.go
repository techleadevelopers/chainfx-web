package solana

import (
	"context"
	"log/slog"
	"time"
)

type WorkerEvent struct {
	Type    string
	Payload map[string]any
}

type Worker struct {
	svc  *Service
	sink EventSink
}

func NewWorker(svc *Service) *Worker {
	if svc == nil {
		return nil
	}
	return &Worker{svc: svc}
}

func (w *Worker) SetSink(sink EventSink) {
	w.sink = sink
}

func (w *Worker) Start(ctx context.Context) {
	cfg := w.svc.Config()
	slog.Info("solana: worker iniciado", "cluster", cfg.Cluster, "deposit_interval", cfg.ScanInterval, "tx_interval", cfg.TxScanInterval)
	depositTicker := time.NewTicker(cfg.ScanInterval)
	txTicker := time.NewTicker(cfg.TxScanInterval)
	defer depositTicker.Stop()
	defer txTicker.Stop()
	w.scanDeposits(ctx)
	w.trackWithdrawals(ctx)
	for {
		select {
		case <-ctx.Done():
			slog.Info("solana: worker encerrado")
			return
		case <-depositTicker.C:
			w.scanDeposits(ctx)
		case <-txTicker.C:
			w.trackWithdrawals(ctx)
		}
	}
}

func (w *Worker) scanDeposits(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	addresses, err := w.svc.ActiveAddresses(scanCtx)
	if err != nil {
		slog.Warn("solana: erro ao listar enderecos", "err", err)
		return
	}
	for _, addr := range addresses {
		events, err := w.svc.SyncAddress(scanCtx, addr)
		if err != nil {
			slog.Warn("solana: erro ao scanear endereco", "address", addr.Address, "err", err)
			continue
		}
		w.publish(events)
	}
}

func (w *Worker) trackWithdrawals(ctx context.Context) {
	trackCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	events, err := w.svc.TrackWithdrawals(trackCtx)
	if err != nil {
		slog.Warn("solana: erro ao rastrear withdrawals", "err", err)
		return
	}
	w.publish(events)
}

func (w *Worker) publish(events []WorkerEvent) {
	if w.sink == nil {
		return
	}
	for _, ev := range events {
		w.sink.PublishSolanaEvent(ev.Type, ev.Payload)
	}
}
