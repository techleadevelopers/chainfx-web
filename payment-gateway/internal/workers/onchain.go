package workers

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"strings"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/models"
	rpcpool "payment-gateway/internal/rpc"
)

var erc20TransferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

const (
	// Fallback confirmation counts used when config values are zero.
	// BSC: finalistic consensus, 6 blocks ≈ 18 s — safe for most reorgs.
	// Polygon: bor consensus can produce deep reorgs; 128 blocks ≈ 5 min — conservative.
	defaultBSCConfirmations     = uint64(6)
	defaultPolygonConfirmations = uint64(128)
	scanBlockRange              = uint64(50)

	// Absolute safety floors — these cannot be overridden by environment variables.
	// Setting BSC_MIN_CONFIRMATIONS < 3 or POLYGON_MIN_CONFIRMATIONS < 64 will
	// be silently clamped to these values to prevent double-spend via reorgs.
	minSafeBSCConfirmations     = uint64(3)
	minSafePolygonConfirmations = uint64(64)
)

type onchainNetworkConfig struct {
	Name                  string
	RPCUrls               string
	TokenContract         string
	TokenDecimals         int
	RequiredConfirmations uint64
}

// OnchainWorker monitors configured EVM networks for stablecoin deposits to pending sell orders.
type OnchainWorker struct {
	bus      *EventBus
	db       *database.DB
	cfg      *config.Config
	pools    map[string]*rpcpool.Pool
	networks []onchainNetworkConfig
	dlq      *DeadLetterQueue
}

func NewOnchainWorker(bus *EventBus, db *database.DB, cfg *config.Config) *OnchainWorker {
	w := &OnchainWorker{
		bus:   bus,
		db:    db,
		cfg:   cfg,
		pools: make(map[string]*rpcpool.Pool),
		dlq:   NewPersistentDLQ(db, 1000),
	}

	bscConf := cfg.BSCMinConfirmations
	if bscConf == 0 {
		bscConf = defaultBSCConfirmations
	}
	// SECURITY: enforce absolute safety floors — prevents misconfiguration
	// (e.g. BSC_MIN_CONFIRMATIONS=1) from enabling double-spend via reorgs.
	if bscConf < minSafeBSCConfirmations {
		slog.Warn("OnchainWorker: BSC_MIN_CONFIRMATIONS abaixo do piso de seguranca, sobrescrevendo",
			"configured", bscConf, "floor", minSafeBSCConfirmations)
		bscConf = minSafeBSCConfirmations
	}

	polyConf := cfg.PolygonMinConfirmations
	if polyConf == 0 {
		polyConf = defaultPolygonConfirmations
	}
	if polyConf < minSafePolygonConfirmations {
		slog.Warn("OnchainWorker: POLYGON_MIN_CONFIRMATIONS abaixo do piso de seguranca, sobrescrevendo",
			"configured", polyConf, "floor", minSafePolygonConfirmations)
		polyConf = minSafePolygonConfirmations
	}

	// Register effective floors with the metrics package for Prometheus export.
	metrics.SetOnchainConfirmationFloor("BSC", bscConf)
	metrics.SetOnchainConfirmationFloor("POLYGON", polyConf)

	slog.Info("OnchainWorker: confirmacoes configuradas",
		"bsc_confirmations", bscConf,
		"polygon_confirmations", polyConf,
		"note", "piso minimo de seguranca: BSC>=3, Polygon>=64; aumentar se reorgs forem detectados")

	for _, network := range []onchainNetworkConfig{
		{Name: "BSC", RPCUrls: cfg.BscRpcUrls, TokenContract: cfg.BscUsdtContract, TokenDecimals: 18, RequiredConfirmations: bscConf},
		{Name: "POLYGON", RPCUrls: cfg.PolygonRpcUrls, TokenContract: cfg.PolygonUsdtContract, TokenDecimals: 6, RequiredConfirmations: polyConf},
	} {
		if strings.TrimSpace(network.RPCUrls) == "" || strings.TrimSpace(network.TokenContract) == "" {
			continue
		}
		pool, err := rpcpool.NewPool(network.RPCUrls)
		if err != nil {
			slog.Warn("OnchainWorker: RPC pool init failed", "network", network.Name, "err", err)
			continue
		}
		w.pools[network.Name] = pool
		w.networks = append(w.networks, network)
	}

	return w
}

