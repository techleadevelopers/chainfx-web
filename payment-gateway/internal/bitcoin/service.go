package bitcoin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"payment-gateway/internal/database"
)

// Service é o ponto central da rail BTC.
// Não tem dependência de nenhum pacote EVM/NFC/signer existente.
type Service struct {
	cfg      *Config
	provider Provider
	repo     btcStore
}

type btcStore interface {
	GetOrCreateUserAddress(ctx context.Context, userID, network string, derive func(index int) (BTCAddress, error)) (*BTCAddress, error)
	GetUserAddress(ctx context.Context, userID, network string) (*BTCAddress, error)
	GetAddressByID(ctx context.Context, id, network string) (*BTCAddress, error)
	GetNextDerivationIndex(ctx context.Context, network string) (int, error)
	AllocateAddress(ctx context.Context, a BTCAddress) error
	GetActiveUTXOsByAddress(ctx context.Context, walletAddressID, network string) ([]UTXO, error)
	UpsertUTXO(ctx context.Context, u UTXO, minConfirmations int) error
	MarkUTXOOrphaned(ctx context.Context, id string) error
	GetBalance(ctx context.Context, userID, network string) (Balance, error)
	GetTodayWithdrawalSats(ctx context.Context, userID, network string) (int64, error)
	GetConfirmedUTXOs(ctx context.Context, userID, network string) ([]UTXO, error)
	ClaimTransaction(ctx context.Context, t BTCTransaction) (*BTCTransaction, bool, error)
	PersistSpendPlan(ctx context.Context, txID string, inputs []BTCTransactionInput, outputs []BTCTransactionOutput, feeSats, feeRate int64) error
	UpdateTransactionSigned(ctx context.Context, id, txid, rawHex string, feeSats, feeRate int64) error
	UpdateTransactionStatus(ctx context.Context, id, status, code, message string) error
	UpdateTransactionError(ctx context.Context, id, code, message, status string) error
	UpdateTransactionConfirmations(ctx context.Context, id, status string, confs int, blockHeight int64) error
	ReleaseSpend(ctx context.Context, txID string) error
	MarkUTXOsSpent(ctx context.Context, spentByTxid string, ids []string) error
	MarkTransactionUTXOsSpent(ctx context.Context, txID, spentByTxid string) error
	GetPendingTransactions(ctx context.Context, network string) ([]BTCTransaction, error)
	GetAllActiveAddresses(ctx context.Context, network string) ([]BTCAddress, error)
	ListUserTransactions(ctx context.Context, userID, network string, limit int) ([]BTCTransaction, error)
	GetTransactionByTxid(ctx context.Context, txid, network string) (*BTCTransaction, error)
	UpdateWalletState(ctx context.Context, network string, lastScannedBlock int64) error
}

// NewService cria o Service BTC se BTC_ENABLED=true; retorna (nil, nil) se desabilitado.
func NewService(db *database.DB) (*Service, error) {
	cfg, err := LoadBTCConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil // BTC desabilitado — não é erro
	}

	provider := NewMempoolProvider(cfg)
	repo := &repository{sql: db.SQL}
	return &Service{cfg: cfg, provider: provider, repo: repo}, nil
}

// Config expõe a configuração (para o worker).
func (s *Service) Config() *Config { return s.cfg }

// ─── Fase 2: Geração de endereço de recebimento ───────────────────────────────

// GetOrCreateAddress retorna o endereço BTC ativo do usuário, criando-o se necessário.
// A alocação de índice é atômica via UPDATE btc_wallet_state RETURNING — sem races.
func (s *Service) GetOrCreateAddress(ctx context.Context, userID string) (*BTCAddress, error) {
	network := string(s.cfg.Network)
	return s.repo.GetOrCreateUserAddress(ctx, userID, network, func(index int) (BTCAddress, error) {
		address, _, derivPath, err := DeriveReceiveAddress(s.cfg, uint32(index))
		if err != nil {
			return BTCAddress{}, fmt.Errorf("btc: erro ao derivar endereco: %w", err)
		}
		return BTCAddress{
			ID:              uuid.New().String(),
			UserID:          userID,
			Network:         network,
			Address:         address,
			DerivationPath:  derivPath,
			DerivationIndex: index,
			AddressType:     AddressTypeP2WPKH,
			Status:          "active",
		}, nil
	})
}

// ─── Fase 3 & 4: Detecção de depósitos + saldo ───────────────────────────────

