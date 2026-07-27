package workers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"payment-gateway/internal/bitcoin"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/email"
	"payment-gateway/internal/paymaster"
	"payment-gateway/internal/psp"
	"payment-gateway/internal/rpc"
	"payment-gateway/internal/solana"
)

// pspHealthProbeInterval controls how often the PSP Router's providers are
// health-checked when a Router is wired.
const pspHealthProbeInterval = time.Minute

// pspHealthProbeConcurrency bounds how many in-flight ProbeAll calls may be
// queued at once. HealthCheck hits a real HTTP endpoint (Efí OAuth + /v2/cob),
// so if the provider is slow this stops probe goroutines piling up unbounded.
const pspHealthProbeConcurrency = 10

type WorkerManager struct {
	Bus                 *EventBus
	PriceWorker         *PriceWorker
	PayoutWorker        *PayoutWorker
	BuySendWorker       *BuySendWorker
	DCAWorker           *DCAWorker
	OnchainWorker       *OnchainWorker
	SellExpiryWorker    *SellExpiryWorker
	SweepWorker         *SweepWorker
	EmailWorker         *EmailWorker
	KYCWorker           *KYCWorker
	M2MSettlementWorker *M2MSettlementWorker
	AutoSweeperWorker   *AutoSweeperWorker
	NFCExpirationWorker *NFCExpirationWorker
	NFCSettlementWorker *NFCMerchantSettlementWorker
	NFCReconcileWorker  *NFCSettlementReconciliationWorker
	PaymasterService    *paymaster.Service
	PSPRouter           *psp.Router // optional; set by cmd/api/main.go before StartAll when Efí is configured
	// BTC rail — nil when BTC_ENABLED=false or when the BTC config is absent.
	BTCSvc       *bitcoin.Service
	BTCWorker    *bitcoin.BTCWorker
	SolanaSvc    *solana.Service
	SolanaWorker *solana.Worker
	db           *database.DB
	cfg          *config.Config
	wg           sync.WaitGroup
}

func NewWorkerManager(db *database.DB, cfg *config.Config, mailer *email.Service, pool *rpc.Pool) *WorkerManager {
	bus := NewEventBus()

	var paymasterSvc *paymaster.Service
	if pool != nil {
		paymasterSvc = paymaster.NewService(cfg, db, pool)
	}

	// BTC rail — nil-safe: NewService returns (nil, nil) when BTC_ENABLED=false.
	btcSvc, err := bitcoin.NewService(db)
	if err != nil {
		slog.Warn("btc: falha ao inicializar serviço Bitcoin, rail desabilitada", "error", err)
		btcSvc = nil
	}
	btcWorker := bitcoin.NewBTCWorker(btcSvc)
	// Wire event sink so the BTC worker publishes deposit/withdrawal events to the main bus.
	if btcWorker != nil {
		btcWorker.SetSink(&btcEventSinkAdapter{bus: bus})
	}
	solanaSvc, err := solana.NewService(db, cfg)
	if err != nil {
		slog.Warn("solana: falha ao inicializar servico, rail desabilitada", "error", err)
		solanaSvc = nil
	}
	solanaWorker := solana.NewWorker(solanaSvc)
	if solanaWorker != nil {
		solanaWorker.SetSink(&solanaEventSinkAdapter{bus: bus})
	}

	priceWorker := NewPriceWorker(bus)
	return &WorkerManager{
		Bus:                 bus,
		PriceWorker:         priceWorker,
		PayoutWorker:        NewPayoutWorker(bus, db, cfg),
		BuySendWorker:       NewBuySendWorker(bus, db, cfg),
		DCAWorker:           NewDCAWorker(bus, db, cfg, priceWorker),
		OnchainWorker:       NewOnchainWorker(bus, db, cfg),
		SellExpiryWorker:    NewSellExpiryWorker(db),
		SweepWorker:         NewSweepWorker(bus, db, cfg),
		EmailWorker:         NewEmailWorker(bus, db, mailer),
		KYCWorker:           NewKYCWorker(bus, db, cfg),
		M2MSettlementWorker: NewM2MSettlementWorker(bus, db, cfg),
		AutoSweeperWorker:   NewAutoSweeperWorker(cfg, db, pool),
		NFCExpirationWorker: NewNFCExpirationWorker(bus, db, cfg),
		NFCSettlementWorker: NewNFCMerchantSettlementWorker(bus, db, cfg),
		NFCReconcileWorker:  NewNFCSettlementReconciliationWorker(db, cfg),
		PaymasterService:    paymasterSvc,
		BTCSvc:              btcSvc,
		BTCWorker:           btcWorker,
		SolanaSvc:           solanaSvc,
		SolanaWorker:        solanaWorker,
		db:                  db,
		cfg:                 cfg,
	}
}