func (ow *OnchainWorker) Start(ctx context.Context) {
	slog.Info("OnchainWorker iniciado", "networks", len(ow.networks))
	for network, pool := range ow.pools {
		slog.Info("OnchainWorker rede habilitada", "network", network)
		pool.StartHealthChecks(ctx, 30*time.Second)
	}

	mobilePayout := ow.bus.Subscribe("mobile.payout.requested")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("OnchainWorker: encerrando")
			return
		case ev, ok := <-mobilePayout:
			if !ok {
				return
			}
			go func(e Event) {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("OnchainWorker: panic em forwardMobilePayout", "recover", r)
					}
				}()
				ow.forwardMobilePayout(e)
			}(ev)
		case <-ticker.C:
			for _, network := range ow.networks {
				ow.poll(ctx, network)
			}
		}
	}
}

// m2mTreasuryAddress returns the normalised TREASURY_HOT address used for M2M deposits.
func (ow *OnchainWorker) m2mTreasuryAddress() string {
	return strings.ToLower(strings.TrimSpace(ow.cfg.TreasuryHot))
}

func (ow *OnchainWorker) poll(ctx context.Context, network onchainNetworkConfig) {
	pool := ow.pools[network.Name]
	if pool == nil || strings.TrimSpace(network.TokenContract) == "" {
		slog.Debug("OnchainWorker: rede nao configurada, pulando poll", "network", network.Name)
		return
	}

	pollCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	latestBlock, err := pool.BlockNumber(pollCtx)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao buscar bloco", "network", network.Name, "err", err)
		return
	}
	if latestBlock <= network.RequiredConfirmations {
		slog.Debug("OnchainWorker: aguardando blocos suficientes", "network", network.Name, "latest", latestBlock)
		return
	}

	scanHead := latestBlock - network.RequiredConfirmations
	fromBlock := ow.getCursor(pollCtx, network.Name, scanHead)
	if fromBlock >= scanHead {
		return
	}
	toBlock := min64(fromBlock+scanBlockRange, scanHead)
	if toBlock <= fromBlock {
		slog.Warn("OnchainWorker: faixa de blocos invalida, pulando poll",
			"network", network.Name, "from", fromBlock, "to", toBlock, "latest", latestBlock, "scan_head", scanHead)
		return
	}

	pendingOrders, err := ow.db.GetPendingOrdersByNetwork(pollCtx, network.Name)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao buscar ordens pendentes; cursor nao avancado", "network", network.Name, "err", err)
		return
	}

	// Build address→orders map from sell/swap orders. A shared hot wallet can
	// have multiple pending sell orders; matching must not fan out one tx to all.
	addrSet := make(map[string][]models.Order, len(pendingOrders))
	for _, o := range pendingOrders {
		if o.Address != "" {
			key := strings.ToLower(o.Address)
			addrSet[key] = append(addrSet[key], o)
		}
	}

	m2mAddresses, err := ow.db.ListPendingM2MDepositAddresses(pollCtx)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao buscar enderecos M2M pendentes; cursor nao avancado", "network", network.Name, "err", err)
		return
	}
	m2mAddrSet := make(map[string]struct{}, len(m2mAddresses))
	for _, address := range m2mAddresses {
		address = strings.ToLower(strings.TrimSpace(address))
		if address == "" || !common.IsHexAddress(address) {
			continue
		}
		m2mAddrSet[address] = struct{}{}
		if _, exists := addrSet[address]; !exists {
			addrSet[address] = nil
		}
	}

	if len(addrSet) == 0 {
		ow.saveCursor(pollCtx, network.Name, toBlock)
		return
	}

	query := ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(fromBlock + 1),
		ToBlock:   new(big.Int).SetUint64(toBlock),
		Addresses: []common.Address{common.HexToAddress(network.TokenContract)},
		Topics:    [][]common.Hash{{erc20TransferTopic}},
	}
	logs, err := pool.FilterLogs(pollCtx, query)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao filtrar logs; cursor nao avancado",
			"network", network.Name, "from", fromBlock, "to", toBlock, "err", err)
		return
	}

	slog.Debug("OnchainWorker: scan",
		"network", network.Name,
		"blocks", fmt.Sprintf("%d->%d", fromBlock+1, toBlock),
		"logs", len(logs), "orders", len(pendingOrders))

	safeToBlock := toBlock
	for _, log := range logs {
		if len(log.Topics) < 3 {
			continue
		}
		toAddr := strings.ToLower(common.HexToAddress(log.Topics[2].Hex()).Hex())
		orders, ok := addrSet[toAddr]
		if !ok {
			continue
		}
		if latestBlock < log.BlockNumber+network.RequiredConfirmations {
			if log.BlockNumber > 0 && log.BlockNumber-1 < safeToBlock {
				safeToBlock = log.BlockNumber - 1
			}
			slog.Debug("OnchainWorker: deposito aguardando confirmacoes",
				"network", network.Name, "tx", log.TxHash.Hex(), "block", log.BlockNumber,
				"conf_remaining", (log.BlockNumber+network.RequiredConfirmations)-latestBlock)
			continue
		}
		rawAmount := new(big.Int).SetBytes(log.Data)

		// ── M2M intent matching (transfers to TREASURY_HOT) ──────────────────
		if _, isM2MAddress := m2mAddrSet[toAddr]; isM2MAddress {
			go func(netCfg onchainNetworkConfig, addr, txH string, amt *big.Int, bn uint64) {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("OnchainWorker: panic em matchM2MDeposit", "recover", r)
					}
				}()
				ow.matchM2MDeposit(ctx, netCfg, addr, txH, amt, bn)
			}(network, toAddr, log.TxHash.Hex(), rawAmount, log.BlockNumber)
			continue
		}

		// ── Regular sell-order deposit ────────────────────────────────────────
		if len(orders) == 1 {
			ow.confirmDeposit(ctx, network, orders[0].ID, log.TxHash.Hex(), rawAmount, log.BlockNumber)
			continue
		}
		ow.matchSharedSellDeposit(ctx, network, orders, log.TxHash.Hex(), rawAmount, log.BlockNumber)
	}

	if safeToBlock > fromBlock {
		ow.saveCursor(pollCtx, network.Name, safeToBlock)
	}
}

