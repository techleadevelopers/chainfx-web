package bitcoin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// TreasuryService é o ponto de entrada para operações da BTC Treasury operacional.
//
// Responsabilidades:
//   - Sincronizar UTXOs do endereço da Treasury com o provider
//   - Calcular saldo utilizável (confirmed - reserved - fee - min_reserve)
//   - Executar envios idempotentes respeitando modelo UTXO
//   - Garantir invariante: uma ordem BUY BTC ≤ uma tentativa de broadcast
//
// A Treasury usa a infraestrutura BTC existente (Provider, SelectUTXOs, BuildAndSignTx)
// sem criar um segundo stack BTC. Usa tabelas separadas (btc_treasury_utxos,
// btc_treasury_operations) para isolamento completo das wallets dos usuários.
type TreasuryService struct {
	cfg      *Config         // configuração BTC (rede, fee, limits, etc.)
	tcfg     *TreasuryConfig // configuração específica da treasury
	provider Provider        // mempool.space — reutilizado do bitcoin.Service
	signer   TreasurySigner  // AESGCMTreasurySigner ou outro impl
	repo     treasuryStore
	rawDB    *sql.DB // para advisory lock
}

// NewTreasuryService cria o TreasuryService.
// Retorna (nil, nil) se BTC_TREASURY_ENABLED=false.
// Retorna erro se a configuração for inválida (ex: chave indecifrável).
func NewTreasuryService(btcCfg *Config, db *sql.DB) (*TreasuryService, error) {
	tcfg, err := LoadTreasuryConfig()
	if err != nil {
		return nil, err
	}
	if tcfg == nil {
		return nil, nil // Treasury desabilitada — não é erro
	}

	// Validar endereço da treasury na rede configurada
	if err := ValidateAddress(tcfg.Address, btcCfg.HRP()); err != nil {
		return nil, fmt.Errorf("bitcoin/treasury: BTC_TREASURY_ADDRESS inválido para rede %s: %w", btcCfg.Network, err)
	}

	signer, err := NewAESGCMTreasurySigner(tcfg, btcCfg)
	if err != nil {
		return nil, fmt.Errorf("bitcoin/treasury: falha ao inicializar signer: %w", err)
	}

	slog.Info("btc/treasury: Treasury operacional inicializada",
		"address", tcfg.Address,
		"signer_key_id", tcfg.SignerKeyID,
		"min_reserve_sats", tcfg.MinReserveSats,
		"network", btcCfg.Network,
	)

	return &TreasuryService{
		cfg:      btcCfg,
		tcfg:     tcfg,
		provider: NewMempoolProvider(btcCfg),
		signer:   signer,
		repo:     &treasuryRepository{sql: db},
		rawDB:    db,
	}, nil
}

// Config retorna a configuração BTC da Treasury.
func (ts *TreasuryService) Config() *Config { return ts.cfg }

// TreasuryConfig retorna a configuração específica da Treasury.
func (ts *TreasuryService) TreasuryConfig() *TreasuryConfig { return ts.tcfg }

// ─── Saldo ────────────────────────────────────────────────────────────────────

// AvailableBalance consulta o provider diretamente e calcula o saldo utilizável.
// spendable = confirmed - reserved - estimated_fee - min_reserve
//
// Nota: consulta ao provider é feita ao vivo para garantir precisão no momento
// da decisão de roteamento. Sem cache — a treasury é um ativo crítico.
func (ts *TreasuryService) AvailableBalance(ctx context.Context, amountSats int64) (TreasuryBalance, error) {
	network := string(ts.cfg.Network)
	address := ts.tcfg.Address

	// 1. Sincronizar UTXOs com o provider
	if err := ts.syncUTXOs(ctx); err != nil {
		slog.Warn("btc/treasury: erro ao sincronizar UTXOs; usando DB como fallback",
			"address", address, "error", err)
	}

	// 2. Buscar UTXOs confirmados do DB (já sincronizados)
	confirmed, err := ts.repo.GetTreasuryConfirmedUTXOs(ctx, network, address)
	if err != nil {
		return TreasuryBalance{}, fmt.Errorf("btc/treasury: erro ao buscar UTXOs confirmados: %w", err)
	}

	var confirmedSats int64
	for _, u := range confirmed {
		confirmedSats += u.ValueSats
	}

	// 3. Estimar fee (conservadora: 1 input, 2 outputs; será refinada no Send)
	feeRate, err := ts.provider.EstimateFeeRate(ctx, ts.cfg.FeeTargetBlocks)
	if err != nil {
		feeRate = ts.cfg.MinFeeRateSatVB
	}
	if feeRate < ts.cfg.MinFeeRateSatVB {
		feeRate = ts.cfg.MinFeeRateSatVB
	}
	if feeRate > ts.cfg.MaxFeeRateSatVB {
		feeRate = ts.cfg.MaxFeeRateSatVB
	}
	estimatedFee := feeRate * int64(EstimateVSize(1, 2))

	// 4. Calcular spendable
	spendable := confirmedSats - estimatedFee - ts.tcfg.MinReserveSats
	if spendable < 0 {
		spendable = 0
	}

	return TreasuryBalance{
		ConfirmedSats:  confirmedSats,
		MinReserveSats: ts.tcfg.MinReserveSats,
		EstimatedFee:   estimatedFee,
		SpendableSats:  spendable,
	}, nil
}

