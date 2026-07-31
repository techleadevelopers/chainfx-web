package workers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/bitcoin"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/liquidity"
)

// BTCFundingSource é a interface que abstrai qualquer fonte de liquidez BTC.
//
// Implementações:
//   - TreasuryFundingSource → envia BTC da carteira operacional da plataforma
//   - BingXFundingSource    → compra e retira via BingX (rota atual)
//
// A interface permite que o BTCFundingRouter decida a rota sem espalhar
// lógica de treasury/BingX por vários arquivos.
type BTCFundingSource interface {
	// Name retorna o nome auditável da fonte (ex: "treasury", "bingx").
	Name() string
	// CanFund verifica se esta fonte pode atender a ordem (saldo, configuração, limites).
	// Deve ser rápido (< 3s); sem efeito colateral.
	CanFund(ctx context.Context, buy *database.BuyOrder) (bool, error)
	// Fund executa o envio e atualiza o status da ordem no DB.
	// Retorna (true, nil) se a ordem foi entregue ou marcada como pendente_confirmacao.
	// Retorna (false, err) se a fonte falhou DEFINITIVAMENTE antes de qualquer broadcast —
	// neste caso o router pode tentar a próxima fonte.
	// NUNCA retorna (false, ...) depois de broadcast ou broadcast_unknown.
	Fund(ctx context.Context, buy *database.BuyOrder) (handled bool, err error)
}

// BTCFundingRouter roteia ordens BUY BTC:
//
//   Treasury → BingX (fallback)
//
// Regras de fallback:
//   - Usar BingX somente quando: Treasury desabilitada, não configurada,
//     saldo insuficiente, ou falha definitiva antes do broadcast.
//   - NUNCA usar BingX se Treasury já iniciou broadcast (mesmo broadcast_unknown).
//   - Uma ordem só passa por UMA rota de cada vez.
//
// A decisão de roteamento é centralizada aqui — nenhum caller precisa conhecer
// as condições internas de cada fonte.
type BTCFundingRouter struct {
	sources []BTCFundingSource // ordem de preferência: treasury primeiro
	db      *database.DB
	bus     *EventBus
}

// NewBTCFundingRouter cria o router. treasury pode ser nil (Treasury desabilitada).
// bingxRouter pode ser nil (BingX desabilitado).
func NewBTCFundingRouter(
	treasury *bitcoin.TreasuryService,
	bingxRouter *liquidity.Router,
	db *database.DB,
	bus *EventBus,
	cfg *config.Config,
	httpClient *http.Client,
) *BTCFundingRouter {
	var sources []BTCFundingSource

	if treasury != nil {
		sources = append(sources, &TreasuryFundingSource{svc: treasury, db: db})
	}

	if bingxRouter != nil {
		sources = append(sources, &BingXFundingSource{
			router: bingxRouter,
			db:     db,
			cfg:    cfg,
			client: httpClient,
		})
	}

	return &BTCFundingRouter{sources: sources, db: db, bus: bus}
}

// Enabled retorna true se há pelo menos uma fonte configurada.
func (r *BTCFundingRouter) Enabled() bool {
	return r != nil && len(r.sources) > 0
}

// Route tenta fundar a ordem BUY BTC percorrendo as fontes em ordem de preferência.
//
// Retorna true se a ordem foi tratada (success ou pendente_confirmacao).
// Retorna false apenas se NENHUMA fonte estava disponível.
//
// Invariante crítico:
//   Uma vez que uma fonte iniciou o processo de broadcast (ou retornou broadcast_unknown),
//   o router para imediatamente — NÃO tenta a próxima fonte.
func (r *BTCFundingRouter) Route(ctx context.Context, buy *database.BuyOrder) bool {
	if r == nil || buy == nil || len(r.sources) == 0 {
		return false
	}

	orderID := buy.ID
	asset := strings.ToUpper(strings.TrimSpace(buy.Asset))
	network := strings.ToUpper(strings.TrimSpace(buy.Network))

	if asset != "BTC" || network != "BITCOIN" {
		slog.Warn("btc/router: Route chamado para par não-BTC", "asset", asset, "network", network, "order_id", orderID)
		return false
	}

	for _, src := range r.sources {
		// 1. Verificar se esta fonte pode atender a ordem
		canCtx, canCancel := context.WithTimeout(ctx, 5*time.Second)
		canFund, canErr := src.CanFund(canCtx, buy)
		canCancel()

		if canErr != nil {
			slog.Warn("btc/router: CanFund falhou",
				"source", src.Name(), "order_id", orderID, "error", canErr)
			continue
		}
		if !canFund {
			slog.Info("btc/router: fonte indisponível ou saldo insuficiente",
				"source", src.Name(), "order_id", orderID)
			continue
		}

		// 2. Tentar financiar
		slog.Info("btc/router: tentando fonte",
			"source", src.Name(), "order_id", orderID,
			"amount_sats", btcToSats(buy.CryptoAmount))

		handled, fundErr := src.Fund(ctx, buy)
		if handled {
			// Ordem tratada (success ou pendente_confirmacao) — parar aqui
			if fundErr != nil {
				slog.Warn("btc/router: fonte retornou handled=true com erro (broadcast_unknown ou pendente_confirmacao)",
					"source", src.Name(), "order_id", orderID, "error", fundErr)
			} else {
				slog.Info("btc/router: ordem entregue com sucesso",
					"source", src.Name(), "order_id", orderID)
			}
			return true
		}

		// Fonte falhou definitivamente antes do broadcast — pode tentar próxima
		slog.Warn("btc/router: fonte falhou; tentando próxima",
			"source", src.Name(), "order_id", orderID, "error", fundErr)

		// Registrar evento de fallback para auditoria
		_ = r.db.AddBuyEvent(ctx, orderID, "buy.btc.funding.fallback", map[string]any{
			"failed_source": src.Name(),
			"error":         errMsg(fundErr),
		})
	}

	// Nenhuma fonte disponível
	slog.Error("btc/router: todas as fontes BTC falharam ou indisponíveis",
		"order_id", orderID)
	return false
}