// matchM2MDeposit tries to match an on-chain transfer to TREASURY_HOT against
// a pending M2M payment intent. It uses amount-proximity matching with the
// configured tolerance (M2M_DEPOSIT_TOLERANCE_PCT, default 0.5%).
//
// If exactly one intent matches (closest amount within tolerance), that intent
// is atomically claimed via ConfirmM2MDeposit and a bus event is published so
// the M2MSettlementWorker can act immediately.
func (ow *OnchainWorker) matchM2MDeposit(ctx context.Context, network onchainNetworkConfig, depositAddress, txHash string, rawAmount *big.Int, blockNum uint64) {
	matchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	divisor := new(big.Float).SetInt(
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(network.TokenDecimals)), nil),
	)
	depositUSDT, _ := new(big.Float).Quo(new(big.Float).SetInt(rawAmount), divisor).Float64()

	candidates, err := ow.db.FindPendingIntentsByDepositAddress(matchCtx, depositAddress, network.Name)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao buscar M2M intents", "tx", txHash, "err", err)
		return
	}
	if len(candidates) == 0 {
		slog.Debug("OnchainWorker: nenhuma M2M intent pendente para este deposito", "tx", txHash, "amount_usdt", depositUSDT)
		return
	}

	tol := ow.cfg.M2MDepositTolerancePct
	if tol <= 0 {
		tol = 0.005
	}
	chosen, matchErr := chooseM2MDepositCandidate(candidates, depositUSDT, tol)
	if matchErr != nil {
		slog.Warn("OnchainWorker: deposito M2M nao conciliado automaticamente",
			"payment_address", depositAddress,
			"tx", txHash,
			"deposit_usdt", depositUSDT,
			"candidates", len(candidates),
			"reason", matchErr.Error())
		return
	}
	claimed, err := ow.db.ConfirmM2MDeposit(matchCtx, chosen.IntentID, txHash, depositUSDT)
	if err != nil {
		slog.Error("OnchainWorker: erro ao confirmar M2M deposito",
			"intent_id", chosen.IntentID, "tx", txHash, "err", err)
		return
	}
	if !claimed {
		slog.Debug("OnchainWorker: M2M intent ja confirmada por outro worker",
			"intent_id", chosen.IntentID, "tx", txHash)
		return
	}

	// Detect overpayment: when the agent deposited more than the required amount.
	// The excess stays in TREASURY_HOT and requires manual reconciliation or refund.
	overpaymentUSDT := depositUSDT - chosen.RequiredUSDT
	if overpaymentUSDT > 0.001 { // threshold: ignore sub-cent dust differences
		slog.Warn("OnchainWorker: OVERPAYMENT detectado — excesso em TREASURY_HOT, reconciliacao manual necessaria",
			"intent_id", chosen.IntentID,
			"deposit_usdt", depositUSDT,
			"required_usdt", chosen.RequiredUSDT,
			"overpayment_usdt", overpaymentUSDT,
			"tx", txHash,
			"network", network.Name,
			"action_required", "verificar e restituir ou registrar saldo excedente")
		// Emit Prometheus counter — triggers alert if chainfx_m2m_overpayment_total > 0
		metrics.IncOverpayment(chosen.IntentID, overpaymentUSDT)
		ow.bus.Publish(Event{
			Type:    "m2m.overpayment.detected",
			OrderID: chosen.IntentID,
			Payload: map[string]any{
				"intent_id":        chosen.IntentID,
				"deposit_usdt":     depositUSDT,
				"required_usdt":    chosen.RequiredUSDT,
				"overpayment_usdt": overpaymentUSDT,
				"deposit_tx":       txHash,
				"network":          network.Name,
				"agent_wallet":     chosen.AgentWallet,
				"action_required":  "reconciliation: refund or credit excess to agent",
			},
		})
	}

	slog.Info("OnchainWorker: M2M deposito confirmado",
		"intent_id", chosen.IntentID,
		"tx", txHash,
		"deposit_usdt", depositUSDT,
		"required_usdt", chosen.RequiredUSDT,
		"overpayment_usdt", overpaymentUSDT,
		"block", blockNum,
		"network", network.Name)

	// Signal the M2MSettlementWorker immediately (OrderID field reused as intentID).
	ow.bus.Publish(Event{
		Type:    "m2m.deposit.confirmed",
		OrderID: chosen.IntentID,
		Payload: map[string]any{
			"deposit_tx":       txHash,
			"deposit_usdt":     depositUSDT,
			"required_usdt":    chosen.RequiredUSDT,
			"overpayment_usdt": overpaymentUSDT,
			"block":            blockNum,
			"network":          network.Name,
		},
	})
}