// StartAll starts every worker in its own goroutine.
func (wm *WorkerManager) StartAll(ctx context.Context) {
	slog.Info("Iniciando todos os workers...")

	workerCount := 15 // base workers + DCA + KYC + AutoSweeper + Paymaster + NFC expiration + NFC settlement + reconciliation
	if wm.PSPRouter != nil {
		workerCount++ // + PSP health probe
	}
	if wm.BTCWorker != nil {
		workerCount++ // + BTC deposit scanner + confirmation tracker
	}
	if wm.SolanaWorker != nil {
		workerCount++ // + Solana deposit scanner + confirmation tracker
	}
	wm.wg.Add(workerCount)

	go func() {
		defer wm.wg.Done()
		wm.PriceWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.PayoutWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.BuySendWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.DCAWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.OnchainWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.SellExpiryWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.SweepWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.EmailWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.KYCWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.M2MSettlementWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.AutoSweeperWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.NFCExpirationWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.NFCSettlementWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		wm.NFCReconcileWorker.Start(ctx)
	}()

	go func() {
		defer wm.wg.Done()
		if wm.PaymasterService != nil {
			wm.PaymasterService.Start(ctx)
		}
	}()

	if wm.PSPRouter != nil {
		go func() {
			defer wm.wg.Done()
			wm.runPSPHealthProbe(ctx)
		}()
	}

	if wm.BTCWorker != nil {
		go func() {
			defer wm.wg.Done()
			wm.BTCWorker.Start(ctx)
		}()
	}
	if wm.SolanaWorker != nil {
		go func() {
			defer wm.wg.Done()
			wm.SolanaWorker.Start(ctx)
		}()
	}

	slog.Info("Todos os workers iniciados com sucesso", "count", workerCount)
}

// runPSPHealthProbe periodically calls PSPRouter.ProbeAll, which health-checks
// the active + backup PIX providers and drives the Router's auto-failover /
// auto-restore logic. A semaphore bounds concurrent in-flight probes so a slow
// or hanging provider can't pile up goroutines across missed ticks.
func (wm *WorkerManager) runPSPHealthProbe(ctx context.Context) {
	slog.Info("psp: health probe iniciado", "interval", pspHealthProbeInterval, "activeProvider", wm.PSPRouter.ActiveProvider())

	sem := make(chan struct{}, pspHealthProbeConcurrency)
	ticker := time.NewTicker(pspHealthProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case sem <- struct{}{}:
				go func() {
					defer func() { <-sem }()
					wm.PSPRouter.ProbeAll(ctx)
				}()
			default:
				slog.Warn("psp: health probe skipped, too many in-flight checks", "limit", pspHealthProbeConcurrency)
			}
		}
	}
}

// Shutdown aguarda todos os workers finalizarem
func (wm *WorkerManager) Shutdown(ctx context.Context) {
	slog.Info("Iniciando shutdown dos workers...")

	// Fecha o EventBus primeiro para parar de receber novos eventos
	wm.Bus.Shutdown()

	// Aguarda todos os workers finalizarem com timeout
	done := make(chan struct{})
	go func() {
		wm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("Todos os workers finalizados com sucesso")
	case <-ctx.Done():
		slog.Warn("Timeout no shutdown dos workers", "timeout", ctx.Err())
	}
}

// StartAllAndWait inicia os workers e aguarda o contexto ser cancelado
func (wm *WorkerManager) StartAllAndWait(ctx context.Context) {
	wm.StartAll(ctx)
	<-ctx.Done()
	wm.Shutdown(context.Background())
}

// ─── BTC event sink adapter ───────────────────────────────────────────────────

// btcEventSinkAdapter implementa bitcoin.DepositEventSink traduzindo eventos BTC
// para o EventBus interno do ChainFX. Sem importação circular: a interface é
// definida no pacote bitcoin, a implementação concreta fica aqui no pacote workers.
type btcEventSinkAdapter struct {
	bus *EventBus
}

func (a *btcEventSinkAdapter) PublishBTCEvent(eventType string, payload map[string]any) {
	if a.bus == nil {
		return
	}
	orderID, _ := payload["user_id"].(string) // melhor identificador disponível
	a.bus.Publish(Event{
		Type:    eventType,
		OrderID: orderID,
		Payload: payload,
	})
}

type solanaEventSinkAdapter struct {
	bus *EventBus
}

func (a *solanaEventSinkAdapter) PublishSolanaEvent(eventType string, payload map[string]any) {
	if a.bus == nil {
		return
	}
	orderID, _ := payload["user_id"].(string)
	a.bus.Publish(Event{Type: eventType, OrderID: orderID, Payload: payload})
}