// SyncAddressUTXOs busca UTXOs do provider, persiste no banco e detecta reorgs.
// Mantida para compatibilidade; a versão rica é SyncAddressUTXOsWithEvents.
func (s *Service) SyncAddressUTXOs(ctx context.Context, addr BTCAddress) error {
	_, _, err := s.SyncAddressUTXOsWithEvents(ctx, addr)
	return err
}

// SyncAddressUTXOsWithEvents sincroniza UTXOs e retorna eventos produzidos:
//   - "btc.deposit.detected" — UTXO novo detectado (pending)
//   - "btc.deposit.confirmed" — UTXO atingiu min confirmações
//   - UTXOs que desapareceram do provider são marcados como 'orphaned'
//
// Também retorna a altura do bloco mais recente observada.
func (s *Service) SyncAddressUTXOsWithEvents(ctx context.Context, addr BTCAddress) ([]BTCWorkerEvent, int64, error) {
	network := string(s.cfg.Network)

	// 1. Buscar estado atual no DB
	existing, err := s.repo.GetActiveUTXOsByAddress(ctx, addr.ID, network)
	if err != nil {
		return nil, 0, fmt.Errorf("btc: sync utxos DB %s: %w", addr.Address, err)
	}
	existingByKey := make(map[string]UTXO, len(existing))
	for _, u := range existing {
		existingByKey[u.Txid+":"+strconv.Itoa(int(u.Vout))] = u
	}

	// 2. Buscar UTXOs do provider
	providerUTXOs, err := s.provider.GetAddressUTXOs(ctx, addr.Address)
	if err != nil {
		return nil, 0, fmt.Errorf("btc: sync utxos provider %s: %w", addr.Address, err)
	}

	blockHeight, _ := s.provider.GetCurrentBlockHeight(ctx)

	// 3. Upsert e detectar novos / recém confirmados
	var events []BTCWorkerEvent
	providerKeys := make(map[string]bool, len(providerUTXOs))

	for _, pu := range providerUTXOs {
		key := pu.Txid + ":" + strconv.Itoa(int(pu.Vout))
		providerKeys[key] = true

		confirmations := 0
		status := UTXOStatusPending
		if pu.Status.Confirmed {
			if blockHeight > 0 && pu.Status.BlockHeight > 0 {
				confirmations = int(blockHeight - pu.Status.BlockHeight + 1)
			}
			if confirmations >= s.cfg.MinConfirmations {
				status = UTXOStatusConfirmed
			}
		}

		scriptHex := ""
		if script, e := ScriptFromAddress(addr.Address, s.cfg.HRP()); e == nil {
			scriptHex = hex.EncodeToString(script)
		}

		u := UTXO{
			ID:              uuid.New().String(),
			Network:         network,
			UserID:          addr.UserID,
			WalletAddressID: addr.ID,
			Txid:            pu.Txid,
			Vout:            pu.Vout,
			ValueSats:       pu.Value,
			ScriptPubKey:    scriptHex,
			BlockHeight:     pu.Status.BlockHeight,
			Confirmations:   confirmations,
			Status:          status,
			DetectedAt:      time.Now(),
		}

		prev, wasKnown := existingByKey[key]

		if err := s.repo.UpsertUTXO(ctx, u, s.cfg.MinConfirmations); err != nil {
			slog.Error("btc: erro ao upsert UTXO",
				"address", addr.Address, "txid", pu.Txid, "err", err)
			continue
		}

		// Publicar evento de depósito detectado (UTXO nunca visto antes)
		if !wasKnown {
			events = append(events, BTCWorkerEvent{
				Type: "btc.deposit.detected",
				Payload: map[string]any{
					"user_id":     addr.UserID,
					"address":     addr.Address,
					"txid":        pu.Txid,
					"vout":        pu.Vout,
					"amount_sats": pu.Value,
					"network":     network,
					"status":      status,
				},
			})
		} else if prev.Status == UTXOStatusPending && status == UTXOStatusConfirmed {
			// UTXO que acabou de confirmar
			events = append(events, BTCWorkerEvent{
				Type: "btc.deposit.confirmed",
				Payload: map[string]any{
					"user_id":       addr.UserID,
					"address":       addr.Address,
					"txid":          pu.Txid,
					"vout":          pu.Vout,
					"amount_sats":   pu.Value,
					"network":       network,
					"confirmations": confirmations,
				},
			})
		}
	}

	// 4. Detectar reorg: UTXOs no DB que sumiram do provider → orphaned
	for key, dbUTXO := range existingByKey {
		if !providerKeys[key] {
			slog.Warn("btc: UTXO desapareceu do provider (possível reorg)",
				"txid", dbUTXO.Txid, "vout", dbUTXO.Vout, "address", addr.Address)
			if err := s.repo.MarkUTXOOrphaned(ctx, dbUTXO.ID); err != nil {
				slog.Error("btc: erro ao marcar UTXO orphaned", "id", dbUTXO.ID, "err", err)
			}
		}
	}

	return events, blockHeight, nil
}