func chooseM2MDepositCandidate(candidates []database.M2MDepositMatch, depositUSDT, tol float64) (database.M2MDepositMatch, error) {
	if len(candidates) == 0 {
		return database.M2MDepositMatch{}, fmt.Errorf("nenhuma intent pendente")
	}
	if tol <= 0 {
		tol = 0.005
	}
	if len(candidates) == 1 {
		chosen := candidates[0]
		if chosen.RequiredUSDT > 0 {
			diff := (depositUSDT - chosen.RequiredUSDT) / chosen.RequiredUSDT
			if diff < -tol {
				return database.M2MDepositMatch{}, fmt.Errorf("deposito abaixo da tolerancia para %s", chosen.IntentID)
			}
		}
		return chosen, nil
	}

	bestIndex := -1
	bestDistance := math.MaxFloat64
	secondDistance := math.MaxFloat64
	for i, candidate := range candidates {
		if candidate.RequiredUSDT <= 0 {
			continue
		}
		diff := (depositUSDT - candidate.RequiredUSDT) / candidate.RequiredUSDT
		if diff < -tol {
			continue
		}
		distance := math.Abs(diff)
		if distance < bestDistance {
			secondDistance = bestDistance
			bestDistance = distance
			bestIndex = i
			continue
		}
		if distance < secondDistance {
			secondDistance = distance
		}
	}
	if bestIndex < 0 {
		return database.M2MDepositMatch{}, fmt.Errorf("nenhuma intent compativel com valor depositado")
	}
	if bestDistance > tol {
		return database.M2MDepositMatch{}, fmt.Errorf("multiplas intents no endereco e deposito acima da tolerancia exata; reconciliacao manual")
	}
	if math.Abs(secondDistance-bestDistance) <= 0.000001 {
		return database.M2MDepositMatch{}, fmt.Errorf("valor depositado ambiguo entre multiplas intents")
	}
	return candidates[bestIndex], nil
}