// ─── TreasuryFundingSource ────────────────────────────────────────────────────

// TreasuryFundingSource implementa BTCFundingSource usando a Treasury operacional.
type TreasuryFundingSource struct {
	svc *bitcoin.TreasuryService
	db  *database.DB
}

func (s *TreasuryFundingSource) Name() string { return "treasury" }

// CanFund verifica se a Treasury tem saldo suficiente para a ordem.
func (s *TreasuryFundingSource) CanFund(ctx context.Context, buy *database.BuyOrder) (bool, error) {
	if s == nil || s.svc == nil {
		return false, nil
	}
	cfg := s.svc.Config()
	tcfg := s.svc.TreasuryConfig()
	if cfg == nil || tcfg == nil || !tcfg.Enabled {
		return false, nil
	}
	if cfg.EmergencyLockdown {
		slog.Warn("btc/treasury: BTC_EMERGENCY_LOCKDOWN ativo — Treasury indisponível")
		return false, nil
	}
	if !cfg.WithdrawalsEnabled {
		slog.Info("btc/treasury: BTC_WITHDRAWALS_ENABLED=false — Treasury indisponível")
		return false, nil
	}

	amountSats := btcToSats(buy.CryptoAmount)
	if amountSats <= 0 {
		return false, errors.New("btc/treasury: amount inválido")
	}

	balance, err := s.svc.AvailableBalance(ctx, amountSats)
	if err != nil {
		return false, err
	}

	if !balance.HasSufficientFunds(amountSats) {
		slog.Info("btc/treasury: saldo insuficiente",
			"spendable_sats", balance.SpendableSats,
			"required_sats", amountSats,
			"confirmed_sats", balance.ConfirmedSats,
			"min_reserve_sats", balance.MinReserveSats,
			"estimated_fee", balance.EstimatedFee,
		)
		return false, nil
	}

	return true, nil
}

// Fund executa o envio via Treasury e atualiza o status da buy order no DB.
func (s *TreasuryFundingSource) Fund(ctx context.Context, buy *database.BuyOrder) (bool, error) {
	amountSats := btcToSats(buy.CryptoAmount)

	result, err := s.svc.SendBTC(ctx, bitcoin.TreasurySendRequest{
		OrderID:    buy.ID,
		ToAddress:  buy.DestAddress,
		AmountSats: amountSats,
	})
	if err != nil {
		// Falha definitiva antes do broadcast — o router pode tentar BingX
		return false, err
	}

	// Broadcast bem-sucedido ou broadcast_unknown
	switch result.Status {
	case bitcoin.TxStatusBroadcast, bitcoin.TxStatusConfirmed:
		txHash := result.TxID
		if txHash == "" {
			txHash = "treasury-" + buy.ID
		}
		if dbErr := s.db.UpdateBuyOrderStatus(ctx, buy.ID, "enviado", map[string]any{
			"txHashOut":      txHash,
			"provider":       "treasury",
			"funding_source": "treasury",
			"fee_sats":       result.FeeSats,
			"signer_key_id":  result.SignerKeyID,
		}); dbErr != nil {
			slog.Error("btc/treasury: falha ao atualizar BUY enviado",
				"order_id", buy.ID, "error", dbErr)
			return true, dbErr
		}
		return true, nil

	case bitcoin.TxStatusBroadcastUnknown:
		// Resultado incerto — NÃO fallback para BingX.
		// Reconciliação resolverá via btc_treasury_operations.
		txHash := result.TxID
		if txHash == "" {
			txHash = "treasury-unknown-" + buy.ID
		}
		if dbErr := s.db.UpdateBuyOrderStatus(ctx, buy.ID, "pendente_confirmacao", map[string]any{
			"txHashOut":      txHash,
			"provider":       "treasury",
			"funding_source": "treasury",
			"note":           "broadcast_unknown — aguardando reconciliação; NÃO usar BingX",
		}); dbErr != nil {
			slog.Error("btc/treasury: falha ao atualizar BUY pendente_confirmacao",
				"order_id", buy.ID, "error", dbErr)
		}
		// handled=true: o router não deve tentar BingX
		return true, errors.New("broadcast_unknown — ordem em pendente_confirmacao; reconciliação necessária")

	default:
		// Status inesperado — tratar como falha segura (não broadcast)
		return false, fmt.Errorf("btc/treasury: status inesperado %q após SendBTC", result.Status)
	}
}