// ─── Envio ────────────────────────────────────────────────────────────────────

// SendBTC executa um envio BTC idempotente a partir da Treasury.
//
// Fluxo:
//  1. Adquirir advisory lock (serializa concorrência multi-instância)
//  2. Sincronizar UTXOs
//  3. Verificar saldo utilizável
//  4. Claim idempotente em btc_treasury_operations
//  5. Se já processado → retornar resultado anterior
//  6. Selecionar UTXOs (greedy)
//  7. Reservar UTXOs atomicamente (ErrDoubleSpend se race condition)
//  8. Assinar tx
//  9. Persistir tx assinada (status = signed)
//
// 10. Broadcast
// 11. Handle broadcast_unknown (NUNCA fallback BingX a partir daqui)
// 12. Marcar UTXOs como spent
//
// Garantia crítica: uma ordem não pode gerar dois envios.
// Invariante: depois de signed/broadcast/broadcast_unknown → NÃO tentar BingX.
func (ts *TreasuryService) SendBTC(ctx context.Context, req TreasurySendRequest) (TreasurySendResult, error) {
	network := string(ts.cfg.Network)
	address := ts.tcfg.Address
	idempKey := "btc_buy:" + req.OrderID

	// ── 0. Validações sem efeito econômico ───────────────────────────────────
	if req.AmountSats <= 0 {
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: amountSats deve ser > 0")
	}
	if req.AmountSats < ts.cfg.DustLimitSats {
		return TreasurySendResult{}, ErrDustOutput
	}
	if ts.cfg.MaxSendSats > 0 && req.AmountSats > ts.cfg.MaxSendSats {
		return TreasurySendResult{}, ErrMaxSendExceeded
	}
	if ts.cfg.EmergencyLockdown {
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: BTC_EMERGENCY_LOCKDOWN ativo — envio bloqueado")
	}
	if !ts.cfg.WithdrawalsEnabled {
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: BTC_WITHDRAWALS_ENABLED=false — envio bloqueado")
	}
	if err := ValidateAddress(req.ToAddress, ts.cfg.HRP()); err != nil {
		return TreasurySendResult{}, fmt.Errorf("%w: %s", ErrInvalidAddress, req.ToAddress)
	}

	// ── 1. Advisory lock (multi-instância) ───────────────────────────────────
	lockCtx, lockCancel := context.WithTimeout(ctx, 5*time.Second)
	acquired, lockErr := AcquireTreasuryAdvisoryLock(lockCtx, ts.rawDB, address, network)
	lockCancel()
	if lockErr != nil {
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: erro ao adquirir lock: %w", lockErr)
	}
	if !acquired {
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: outra operação em andamento (lock não adquirido)")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = ReleaseTreasuryAdvisoryLock(releaseCtx, ts.rawDB, address, network)
	}()

	// ── 2. Sincronizar UTXOs ──────────────────────────────────────────────────
	if err := ts.syncUTXOs(ctx); err != nil {
		slog.Warn("btc/treasury: sync de UTXOs falhou; prosseguindo com DB",
			"order_id", req.OrderID, "error", err)
	}

	// ── 3. Verificar saldo ────────────────────────────────────────────────────
	feeRate := req.FeeRateSatVB
	if feeRate <= 0 {
		var err error
		feeRate, err = ts.provider.EstimateFeeRate(ctx, ts.cfg.FeeTargetBlocks)
		if err != nil {
			feeRate = ts.cfg.MinFeeRateSatVB
		}
	}
	if feeRate < ts.cfg.MinFeeRateSatVB {
		feeRate = ts.cfg.MinFeeRateSatVB
	}
	if feeRate > ts.cfg.MaxFeeRateSatVB {
		return TreasurySendResult{}, ErrFeeTooHigh
	}

	confirmedUTXOs, err := ts.repo.GetTreasuryConfirmedUTXOs(ctx, network, address)
	if err != nil {
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: erro ao buscar UTXOs: %w", err)
	}

	var confirmedSats int64
	for _, u := range confirmedUTXOs {
		confirmedSats += u.ValueSats
	}

	// Estimativa conservadora de fee (será refinada após seleção de UTXOs)
	estimatedFee := feeRate * int64(EstimateVSize(1, 2))
	spendable := confirmedSats - estimatedFee - ts.tcfg.MinReserveSats
	if spendable < req.AmountSats {
		return TreasurySendResult{}, fmt.Errorf("%w: spendable=%d sats, necessário=%d sats (min_reserve=%d, estimated_fee=%d)",
			ErrInsufficientFunds, spendable, req.AmountSats, ts.tcfg.MinReserveSats, estimatedFee)
	}

	// ── 4. Claim idempotente ──────────────────────────────────────────────────
	opID := uuid.New().String()
	op := TreasuryOperation{
		ID:                 opID,
		OrderID:            req.OrderID,
		Asset:              "BTC",
		Network:            "BITCOIN",
		FundingSource:      "treasury",
		TreasuryAddress:    address,
		DestinationAddress: req.ToAddress,
		AmountSats:         req.AmountSats,
		SignerOperationID:  ts.signer.KeyID(),
		IdempotencyKey:     idempKey,
	}
	existingOp, created, err := ts.repo.ClaimTreasuryOperation(ctx, op)
	if err != nil {
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: erro ao criar operação idempotente: %w", err)
	}
	if !created && existingOp != nil {
		// Operação já existe — retornar resultado anterior
		slog.Info("btc/treasury: operação idempotente — retornando resultado existente",
			"order_id", req.OrderID, "status", existingOp.Status, "txid", existingOp.Txid)
		return TreasurySendResult{
			TxID:          existingOp.Txid,
			FeeSats:       existingOp.FeeSats,
			AmountSats:    existingOp.AmountSats,
			Status:        existingOp.Status,
			FundingSource: "treasury",
			SignerKeyID:   ts.signer.KeyID(),
		}, nil
	}

	// A partir daqui: operação criada — qualquer falha ANTES do broadcast libera UTXOs.
	// Falha DEPOIS do broadcast (inclusive broadcast_unknown) → manual_review, NÃO fallback.

	if err := ts.repo.UpdateTreasuryOperationProcessing(ctx, opID); err != nil {
		slog.Warn("btc/treasury: erro ao marcar operação como processing", "op_id", opID, "error", err)
	}

	// ── 5. Selecionar UTXOs ───────────────────────────────────────────────────
	utxos := treasuryUTXOsToUTXOs(confirmedUTXOs)
	selected, changeSats, feeSats, err := SelectUTXOs(utxos, req.AmountSats, feeRate, ts.cfg.DustLimitSats)
	if err != nil {
		_ = ts.repo.UpdateTreasuryOperationFailed(ctx, opID, "COIN_SELECT_FAILED", err.Error(), TxStatusFailedBeforeSign)
		return TreasurySendResult{}, err
	}

	// ── 6. Construir outputs ──────────────────────────────────────────────────
	destScript, err := ScriptFromAddress(req.ToAddress, ts.cfg.HRP())
	if err != nil {
		_ = ts.repo.UpdateTreasuryOperationFailed(ctx, opID, "DEST_ADDRESS_INVALID", err.Error(), TxStatusFailedBeforeSign)
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: destino inválido: %w", err)
	}

	txOutputs := []TxOutput{{ValueSats: req.AmountSats, ScriptPubKey: destScript}}
	if changeSats > 0 {
		// Troco retorna para o mesmo endereço da Treasury (endereço fixo operacional)
		changeScript, err := ScriptFromAddress(address, ts.cfg.HRP())
		if err != nil {
			_ = ts.repo.UpdateTreasuryOperationFailed(ctx, opID, "CHANGE_ADDRESS_INVALID", err.Error(), TxStatusFailedBeforeSign)
			return TreasurySendResult{}, fmt.Errorf("btc/treasury: erro ao gerar script de troco: %w", err)
		}
		txOutputs = append(txOutputs, TxOutput{ValueSats: changeSats, ScriptPubKey: changeScript})
	}

	// ── 7. Reservar UTXOs atomicamente ────────────────────────────────────────
	selectedIDs := utxoIDsFromTreasury(selectedTreasuryUTXOs(confirmedUTXOs, selected))
	if err := ts.repo.ReserveTreasuryUTXOs(ctx, opID, selectedIDs); err != nil {
		_ = ts.repo.UpdateTreasuryOperationFailed(ctx, opID, "UTXO_RESERVATION_FAILED", err.Error(), TxStatusFailedBeforeSign)
		if errors.Is(err, ErrDoubleSpend) {
			return TreasurySendResult{}, ErrDoubleSpend
		}
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: erro ao reservar UTXOs: %w", err)
	}

	// ── 8. Construir inputs para assinatura ───────────────────────────────────
	txInputs := make([]TxInput, len(selected))
	for i, u := range selected {
		txInputs[i] = TxInput{
			Txid:         u.Txid,
			Vout:         u.Vout,
			ValueSats:    u.ValueSats,
			ScriptPubKey: u.ScriptPubKey,
			// PrivKeyBytes e PubKeyBytes serão preenchidos pelo signer
		}
	}

	// ── 9. Assinar ────────────────────────────────────────────────────────────
	rawHex, txid, err := ts.signer.Sign(ctx, txInputs, txOutputs)
	if err != nil {
		_ = ts.repo.ReleaseTreasuryUTXOs(ctx, opID)
		_ = ts.repo.UpdateTreasuryOperationFailed(ctx, opID, "SIGN_FAILED", err.Error(), TxStatusFailedBeforeSign)
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: erro ao assinar: %w", err)
	}

	// ── 10. Persistir tx assinada (ANTES do broadcast) ───────────────────────
	if err := ts.repo.UpdateTreasuryOperationSigned(ctx, opID, txid, rawHex, feeSats); err != nil {
		_ = ts.repo.ReleaseTreasuryUTXOs(ctx, opID)
		_ = ts.repo.UpdateTreasuryOperationFailed(ctx, opID, "SIGNED_PERSIST_FAILED", err.Error(), TxStatusFailedBeforeBroadcast)
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: erro ao persistir tx assinada: %w", err)
	}

	// ── 11. Broadcast ─────────────────────────────────────────────────────────
	slog.Info("btc/treasury: broadcasting",
		"order_id", req.OrderID, "txid", txid,
		"amount_sats", req.AmountSats, "fee_sats", feeSats,
		"signer_key_id", ts.signer.KeyID())

	broadcastedTxid, broadcastErr := ts.provider.BroadcastTransaction(ctx, rawHex)
	if broadcastErr != nil {
		if errors.Is(broadcastErr, ErrBroadcastUnknown) {
			// Resultado incerto — NÃO liberar UTXOs, NÃO tentar BingX.
			// O sistema de reconciliação resolverá.
			_ = ts.repo.UpdateTreasuryOperationBroadcast(ctx, opID, TxStatusBroadcastUnknown)
			slog.Warn("btc/treasury: broadcast incerto — aguardando reconciliação",
				"order_id", req.OrderID, "txid", txid, "op_id", opID)
			return TreasurySendResult{
				TxID:          txid,
				FeeSats:       feeSats,
				AmountSats:    req.AmountSats,
				Status:        TxStatusBroadcastUnknown,
				FundingSource: "treasury",
				SignerKeyID:   ts.signer.KeyID(),
			}, nil
		}
		if isAlreadyKnownBroadcastError(broadcastErr) {
			// Tx já está no mempool — tratar como broadcast bem-sucedido
			_ = ts.repo.UpdateTreasuryOperationBroadcast(ctx, opID, TxStatusBroadcast)
			_ = ts.repo.MarkTreasuryUTXOsSpent(ctx, txid, selectedIDs)
			slog.Info("btc/treasury: tx já conhecida pelo provider",
				"order_id", req.OrderID, "txid", txid)
			return TreasurySendResult{
				TxID:          txid,
				FeeSats:       feeSats,
				AmountSats:    req.AmountSats,
				Status:        TxStatusBroadcast,
				FundingSource: "treasury",
				SignerKeyID:   ts.signer.KeyID(),
			}, nil
		}
		// Falha definitiva no broadcast — liberar UTXOs, marcar como failed
		_ = ts.repo.ReleaseTreasuryUTXOs(ctx, opID)
		_ = ts.repo.UpdateTreasuryOperationFailed(ctx, opID, "BROADCAST_FAILED", broadcastErr.Error(), TxStatusFailedBeforeBroadcast)
		return TreasurySendResult{}, fmt.Errorf("btc/treasury: broadcast falhou: %w", broadcastErr)
	}

	finalTxid := broadcastedTxid
	if finalTxid == "" {
		finalTxid = txid
	}

	// ── 12. Marcar UTXOs como spent ───────────────────────────────────────────
	_ = ts.repo.UpdateTreasuryOperationBroadcast(ctx, opID, TxStatusBroadcast)
	_ = ts.repo.MarkTreasuryUTXOsSpent(ctx, finalTxid, selectedIDs)

	slog.Info("btc/treasury: envio bem-sucedido",
		"order_id", req.OrderID,
		"txid", finalTxid,
		"amount_sats", req.AmountSats,
		"fee_sats", feeSats,
		"signer_key_id", ts.signer.KeyID(),
	)

	return TreasurySendResult{
		TxID:          finalTxid,
		FeeSats:       feeSats,
		AmountSats:    req.AmountSats,
		Status:        TxStatusBroadcast,
		FundingSource: "treasury",
		SignerKeyID:   ts.signer.KeyID(),
	}, nil
}