// GetBalance retorna o saldo confirmado, pendente, reservado e disponível do usuário.
func (s *Service) GetBalance(ctx context.Context, userID string) (Balance, error) {
	return s.repo.GetBalance(ctx, userID, string(s.cfg.Network))
}

// ─── Fase 5: Estimativa de fee ───────────────────────────────────────────────

// EstimateFee retorna a estimativa de fee para enviar amountSats sats.
func (s *Service) EstimateFee(ctx context.Context, amountSats int64) (FeeEstimate, error) {
	feeRate, err := s.provider.EstimateFeeRate(ctx, s.cfg.FeeTargetBlocks)
	if err != nil {
		// Fallback para fee mínima configurada
		feeRate = s.cfg.MinFeeRateSatVB
	}

	// Clamp ao range configurado
	if feeRate < s.cfg.MinFeeRateSatVB {
		feeRate = s.cfg.MinFeeRateSatVB
	}
	if feeRate > s.cfg.MaxFeeRateSatVB {
		feeRate = s.cfg.MaxFeeRateSatVB
	}

	// Estimativa conservadora: 1 input, 2 outputs (destino + troco)
	vsize := EstimateVSize(1, 2)
	feeSats := feeRate * int64(vsize)

	return FeeEstimate{
		FeeRateSatVByte: feeRate,
		EstimatedFeeSat: feeSats,
		VirtualSize:     vsize,
		Policy:          s.cfg.FeePolicy,
	}, nil
}

// ─── Fase 5: Envio de BTC ────────────────────────────────────────────────────