func tokenAmountFloat(rawAmount *big.Int, decimals int) float64 {
	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	amount, _ := new(big.Float).Quo(new(big.Float).SetInt(rawAmount), divisor).Float64()
	return amount
}

func (ow *OnchainWorker) depositTolerance() float64 {
	tol := ow.cfg.BscDepositTolerancePct
	if tol <= 0 {
		tol = 0.02
	}
	return tol
}

func (ow *OnchainWorker) matchSharedSellDeposit(ctx context.Context, network onchainNetworkConfig, orders []models.Order, txHash string, rawAmount *big.Int, blockNum uint64) {
	amountFloat := tokenAmountFloat(rawAmount, network.TokenDecimals)
	tol := ow.depositTolerance()

	type candidate struct {
		order   models.Order
		diffAbs float64
	}
	var candidates []candidate
	for _, order := range orders {
		if order.Status != models.StatusAguardandoDeposito &&
			order.Status != models.StatusAguardandoValidacao {
			continue
		}
		if !strings.EqualFold(order.Network, network.Name) || order.AmountUSDT <= 0 {
			continue
		}
		relativeDiff := (amountFloat - order.AmountUSDT) / order.AmountUSDT
		if math.Abs(relativeDiff) <= tol {
			candidates = append(candidates, candidate{order: order, diffAbs: math.Abs(relativeDiff)})
		}
	}

	if len(candidates) == 1 {
		ow.confirmDeposit(ctx, network, candidates[0].order.ID, txHash, rawAmount, blockNum)
		return
	}

	eventType := "order.deposit_unmatched"
	message := "deposito em carteira compartilhada sem ordem compativel; suporte deve reconciliar"
	incidentOrders := orders
	if len(candidates) > 1 {
		eventType = "order.deposit_ambiguous"
		message = "deposito em carteira compartilhada compativel com multiplas ordens; liquidacao automatica bloqueada"
		incidentOrders = make([]models.Order, 0, len(candidates))
		for _, c := range candidates {
			incidentOrders = append(incidentOrders, c.order)
		}
	}

	slog.Warn("OnchainWorker: sell deposit requires manual reconciliation",
		"network", network.Name, "tx", txHash, "amount_usdt", amountFloat,
		"candidate_count", len(candidates), "pending_orders", len(orders), "reason", message)

	for _, order := range incidentOrders {
		_ = ow.db.AddEvent(ctx, order.ID, "order.incident", map[string]any{
			"type":          eventType,
			"reason":        message,
			"depositTx":     txHash,
			"depositAmount": amountFloat,
			"block":         blockNum,
			"network":       network.Name,
		})
	}
}