// ─── BingXFundingSource ───────────────────────────────────────────────────────

// BingXFundingSource implementa BTCFundingSource usando o liquidity router existente (BingX).
// Encapsula a lógica já presente em tryLiquidityExecution para BTC:BITCOIN.
type BingXFundingSource struct {
	router *liquidity.Router
	db     *database.DB
	cfg    *config.Config
	client *http.Client
}

func (s *BingXFundingSource) Name() string { return "bingx" }

// CanFund verifica se o BingX está configurado e permite BTC:BITCOIN.
func (s *BingXFundingSource) CanFund(_ context.Context, buy *database.BuyOrder) (bool, error) {
	if s == nil || s.router == nil || s.cfg == nil {
		return false, nil
	}
	if !s.cfg.BingXEnabled || !s.cfg.BingXWithdrawEnabled || !s.cfg.BingXTradeEnabled {
		return false, nil
	}
	_, ok := resolveLiquidityPair(s.cfg, buy.Asset, buy.Network)
	return ok, nil
}

// Fund executa o envio via BingX e atualiza o status da buy order no DB.
// Reutiliza a lógica existente de tryLiquidityExecution, isolada aqui para BTC.
func (s *BingXFundingSource) Fund(ctx context.Context, buy *database.BuyOrder) (bool, error) {
	// Delegar ao worker que já tem essa lógica completa
	// (evitar duplicação — o BuySendWorker expõe tryBTCViaLiquidityRouter)
	if s.router == nil {
		return false, errors.New("BingX router não configurado")
	}

	pair, ok := resolveLiquidityPair(s.cfg, buy.Asset, buy.Network)
	if !ok {
		return false, errors.New("par BTC:BITCOIN não resolvido pelo liquidity router")
	}

	timeout := time.Duration(s.cfg.LiquidityQuoteTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 2500 * time.Millisecond
	}
	routeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := liquidity.Request{
		OrderID:         buy.ID,
		Asset:           pair.Asset,
		Network:         pair.Network,
		TokenContract:   pair.ContractAddress,
		TokenDecimals:   pair.Decimals,
		FiatCurrency:    buy.FiatCurrency,
		AmountBRL:       buy.PayoutBRL,
		CryptoAmount:    buy.CryptoAmount,
		QuoteLockedRate: buy.RateLocked,
		DestAddress:     buy.DestAddress,
		CreatedAt:       buy.CreatedAt,
	}

	best, quotes, exec, execErr := s.router.ExecuteBest(routeCtx, req)

	// Gravar quotes para auditoria
	if len(quotes) > 0 {
		quoteRecords := make([]database.LiquidityQuoteRecord, 0, len(quotes))
		for _, q := range quotes {
			quoteRecords = append(quoteRecords, liquidityQuoteRecord(q, q.Provider == best.Provider))
		}
		quoteIDs, _ := s.db.RecordLiquidityQuotes(ctx, buy.ID, quoteRecords)
		if best.Provider != "" {
			execQuoteID := quoteIDs[best.Provider]
			_ = s.db.RecordLiquidityExecution(ctx, buy.ID, liquidityExecutionRecord(execQuoteID, best.Provider, "attempted", exec, execErr))
		}
	}

	if execErr != nil {
		return false, execErr
	}

	status := strings.ToLower(strings.TrimSpace(exec.Status))
	if status == "" {
		status = "submitted"
	}

	switch status {
	case "sent", "enviado", "delivered", "confirmed", "settled":
		txHash := firstNonEmptyWorker(exec.TxHash, exec.ExternalOrderID, "liquidity-accepted-"+buy.ID)
		if dbErr := s.db.UpdateBuyOrderStatus(ctx, buy.ID, "enviado", map[string]any{
			"txHashOut":      txHash,
			"provider":       exec.Provider,
			"funding_source": "bingx",
		}); dbErr != nil {
			return true, dbErr
		}
		return true, nil

	default:
		txHash := firstNonEmptyWorker(exec.TxHash, "liquidity-accepted-"+buy.ID)
		if dbErr := s.db.UpdateBuyOrderStatus(ctx, buy.ID, "pendente_confirmacao", map[string]any{
			"txHashOut":       txHash,
			"provider":        exec.Provider,
			"funding_source":  "bingx",
			"externalOrderId": exec.ExternalOrderID,
		}); dbErr != nil {
			return true, dbErr
		}
		return true, nil
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// btcToSats converte BTC float64 para satoshis int64 (1 BTC = 100_000_000 sats).
// Usa aritmética inteira para evitar problemas de precisão float.
func btcToSats(btc float64) int64 {
	if btc <= 0 {
		return 0
	}
	// Multiplicar por 1e8 e arredondar
	sats := int64(btc*1e8 + 0.5)
	return sats
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