// Send executa um saque BTC: seleciona UTXOs, constrói, assina e faz broadcast.
// Idempotente: se a chave já existe com o mesmo payload, retorna o resultado anterior.
// Retorna ErrIdempotencyConflict se a chave foi usada com payload diferente.
func (s *Service) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	network := string(s.cfg.Network)

	// Computar request_hash para detectar conflitos de idempotência
	reqHash := computeRequestHash(req.UserID, req.ToAddress, req.AmountSats, req.IdempotencyKey)
	if req.RequestHash == "" {
		req.RequestHash = reqHash
	}

	// 1. Validações sem efeito econômico
	if req.AmountSats <= 0 {
		return SendResult{}, fmt.Errorf("btc: amountSats deve ser > 0")
	}
	if req.AmountSats < s.cfg.DustLimitSats {
		return SendResult{}, ErrDustOutput
	}
	if s.cfg.MaxSendSats > 0 && req.AmountSats > s.cfg.MaxSendSats {
		return SendResult{}, ErrMaxSendExceeded
	}

	// Validar limite diário de saques
	if s.cfg.DailySendLimitSats > 0 {
		todaySent, err := s.repo.GetTodayWithdrawalSats(ctx, req.UserID, network)
		if err != nil {
			return SendResult{}, fmt.Errorf("btc: erro ao verificar limite diário: %w", err)
		}
		if todaySent+req.AmountSats > s.cfg.DailySendLimitSats {
			return SendResult{}, ErrDailyLimitExceeded
		}
	}

	// Validar endereço de destino
	if err := ValidateAddress(req.ToAddress, s.cfg.HRP()); err != nil {
		return SendResult{}, fmt.Errorf("%w: %s", ErrInvalidAddress, req.ToAddress)
	}

	// 2. Claim idempotente antes de reservar UTXO, assinar ou broadcastar.
	now := time.Now()
	claimed := BTCTransaction{
		ID:              uuid.New().String(),
		UserID:          req.UserID,
		Network:         network,
		Direction:       TxDirectionWithdrawal,
		DestinationAddr: req.ToAddress,
		AmountSats:      req.AmountSats,
		Status:          TxStatusCreated,
		IdempotencyKey:  req.IdempotencyKey,
		RequestHash:     req.RequestHash,
		CreatedAt:       now,
	}
	btcTx, created, err := s.repo.ClaimTransaction(ctx, claimed)
	if err != nil {
		return SendResult{}, fmt.Errorf("btc: erro ao criar claim idempotente: %w", err)
	}
	if btcTx == nil {
		return SendResult{}, fmt.Errorf("btc: claim idempotente não retornou operação")
	}
	if btcTx.RequestHash != "" && btcTx.RequestHash != reqHash {
		return SendResult{}, ErrIdempotencyConflict
	}
	if !created {
		return SendResult{
			TxID:       btcTx.Txid,
			FeeSats:    btcTx.FeeSats,
			AmountSats: btcTx.AmountSats,
			Status:     btcTx.Status,
		}, nil
	}

	// 3. Buscar seed/xpriv para assinar. Falha aqui não reserva UTXO.
	if s.cfg.EncryptedSeed == "" || s.cfg.EncryptionKey == "" {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "SIGNING_NOT_CONFIGURED", ErrNoSeed.Error())
		return SendResult{}, ErrNoSeed
	}
	accountXpriv, err := s.decryptAndParseXpriv()
	if err != nil {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "SIGNING_CONFIG_INVALID", err.Error())
		return SendResult{}, fmt.Errorf("btc: erro ao decifrar seed: %w", err)
	}

	// 4. Fee rate
	feeRate := req.FeeRateSatVB
	if feeRate <= 0 {
		feeRate, err = s.provider.EstimateFeeRate(ctx, s.cfg.FeeTargetBlocks)
		if err != nil {
			feeRate = s.cfg.MinFeeRateSatVB
		}
	}
	if feeRate < s.cfg.MinFeeRateSatVB {
		feeRate = s.cfg.MinFeeRateSatVB
	}
	if feeRate > s.cfg.MaxFeeRateSatVB {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "FEE_TOO_HIGH", ErrFeeTooHigh.Error())
		return SendResult{}, ErrFeeTooHigh
	}

	// 5. Buscar UTXOs confirmados com SELECT FOR UPDATE SKIP LOCKED (via ReserveUTXOs atômico)
	utxos, err := s.repo.GetConfirmedUTXOs(ctx, req.UserID, network)
	if err != nil {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "UTXO_QUERY_FAILED", err.Error())
		return SendResult{}, fmt.Errorf("btc: erro ao buscar UTXOs: %w", err)
	}

	// 6. Seleção de moedas
	selected, changeSats, feeSats, err := SelectUTXOs(utxos, req.AmountSats, feeRate, s.cfg.DustLimitSats)
	if err != nil {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "COIN_SELECT_FAILED", err.Error())
		return SendResult{}, err
	}

	// 7. Construir plano persistível de outputs.
	destScript, err := ScriptFromAddress(req.ToAddress, s.cfg.HRP())
	if err != nil {
		return SendResult{}, fmt.Errorf("btc: endereço de destino inválido: %w", err)
	}
	txOutputs := []TxOutput{{ValueSats: req.AmountSats, ScriptPubKey: destScript}}
	persistedOutputs := []BTCTransactionOutput{{
		ID:            uuid.New().String(),
		TransactionID: btcTx.ID,
		Vout:          0,
		Address:       req.ToAddress,
		ValueSats:     req.AmountSats,
		OutputType:    "destination",
		ScriptPubKey:  hex.EncodeToString(destScript),
	}}

	// Troco
	if changeSats > 0 {
		changeAddr, err := s.repo.GetUserAddress(ctx, req.UserID, network)
		if err != nil || changeAddr == nil {
			return SendResult{}, fmt.Errorf("btc: endereço de troco não encontrado")
		}
		changeScript, err := ScriptFromAddress(changeAddr.Address, s.cfg.HRP())
		if err != nil {
			return SendResult{}, err
		}
		txOutputs = append(txOutputs, TxOutput{ValueSats: changeSats, ScriptPubKey: changeScript})
		persistedOutputs = append(persistedOutputs, BTCTransactionOutput{
			ID:            uuid.New().String(),
			TransactionID: btcTx.ID,
			Vout:          1,
			Address:       changeAddr.Address,
			ValueSats:     changeSats,
			OutputType:    "change",
			ScriptPubKey:  hex.EncodeToString(changeScript),
		})
	}

	persistedInputs, err := s.persistableInputs(ctx, selected)
	if err != nil {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "INPUT_BUILD_FAILED", err.Error())
		return SendResult{}, fmt.Errorf("btc: erro ao montar inputs auditáveis: %w", err)
	}
	if err := validateSpendMath(persistedInputs, persistedOutputs, feeSats); err != nil {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "SPEND_MATH_INVALID", err.Error())
		return SendResult{}, err
	}

	// 8. Reservar UTXOs e persistir inputs/outputs atomicamente.
	if err := s.repo.PersistSpendPlan(ctx, btcTx.ID, persistedInputs, persistedOutputs, feeSats, feeRate); err != nil {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "UTXO_RESERVATION_FAILED", err.Error())
		if errors.Is(err, ErrDoubleSpend) {
			return SendResult{}, ErrDoubleSpend
		}
		return SendResult{}, fmt.Errorf("btc: erro ao reservar UTXOs: %w", err)
	}

	// 9. Construir inputs com chaves privadas e assinar.
	txInputs, err := s.buildTxInputs(ctx, selected, accountXpriv)
	if err != nil {
		_ = s.repo.ReleaseSpend(ctx, btcTx.ID)
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "INPUT_SIGNING_FAILED", err.Error())
		return SendResult{}, fmt.Errorf("btc: erro ao construir inputs: %w", err)
	}
	rawHex, txid, err := BuildAndSignTx(txInputs, txOutputs)
	if err != nil {
		_ = s.repo.ReleaseSpend(ctx, btcTx.ID)
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeSign, "SIGN_FAILED", err.Error())
		return SendResult{}, fmt.Errorf("btc: erro ao assinar transação: %w", err)
	}

	// 10. Persistir raw signed tx e txid antes do broadcast.
	if err := s.repo.UpdateTransactionSigned(ctx, btcTx.ID, txid, rawHex, feeSats, feeRate); err != nil {
		_ = s.repo.ReleaseSpend(ctx, btcTx.ID)
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusFailedBeforeBroadcast, "SIGNED_PERSIST_FAILED", err.Error())
		return SendResult{}, fmt.Errorf("btc: erro ao persistir transação assinada: %w", err)
	}

	// 11. Broadcast
	broadcastedTxid, err := s.provider.BroadcastTransaction(ctx, rawHex)
	if err != nil {
		if errors.Is(err, ErrBroadcastUnknown) {
			_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusBroadcastUnknown, "BROADCAST_UNKNOWN", err.Error())
			slog.Warn("btc: broadcast incerto — aguardando reconciliação", "txid", txid)
			return SendResult{TxID: txid, FeeSats: feeSats, AmountSats: req.AmountSats, Status: TxStatusBroadcastUnknown}, nil
		}
		if isAlreadyKnownBroadcastError(err) {
			_ = s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, TxStatusBroadcast, 0, 0)
			_ = s.repo.MarkUTXOsSpent(ctx, txid, selectedUTXOIDs(selected))
			return SendResult{TxID: txid, FeeSats: feeSats, AmountSats: req.AmountSats, Status: TxStatusBroadcast}, nil
		}
		_ = s.repo.ReleaseSpend(ctx, btcTx.ID)
		_ = s.repo.UpdateTransactionError(ctx, btcTx.ID, "BROADCAST_FAILED", err.Error(), TxStatusFailedBeforeBroadcast)
		return SendResult{}, fmt.Errorf("btc: broadcast falhou: %w", err)
	}

	finalTxid := broadcastedTxid
	if finalTxid == "" {
		finalTxid = txid
	}

	// 13. Atualizar status e marcar UTXOs como gastos
	_ = s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, TxStatusBroadcast, 0, 0)
	_ = s.repo.MarkUTXOsSpent(ctx, finalTxid, selectedUTXOIDs(selected))

	btcTx.BroadcastAt = &now
	slog.Info("btc: transação broadcast com sucesso",
		"txid", finalTxid, "amount_sats", req.AmountSats, "fee_sats", feeSats)

	return SendResult{
		TxID:       finalTxid,
		FeeSats:    feeSats,
		AmountSats: req.AmountSats,
		Status:     TxStatusBroadcast,
	}, nil
}

