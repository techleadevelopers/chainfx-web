package workers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/liquidity"
	"payment-gateway/internal/security"
	"payment-gateway/internal/transactions"
)

type BuySendWorker struct {
	bus                 *EventBus
	db                  *database.DB
	cfg                 *config.Config
	client              *http.Client
	sem                 chan struct{}
	router              *liquidity.Router
	hotWalletHasBalance func(context.Context, *database.BuyOrder, liquidity.Pair) (bool, error)
}

func NewBuySendWorker(bus *EventBus, db *database.DB, cfg *config.Config) *BuySendWorker {
	client := httpclient.Default()
	return &BuySendWorker{
		bus:    bus,
		db:     db,
		cfg:    cfg,
		client: client,
		sem:    make(chan struct{}, 8),
		router: newBuyLiquidityRouter(cfg, client),
	}
}

func (bw *BuySendWorker) Start(ctx context.Context) {
	buyChan := bw.bus.Subscribe("buy.paid")
	slog.Info("BuySendWorker escutando eventos 'buy.paid'")
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Desligando BuySendWorker")
			return
		case <-ticker.C:
			bw.recoverPendingBuys(ctx)
		case event, ok := <-buyChan:
			if !ok {
				return
			}
			bw.dispatch(ctx, event)
		}
	}
}