func (ow *OnchainWorker) confirmDeposit(ctx context.Context, network onchainNetworkConfig, orderID, txHash string, rawAmount *big.Int, blockNum uint64) {
	cCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	order, err := ow.db.GetOrder(cCtx, orderID)
	if err != nil || order == nil {
		slog.Warn("OnchainWorker: ordem nao encontrada", "network", network.Name, "order_id", orderID)
		return
	}
	if order.Status != models.StatusAguardandoDeposito &&
		order.Status != models.StatusAguardandoValidacao {
		return
	}
	if !strings.EqualFold(order.Network, network.Name) {
		return
	}
	if order.DepositTx != nil && *order.DepositTx == txHash {
		slog.Debug("OnchainWorker: deposito ja registrado, ignorando", "network", network.Name, "tx", txHash)
		return
	}
	usedElsewhere, err := ow.db.HasDepositTxForOtherOrder(cCtx, orderID, txHash)
	if err != nil {
		slog.Warn("OnchainWorker: erro ao verificar replay de tx", "network", network.Name, "order_id", orderID, "tx", txHash, "err", err)
		return
	}
	if usedElsewhere {
		reason := "deposit tx ja vinculada a outra ordem"
		slog.Error("OnchainWorker: replay de deposito bloqueado", "network", network.Name, "order_id", orderID, "tx", txHash)
		_ = ow.db.UpdateOrderStatus(cCtx, orderID, string(models.StatusIncidenteValidacao), map[string]any{
			"depositTx": txHash, "block": blockNum, "network": network.Name, "error": reason,
		})
		_ = ow.db.OpenOrderIncident(cCtx, orderID, "sell_deposit_tx_replay", "critical", reason, map[string]any{
			"depositTx": txHash, "block": blockNum, "network": network.Name,
		})
		return
	}

	amountFloat := tokenAmountFloat(rawAmount, network.TokenDecimals)
	tol := ow.depositTolerance()
	if order.AmountUSDT > 0 {
		diff := (amountFloat - order.AmountUSDT) / order.AmountUSDT
		if diff < -tol {
			slog.Warn("OnchainWorker: deposito abaixo da tolerancia",
				"network", network.Name, "order_id", orderID, "expected", order.AmountUSDT,
				"received", amountFloat, "diff_pct", fmt.Sprintf("%.2f%%", diff*100))
			_ = ow.db.UpdateOrderStatus(cCtx, orderID, "aguardando_validacao", map[string]any{
				"depositTx": txHash, "depositAmount": amountFloat, "block": blockNum, "network": network.Name,
			})
			return
		}
	}

	slog.Info("OnchainWorker: deposito confirmado",
		"network", network.Name, "order_id", orderID, "tx", txHash, "amount_usdt", amountFloat, "block", blockNum)

	if err := ow.db.UpdateOrderStatus(cCtx, orderID, "pago", map[string]any{
		"depositTx": txHash, "depositAmount": amountFloat, "block": blockNum, "network": network.Name,
	}); err != nil {
		slog.Error("OnchainWorker: erro ao atualizar status", "network", network.Name, "order_id", orderID, "err", err)
		return
	}

	ow.bus.Publish(Event{
		Type:    "payout.requested",
		OrderID: orderID,
		Payload: map[string]any{"txHash": txHash, "depositAmount": amountFloat, "blockNumber": blockNum, "network": network.Name},
	})
}

func (ow *OnchainWorker) getCursor(ctx context.Context, network string, latestBlock uint64) uint64 {
	block, found, err := ow.db.GetCursor(ctx, network)
	if err != nil || !found || block == 0 {
		return subtractFloor(latestBlock, 1000)
	}

	const maxLookback uint64 = 50000
	if latestBlock > uint64(block) && latestBlock-uint64(block) > maxLookback {
		slog.Warn("cursor on-chain muito antigo, resetando",
			"network", network, "old_cursor", block, "latest", latestBlock)
		return subtractFloor(latestBlock, 1000)
	}

	return uint64(block)
}

func (ow *OnchainWorker) saveCursor(ctx context.Context, network string, block uint64) {
	if err := ow.db.SaveCursor(ctx, network, int64(block)); err != nil {
		slog.Warn("OnchainWorker: erro ao salvar cursor", "network", network, "block", block, "err", err)
	}
}

func (ow *OnchainWorker) forwardMobilePayout(ev Event) {
	slog.Info("OnchainWorker: encaminhando mobile payout", "order_id", ev.OrderID)
	ow.bus.Publish(Event{
		Type:    "payout.requested",
		OrderID: ev.OrderID,
		Payload: ev.Payload,
	})
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func subtractFloor(n, delta uint64) uint64 {
	if n <= delta {
		return 0
	}
	return n - delta
}