// ─── Fase 6: Confirmações ────────────────────────────────────────────────────

// UpdateTransactionConfirmation atualiza confirmações (sem retornar wasConfirmed).
func (s *Service) UpdateTransactionConfirmation(ctx context.Context, btcTx BTCTransaction) error {
	_, err := s.UpdateTransactionConfirmationWithResult(ctx, btcTx)
	return err
}

// UpdateTransactionConfirmationWithResult atualiza confirmações e retorna se a tx
// acabou de ser confirmada neste ciclo (para o worker publicar eventos).
func (s *Service) UpdateTransactionConfirmationWithResult(ctx context.Context, btcTx BTCTransaction) (wasConfirmed bool, err error) {
	if btcTx.Status == TxStatusSigned || btcTx.Status == TxStatusBroadcastUnknown {
		return s.ReconcileSignedOrBroadcastUnknown(ctx, btcTx)
	}
	status, provErr := s.provider.GetTransaction(ctx, btcTx.Txid)
	if provErr != nil {
		return false, provErr
	}

	blockHeight, _ := s.provider.GetCurrentBlockHeight(ctx)

	confs := 0
	txStatus := TxStatusPending
	if status.Status.Confirmed {
		if blockHeight > 0 && status.Status.BlockHeight > 0 {
			confs = int(blockHeight - status.Status.BlockHeight + 1)
		}
		if confs >= s.cfg.MinConfirmations {
			txStatus = TxStatusConfirmed
		}
	}

	updErr := s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, txStatus, confs, status.Status.BlockHeight)
	if updErr != nil {
		return false, updErr
	}

	// wasConfirmed = transição pending/broadcast → confirmed neste ciclo
	wasConfirmed = txStatus == TxStatusConfirmed &&
		btcTx.Status != TxStatusConfirmed
	return wasConfirmed, nil
}