func (bw *BuySendWorker) dispatch(ctx context.Context, event Event) {
	select {
	case bw.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	go func() {
		defer func() {
			<-bw.sem
			if r := recover(); r != nil {
				slog.Error("BuySendWorker: panic em processBuyOnchainSend", "recover", r, "order_id", event.OrderID)
			}
		}()
		bw.processBuyOnchainSend(event)
	}()
}

func (bw *BuySendWorker) recoverPendingBuys(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	buys, err := bw.db.ListPendingBuys(scanCtx)
	if err != nil {
		slog.Error("Erro ao varrer BUYs pendentes para recovery", "error", err)
		return
	}
	for _, buy := range buys {
		bw.dispatch(ctx, Event{Type: "buy.recovery", OrderID: buy.ID})
	}
	if len(buys) > 0 {
		slog.Info("Recovery BUY varreu ordens pagas pendentes", "count", len(buys))
	}
}

func (bw *BuySendWorker) processBuyOnchainSend(event Event) {
	start := time.Now()
	orderID := event.OrderID
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	// ── Atomic DB claim: replaces in-memory active map ────────────────────────
	// UPDATE ... WHERE status IN ('pago_fiat','pago_pix') RETURNING id ensures
	// only one worker (across goroutines AND replicas) processes each order.
	// The in-memory sync.Map is insufficient for multi-replica deployments.
	claimCtx, claimCancel := context.WithTimeout(ctx, 5*time.Second)
	claimed, err := bw.db.ClaimBuyOrderForSend(claimCtx, orderID)
	claimCancel()
	if err != nil {
		slog.Error("Erro ao tentar claim de buy order", "buy_order_id", orderID, "error", err)
		return
	}
	if !claimed {
		slog.Debug("BuySendWorker: buy order já processada por outro worker", "buy_order_id", orderID)
		return
	}

	buy, err := bw.db.GetBuyOrder(ctx, orderID)
	if err != nil {
		slog.Error("Erro ao buscar buy order", "buy_order_id", orderID, "error", err)
		return
	}
	if buy == nil {
		return
	}

	if bw.tryLiquidityExecution(ctx, buy) {
		slog.Info("BUY entregue por roteador de liquidez", "buy_order_id", orderID, "duration_ms", time.Since(start).Milliseconds())
		return
	}
	pair, ok := resolveLiquidityPair(bw.cfg, buy.Asset, buy.Network)
	if !ok || !bw.hotWalletDeliverySupported(pair) {
		errMsg := "par asset/network sem rota executavel: router indisponivel ou sem provider e hot wallet nao suporta o par"
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": errMsg, "asset": buy.Asset, "network": buy.Network})
		slog.Error("Envio BUY bloqueado: sem rota executavel", "buy_order_id", orderID, "asset", buy.Asset, "network", buy.Network)
		return
	}

	// Validação do signer
	if bw.cfg.SignerUrl == "" || bw.cfg.SignerHmacSecret == "" {
		if bw.cfg.AllowSimulations && !bw.cfg.IsProduction() {
			txHash := "buy-sim-" + orderID
			if err := bw.db.UpdateBuyOrderStatus(ctx, orderID, "enviado", map[string]any{"txHashOut": txHash}); err != nil {
				slog.Error("Erro ao persistir envio BUY simulado", "buy_order_id", orderID, "error", err)
				return
			}
			bw.bus.Publish(Event{Type: "buy.sent", OrderID: orderID, Payload: map[string]any{"txHash": txHash}})
			slog.Warn("Signer nao configurado; envio BUY simulado", "buy_order_id", orderID, "tx_hash", txHash)
			return
		}
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": "SIGNER_URL ou SIGNER_HMAC_SECRET nao configurado"})
		slog.Error("Envio BUY bloqueado: signer ausente", "buy_order_id", orderID)
		return
	}

	network := strings.ToUpper(strings.TrimSpace(bw.cfg.SignerNetwork))
	if network == "" || network == "EVM" || network == "BINANCE" || network == "BEP20" {
		network = "BSC"
	}
	if network != "BSC" {
		errMsg := "BUY settlement inicial permitido apenas em BSC"
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": errMsg})
		slog.Error("Envio BUY bloqueado por rede nao suportada nesta fase", "buy_order_id", orderID, "network", network)
		return
	}
	if strings.TrimSpace(bw.cfg.BscTreasuryContract) == "" || strings.TrimSpace(bw.cfg.BscUsdtContract) == "" {
		errMsg := "BSC_TREASURY_CONTRACT e BSC_USDT_CONTRACT obrigatorios para settlement"
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": errMsg})
		slog.Error("Envio BUY bloqueado: vault/token BSC ausente", "buy_order_id", orderID)
		return
	}

	amountRaw, err := transactions.TokenAmountRaw(buy.Asset, network, strconv.FormatFloat(buy.CryptoAmount, 'f', 8, 64))
	if err != nil {
		slog.Error("Erro ao converter amount BUY para raw", "buy_order_id", orderID, "error", err)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": "amount BUY invalido"})
		return
	}
	policy, err := transactions.DefaultBSCUSDTSettlementPolicy(
		bw.cfg.BscTreasuryContract,
		bw.cfg.BscUsdtContract,
		big.NewInt(0),
		big.NewInt(0),
	)
	if err != nil {
		slog.Error("Erro ao criar settlement policy BUY", "buy_order_id", orderID, "error", err)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": err.Error()})
		return
	}
	policy.MaxTransferRaw = new(big.Int).Set(amountRaw)
	policy.DailyLimitRaw = new(big.Int).Mul(amountRaw, big.NewInt(1000))
	validator, err := transactions.NewSettlementPolicyValidator([]transactions.SettlementPolicy{policy})
	if err != nil {
		slog.Error("Erro ao inicializar settlement policy validator", "buy_order_id", orderID, "error", err)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": err.Error()})
		return
	}
	instruction, err := validator.BuildInstruction(transactions.SettlementValidationInput{
		SettlementIntentID: "buy-" + buy.ID,
		OrderID:            buy.ID,
		Side:               transactions.SideBuy,
		Network:            network,
		ChainID:            uint64(transactions.ChainID(network)),
		Vault:              common.HexToAddress(bw.cfg.BscTreasuryContract),
		Token:              common.HexToAddress(bw.cfg.BscUsdtContract),
		Recipient:          common.HexToAddress(buy.DestAddress),
		AmountRaw:          amountRaw,
		SourceChannel:      transactions.SourceWorker,
		RiskDecision:       "APPROVED",
		IntentStatus:       transactions.StatusPaymentConfirmed,
		QuoteCreatedAt:     time.Now().Add(-1 * time.Minute),
		TreasuryBalanceRaw: amountRaw,
	})
	if err != nil {
		slog.Error("Settlement policy rejeitou BUY", "buy_order_id", orderID, "error", err)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": err.Error()})
		return
	}

	operationID := common.BytesToHash(instruction.OperationID[:]).Hex()
	payload := map[string]any{
		"operationId":        operationID,
		"settlementIntentId": instruction.SettlementIntentID,
		"orderId":            instruction.OrderID,
		"side":               instruction.Side,
		"network":            instruction.Network,
		"chainId":            instruction.ChainID,
		"vault":              instruction.Vault.Hex(),
		"token":              instruction.Token.Hex(),
		"recipient":          instruction.Recipient.Hex(),
		"amountRaw":          instruction.AmountRaw.String(),
		"sourceChannel":      instruction.SourceChannel,
		"riskDecision":       instruction.RiskDecision,
		"policyVersion":      instruction.PolicyVersion,
		"networkPolicy":      instruction.NetworkPolicy,
		"riskPolicy":         instruction.RiskPolicy,
		"contractVersion":    instruction.ContractVersion,
		"authorizedAt":       instruction.CreatedAt.Format(time.RFC3339Nano),
		"expiresAt":          instruction.ExpiresAt.Format(time.RFC3339Nano),
		"idempotencyKey":     "settlement-" + operationID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("Erro ao serializar payload", "buy_order_id", orderID, "error", err)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": "Erro ao serializar payload"})
		return
	}

	// Envia para signer
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bw.cfg.SignerUrl+"/settlements/execute", bytes.NewReader(body))
	if err != nil {
		slog.Error("Erro ao montar request para signer BUY", "buy_order_id", orderID, "error", err)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "erro", map[string]any{"error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, bw.cfg.SignerHmacSecret, body)

	resp, err := bw.client.Do(req)
	if err != nil {
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "pendente_confirmacao", map[string]any{"error": err.Error()})
		slog.Error("Signer BUY ambiguo; ordem marcada como pendente_confirmacao", "buy_order_id", orderID, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := fmt.Sprintf("signer status %d", resp.StatusCode)
		_ = bw.db.UpdateBuyOrderStatus(ctx, orderID, "pendente_confirmacao", map[string]any{"error": errMsg})
		slog.Error("Signer BUY retornou status ambiguo; ordem marcada como pendente_confirmacao", "buy_order_id", orderID, "status", resp.StatusCode)
		return
	}

	// Processa resposta
	var signed struct {
		TxHash string `json:"txHash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signed); err != nil {
		slog.Error("Erro ao decodificar resposta do signer", "buy_order_id", orderID, "error", err)
		signed.TxHash = "signer-accepted-" + orderID // fallback
	}

	if signed.TxHash == "" {
		signed.TxHash = "signer-accepted-" + orderID
	}

	if strings.HasPrefix(signed.TxHash, "signer-accepted-") {
		if err := bw.db.UpdateBuyOrderStatus(ctx, orderID, "pendente_confirmacao", map[string]any{"txHashOut": signed.TxHash}); err != nil {
			slog.Error("Erro ao atualizar BUY pendente_confirmacao", "buy_order_id", orderID, "error", err)
			return
		}
		bw.bus.Publish(Event{Type: "buy.pending_confirmation", OrderID: orderID, Payload: map[string]any{"txHash": signed.TxHash}})
		slog.Warn("Envio cripto BUY aceito sem txHash; aguardando confirmacao manual/signer", "buy_order_id", orderID, "duration_ms", time.Since(start).Milliseconds())
		return
	}

	if err := bw.db.UpdateBuyOrderStatus(ctx, orderID, "enviado", map[string]any{"txHashOut": signed.TxHash}); err != nil {
		slog.Error("Erro ao atualizar BUY enviado", "buy_order_id", orderID, "error", err)
		return
	}
	bw.bus.Publish(Event{Type: "buy.sent", OrderID: orderID, Payload: map[string]any{"txHash": signed.TxHash}})
	slog.Info("Envio cripto BUY processado", "buy_order_id", orderID, "duration_ms", time.Since(start).Milliseconds())
}
