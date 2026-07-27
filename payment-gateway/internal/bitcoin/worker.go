package bitcoin

import (
	"context"
	"log/slog"
	"time"
)

// BTCWorker executa dois loops em background:
//  1. depositScanner — varre todos os endereços ativos em busca de novos UTXOs.
//  2. confirmationTracker — atualiza confirmações das transações pendentes.
//
// Ambos são independentes da rail EVM e não tocam nenhum worker existente.
type BTCWorker struct {
	svc  *Service
	sink DepositEventSink // opcional; nil-safe; implementado em workers.btcEventSinkAdapter
}

// NewBTCWorker cria o worker. Retorna nil se svc for nil (BTC desabilitado).
func NewBTCWorker(svc *Service) *BTCWorker {
	if svc == nil {
		return nil
	}
	return &BTCWorker{svc: svc}
}

// SetSink registra um sink para publicação de eventos de depósito.
// Deve ser chamado antes de Start(). Nil é aceito (sem publicação).
func (w *BTCWorker) SetSink(sink DepositEventSink) {
	w.sink = sink
}

// Start inicia os dois loops e bloqueia até ctx ser cancelado.
func (w *BTCWorker) Start(ctx context.Context) {
	cfg := w.svc.Config()
	slog.Info("btc: worker iniciado",
		"network", cfg.Network,
		"deposit_interval", cfg.DepositScanInterval,
		"confirm_interval", cfg.TxScanInterval,
	)

	depositTicker := time.NewTicker(cfg.DepositScanInterval)
	confirmTicker := time.NewTicker(cfg.TxScanInterval)
	defer depositTicker.Stop()
	defer confirmTicker.Stop()

	// Executar imediatamente na inicialização
	w.scanDeposits(ctx)
	w.trackConfirmations(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("btc: worker encerrado")
			return
		case <-depositTicker.C:
			w.scanDeposits(ctx)
		case <-confirmTicker.C:
			w.trackConfirmations(ctx)
		}
	}
}

// scanDeposits varre todos os endereços ativos e sincroniza UTXOs via provider.
// Após cada sincronização, publica eventos para novos depósitos e depósitos confirmados.
func (w *BTCWorker) scanDeposits(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	addresses, err := w.svc.GetAllActiveAddresses(scanCtx)
	if err != nil {
		slog.Error("btc: erro ao buscar endereços ativos", "err", err)
		return
	}

	var maxBlock int64
	for _, addr := range addresses {
		select {
		case <-scanCtx.Done():
			return
		default:
		}

		events, blockHeight, err := w.svc.SyncAddressUTXOsWithEvents(scanCtx, addr)
		if err != nil {
			slog.Warn("btc: erro ao sincronizar UTXOs",
				"address", addr.Address, "err", err)
			continue
		}
		if blockHeight > maxBlock {
			maxBlock = blockHeight
		}

		// Publicar eventos para depósitos detectados/confirmados
		if w.sink != nil {
			for _, ev := range events {
				w.sink.PublishBTCEvent(ev.Type, ev.Payload)
			}
		}
	}

	// Atualizar btc_wallet_state com o bloco mais alto visto neste ciclo
	if maxBlock > 0 {
		if err := w.svc.repo.UpdateWalletState(scanCtx, string(w.svc.cfg.Network), maxBlock); err != nil {
			slog.Warn("btc: erro ao atualizar wallet_state", "err", err)
		}
	}
}

// trackConfirmations atualiza o status de transações em broadcast/pending.
// Publica evento btc.withdrawal.confirmed quando uma tx atinge min confirmações.
func (w *BTCWorker) trackConfirmations(ctx context.Context) {
	trackCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	pending, err := w.svc.GetPendingTransactions(trackCtx)
	if err != nil {
		slog.Error("btc: erro ao buscar transações pendentes", "err", err)
		return
	}

	for _, tx := range pending {
		select {
		case <-trackCtx.Done():
			return
		default:
		}
		wasConfirmed, err := w.svc.UpdateTransactionConfirmationWithResult(trackCtx, tx)
		if err != nil {
			slog.Warn("btc: erro ao atualizar confirmações",
				"txid", tx.Txid, "err", err)
			continue
		}
		if wasConfirmed && w.sink != nil && tx.Direction == TxDirectionWithdrawal {
			w.sink.PublishBTCEvent("btc.withdrawal.confirmed", map[string]any{
				"user_id":     tx.UserID,
				"txid":        tx.Txid,
				"amount_sats": tx.AmountSats,
				"fee_sats":    tx.FeeSats,
				"network":     tx.Network,
			})
		}
	}
}

// BTCWorkerEvent representa um evento produzido durante a sincronização.
type BTCWorkerEvent struct {
	Type    string
	Payload map[string]any
}