// ReconcileSignedOrBroadcastUnknown recupera operações com raw tx persistida.
// Nunca reconstrói transação, recalcula fee, troca change ou libera UTXO em
// broadcast_unknown. O único broadcast feito aqui usa exatamente raw_tx_hash.
func (s *Service) ReconcileSignedOrBroadcastUnknown(ctx context.Context, btcTx BTCTransaction) (bool, error) {
	if btcTx.RawTxHash == "" || btcTx.Txid == "" {
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusManualReview, "MISSING_SIGNED_TX", "tx assinada ausente para recovery")
		return false, fmt.Errorf("btc: tx assinada ausente para recovery")
	}
	computed, err := TxIDFromRawSignedHex(btcTx.RawTxHash)
	if err != nil || computed != btcTx.Txid {
		msg := "raw tx não corresponde ao txid persistido"
		if err != nil {
			msg = err.Error()
		}
		_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusManualReview, "CORRUPT_SIGNED_TX", msg)
		return false, fmt.Errorf("btc: raw tx corrupta ou divergente")
	}

	status, err := s.provider.GetTransaction(ctx, btcTx.Txid)
	if err == nil {
		return s.applyProviderTxStatus(ctx, btcTx, status)
	}
	if !isProviderNotFound(err) {
		return false, err
	}

	if btcTx.Status == TxStatusSigned {
		broadcastedTxid, err := s.provider.BroadcastTransaction(ctx, btcTx.RawTxHash)
		if err != nil {
			if errors.Is(err, ErrBroadcastUnknown) {
				_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusBroadcastUnknown, "BROADCAST_UNKNOWN", err.Error())
				return false, nil
			}
			if isAlreadyKnownBroadcastError(err) {
				_ = s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, TxStatusBroadcast, 0, 0)
				return false, nil
			}
			_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusManualReview, "REBROADCAST_FAILED", err.Error())
			return false, err
		}
		if broadcastedTxid != "" && broadcastedTxid != btcTx.Txid {
			_ = s.repo.UpdateTransactionStatus(ctx, btcTx.ID, TxStatusManualReview, "TXID_MISMATCH", "provider retornou txid diferente")
			return false, fmt.Errorf("btc: provider retornou txid diferente")
		}
		_ = s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, TxStatusBroadcast, 0, 0)
		return false, nil
	}

	// broadcast_unknown sem presença ainda é ambíguo: não libera nem reconstrói.
	return false, nil
}