// ─── Sync interno ─────────────────────────────────────────────────────────────

// syncUTXOs consulta o provider e persiste/atualiza UTXOs da Treasury no DB.
// Detecta reorgs (UTXOs que sumiram → orphaned).
func (ts *TreasuryService) syncUTXOs(ctx context.Context) error {
	network := string(ts.cfg.Network)
	address := ts.tcfg.Address
	hrp := ts.cfg.HRP()
	scriptHex := scriptHexForAddress(address, hrp)

	// Buscar UTXOs do provider
	providerUTXOs, err := ts.provider.GetAddressUTXOs(ctx, address)
	if err != nil {
		return fmt.Errorf("btc/treasury: erro ao buscar UTXOs do provider: %w", err)
	}

	blockHeight, _ := ts.provider.GetCurrentBlockHeight(ctx)

	// Upsert no DB
	providerKeys := make(map[string]bool, len(providerUTXOs))
	for _, pu := range providerUTXOs {
		key := pu.Txid + ":" + strconv.Itoa(int(pu.Vout))
		providerKeys[key] = true
		u := newTreasuryUTXOFromProvider(pu, address, network, scriptHex, blockHeight, ts.cfg.MinConfirmations)
		if err := ts.repo.UpsertTreasuryUTXO(ctx, u, ts.cfg.MinConfirmations); err != nil {
			slog.Warn("btc/treasury: erro ao upsert UTXO", "txid", pu.Txid, "error", err)
		}
	}

	// Detectar reorgs: UTXOs no DB que não estão mais no provider
	active, err := ts.repo.GetTreasuryActiveTreasuryUTXOs(ctx, network, address)
	if err != nil {
		return nil // erro não-crítico aqui
	}
	for _, u := range active {
		key := u.Txid + ":" + strconv.Itoa(int(u.Vout))
		if !providerKeys[key] && u.Status != UTXOStatusReserved {
			slog.Warn("btc/treasury: UTXO desapareceu do provider (possível reorg)",
				"txid", u.Txid, "vout", u.Vout)
			_ = ts.repo.MarkTreasuryUTXOOrphaned(ctx, u.ID)
		}
	}

	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// selectedTreasuryUTXOs retorna os TreasuryUTXOs que correspondem aos UTXO selecionados por SelectUTXOs.
// Usa txid:vout como chave de correspondência.
func selectedTreasuryUTXOs(all []TreasuryUTXO, selected []UTXO) []TreasuryUTXO {
	index := make(map[string]TreasuryUTXO, len(all))
	for _, u := range all {
		index[u.Txid+":"+strconv.Itoa(int(u.Vout))] = u
	}
	out := make([]TreasuryUTXO, 0, len(selected))
	for _, s := range selected {
		key := s.Txid + ":" + strconv.Itoa(int(s.Vout))
		if tu, ok := index[key]; ok {
			out = append(out, tu)
		}
	}
	return out
}