func (s *Service) applyProviderTxStatus(ctx context.Context, btcTx BTCTransaction, status *ProviderTxStatus) (bool, error) {
	blockHeight, _ := s.provider.GetCurrentBlockHeight(ctx)
	confs := 0
	txStatus := TxStatusBroadcast
	if status.Status.Confirmed {
		if blockHeight > 0 && status.Status.BlockHeight > 0 {
			confs = int(blockHeight - status.Status.BlockHeight + 1)
		}
		if confs >= s.cfg.MinConfirmations {
			txStatus = TxStatusConfirmed
		}
	}
	if err := s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, txStatus, confs, status.Status.BlockHeight); err != nil {
		return false, err
	}
	wasConfirmed := txStatus == TxStatusConfirmed && btcTx.Status != TxStatusConfirmed
	if wasConfirmed {
		_ = s.repo.MarkTransactionUTXOsSpent(ctx, btcTx.ID, btcTx.Txid)
	}
	return wasConfirmed, nil
}

func isProviderNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "não encontrado") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "retornou 404") ||
		strings.Contains(msg, "404")
}

// ─── Queries para handlers ────────────────────────────────────────────────────

// GetPendingTransactions retorna transações pendentes de confirmação.
func (s *Service) GetPendingTransactions(ctx context.Context) ([]BTCTransaction, error) {
	return s.repo.GetPendingTransactions(ctx, string(s.cfg.Network))
}

// GetAllActiveAddresses retorna todos os endereços ativos para scan de depósitos.
func (s *Service) GetAllActiveAddresses(ctx context.Context) ([]BTCAddress, error) {
	return s.repo.GetAllActiveAddresses(ctx, string(s.cfg.Network))
}

// ListUserTransactions lista transações do usuário.
func (s *Service) ListUserTransactions(ctx context.Context, userID string, limit int) ([]BTCTransaction, error) {
	return s.repo.ListUserTransactions(ctx, userID, string(s.cfg.Network), limit)
}

// GetTransactionByTxid busca uma transação pelo txid.
func (s *Service) GetTransactionByTxid(ctx context.Context, txid string) (*BTCTransaction, error) {
	return s.repo.GetTransactionByTxid(ctx, txid, string(s.cfg.Network))
}

// ─── Helpers internos ─────────────────────────────────────────────────────────

// computeRequestHash gera um hash determinístico do payload do pedido de saque.
// Usado para detectar reuso indevido de idempotency key com payload diferente.
func computeRequestHash(userID, toAddress string, amountSats int64, idempotencyKey string) string {
	raw := userID + "|" + toAddress + "|" + strconv.FormatInt(amountSats, 10) + "|" + idempotencyKey
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// buildTxInputs mapeia UTXOs selecionados para TxInputs com chave privada derivada.
func (s *Service) buildTxInputs(ctx context.Context, utxos []UTXO, accountXpriv *ExtendedKey) ([]TxInput, error) {
	indexCache := make(map[string]BTCAddress)

	var inputs []TxInput
	for _, u := range utxos {
		addrInfo, ok := indexCache[u.WalletAddressID]
		if !ok {
			addr, err := s.repo.GetAddressByID(ctx, u.WalletAddressID, string(s.cfg.Network))
			if err != nil || addr == nil {
				return nil, fmt.Errorf("btc: endereço do UTXO não encontrado")
			}
			if addr.UserID != u.UserID {
				return nil, fmt.Errorf("btc: UTXO pertence a outro usuário")
			}
			addrInfo = *addr
			indexCache[u.WalletAddressID] = addrInfo
		}

		privKey, pubKey, err := DerivePrivKeyAtIndex(accountXpriv, uint32(addrInfo.DerivationIndex))
		if err != nil {
			return nil, fmt.Errorf("btc: erro ao derivar chave: %w", err)
		}

		inputs = append(inputs, TxInput{
			Txid:         u.Txid,
			Vout:         u.Vout,
			ValueSats:    u.ValueSats,
			ScriptPubKey: u.ScriptPubKey,
			PrivKeyBytes: privKey,
			PubKeyBytes:  pubKey,
		})
	}
	return inputs, nil
}

func (s *Service) persistableInputs(ctx context.Context, utxos []UTXO) ([]BTCTransactionInput, error) {
	out := make([]BTCTransactionInput, 0, len(utxos))
	for _, u := range utxos {
		addr, err := s.repo.GetAddressByID(ctx, u.WalletAddressID, string(s.cfg.Network))
		if err != nil || addr == nil {
			return nil, fmt.Errorf("btc: endereço do UTXO não encontrado")
		}
		if addr.UserID != u.UserID {
			return nil, fmt.Errorf("btc: UTXO pertence a outro usuário")
		}
		out = append(out, BTCTransactionInput{
			ID:              uuid.New().String(),
			UTXOID:          u.ID,
			UserID:          u.UserID,
			WalletAddressID: u.WalletAddressID,
			Address:         addr.Address,
			DerivationPath:  addr.DerivationPath,
			DerivationIndex: addr.DerivationIndex,
			Txid:            u.Txid,
			Vout:            u.Vout,
			ValueSats:       u.ValueSats,
			ScriptPubKey:    u.ScriptPubKey,
		})
	}
	return out, nil
}

func selectedUTXOIDs(utxos []UTXO) []string {
	ids := make([]string, len(utxos))
	for i, u := range utxos {
		ids[i] = u.ID
	}
	return ids
}

func validateSpendMath(inputs []BTCTransactionInput, outputs []BTCTransactionOutput, feeSats int64) error {
	if feeSats < 0 {
		return fmt.Errorf("btc: fee negativa")
	}
	var inTotal, outTotal int64
	for _, in := range inputs {
		if in.ValueSats <= 0 {
			return fmt.Errorf("btc: input inválido %s:%d", in.Txid, in.Vout)
		}
		inTotal += in.ValueSats
	}
	for _, out := range outputs {
		if out.ValueSats <= 0 {
			return fmt.Errorf("btc: output inválido vout=%d", out.Vout)
		}
		outTotal += out.ValueSats
	}
	if inTotal != outTotal+feeSats {
		return fmt.Errorf("btc: matemática inválida inputs=%d outputs=%d fee=%d", inTotal, outTotal, feeSats)
	}
	return nil
}

func isAlreadyKnownBroadcastError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already in mempool") ||
		strings.Contains(msg, "already known") ||
		strings.Contains(msg, "transaction already exists") ||
		strings.Contains(msg, "txn-already-in-mempool")
}

// decryptAndParseXpriv decifra o xpriv com AES-GCM e o parseia.
func (s *Service) decryptAndParseXpriv() (*ExtendedKey, error) {
	keyHex := s.cfg.EncryptionKey
	if len(keyHex) == 64 {
		keyBytes, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("btc: BTC_ENCRYPTION_KEY inválida")
		}
		cipherBytes, err := hex.DecodeString(s.cfg.EncryptedSeed)
		if err != nil {
			return nil, fmt.Errorf("btc: BTC_ENCRYPTED_SEED inválido")
		}
		plaintext, err := aesGCMDecrypt(keyBytes, cipherBytes)
		if err != nil {
			return nil, fmt.Errorf("btc: falha ao decifrar seed: %w", err)
		}
		return ParseXPriv(string(plaintext))
	}

	// Passphrase → SHA256 → chave AES-256
	keyBytes := sha256Sum([]byte(keyHex))
	cipherBytes, err := hex.DecodeString(s.cfg.EncryptedSeed)
	if err != nil {
		return nil, fmt.Errorf("btc: BTC_ENCRYPTED_SEED inválido")
	}
	plaintext, err := aesGCMDecrypt(keyBytes, cipherBytes)
	if err != nil {
		return nil, fmt.Errorf("btc: falha ao decifrar seed: %w", err)
	}
	return ParseXPriv(string(plaintext))
}

func aesGCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("ciphertext muito curto")
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	return gcm.Open(nil, nonce, ct, nil)
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

// ParseXPriv parseia um xpriv/tpriv base58check em ExtendedKey privado.
func ParseXPriv(xpriv string) (*ExtendedKey, error) {
	payload, err := base58CheckDecode(xpriv)
	if err != nil {
		return nil, ErrInvalidXPub
	}
	if len(payload) != 78 {
		return nil, ErrInvalidXPub
	}

	ver := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
	var net Network
	switch ver {
	case 0x0488ADE4:
		net = Mainnet
	case 0x04358394:
		net = Testnet
	default:
		return nil, ErrInvalidXPub
	}

	if payload[45] != 0x00 {
		return nil, fmt.Errorf("btc: não é um xpriv válido")
	}

	ek := &ExtendedKey{isPrivate: true, network: net}
	copy(ek.version[:], payload[:4])
	ek.depth = payload[4]
	copy(ek.fingerprint[:], payload[5:9])
	ek.childNum = uint32(payload[9])<<24 | uint32(payload[10])<<16 | uint32(payload[11])<<8 | uint32(payload[12])
	copy(ek.chainCode[:], payload[13:45])
	copy(ek.key[:], payload[45:78])
	return ek, nil
}
