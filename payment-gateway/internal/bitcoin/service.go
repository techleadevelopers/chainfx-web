package bitcoin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"payment-gateway/internal/database"
)

// Service é o ponto central da rail BTC.
// Não tem dependência de nenhum pacote EVM/NFC/signer existente.
type Service struct {
	cfg      *Config
	provider Provider
	repo     *repository
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

	// Verificar se já existe
	addr, err := s.repo.GetUserAddress(ctx, userID, network)
	if err != nil {
		return nil, fmt.Errorf("btc: erro ao buscar endereço: %w", err)
	}
	if addr != nil {
		return addr, nil
	}

	// Alocar próximo índice de derivação de forma atômica
	index, err := s.repo.GetNextDerivationIndex(ctx, network)
	if err != nil {
		return nil, fmt.Errorf("btc: erro ao alocar índice: %w", err)
	}

	address, _, derivPath, err := DeriveReceiveAddress(s.cfg, uint32(index))
	if err != nil {
		return nil, fmt.Errorf("btc: erro ao derivar endereço: %w", err)
	}

	newAddr := BTCAddress{
		ID:              uuid.New().String(),
		UserID:          userID,
		Network:         network,
		Address:         address,
		DerivationPath:  derivPath,
		DerivationIndex: index,
		AddressType:     AddressTypeP2WPKH,
		Status:          "active",
	}

	if err := s.repo.AllocateAddress(ctx, newAddr); err != nil {
		return nil, fmt.Errorf("btc: erro ao salvar endereço: %w", err)
	}

	return &newAddr, nil
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

		if err := s.repo.UpsertUTXO(ctx, u); err != nil {
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

	// 1. Idempotência: verificar se já foi processada
	existing, err := s.repo.GetTransactionByIdempotencyKey(ctx, req.UserID, req.IdempotencyKey)
	if err != nil && err != sql.ErrNoRows {
		return SendResult{}, fmt.Errorf("btc: erro ao checar idempotência: %w", err)
	}
	if existing != nil {
		// Detectar conflito: mesma chave de idempotência, payload diferente
		if existing.RequestHash != "" && existing.RequestHash != reqHash {
			return SendResult{}, ErrIdempotencyConflict
		}
		// Mesmo payload → retornar resultado cacheado
		return SendResult{
			TxID:       existing.Txid,
			FeeSats:    existing.FeeSats,
			AmountSats: existing.AmountSats,
			Status:     existing.Status,
		}, nil
	}

	// 2. Validações
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

	// 3. Buscar seed/xpriv para assinar
	if s.cfg.EncryptedSeed == "" || s.cfg.EncryptionKey == "" {
		return SendResult{}, ErrNoSeed
	}
	accountXpriv, err := s.decryptAndParseXpriv()
	if err != nil {
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
		return SendResult{}, ErrFeeTooHigh
	}

	// 5. Buscar UTXOs confirmados com SELECT FOR UPDATE SKIP LOCKED (via ReserveUTXOs atômico)
	utxos, err := s.repo.GetConfirmedUTXOs(ctx, req.UserID, network)
	if err != nil {
		return SendResult{}, fmt.Errorf("btc: erro ao buscar UTXOs: %w", err)
	}

	// 6. Seleção de moedas
	selected, changeSats, feeSats, err := SelectUTXOs(utxos, req.AmountSats, feeRate, s.cfg.DustLimitSats)
	if err != nil {
		return SendResult{}, err
	}

	// 7. Reservar UTXOs atomicamente (UPDATE WHERE status='confirmed' evita double-spend)
	utxoIDs := make([]string, len(selected))
	for i, u := range selected {
		utxoIDs[i] = u.ID
	}
	if err := s.repo.ReserveUTXOs(ctx, utxoIDs); err != nil {
		return SendResult{}, fmt.Errorf("btc: erro ao reservar UTXOs: %w", err)
	}

	// 8. Construir inputs (derivar chave privada por índice)
	txInputs, err := s.buildTxInputs(ctx, selected, accountXpriv)
	if err != nil {
		_ = s.repo.ReleaseUTXOs(ctx, utxoIDs)
		return SendResult{}, fmt.Errorf("btc: erro ao construir inputs: %w", err)
	}

	// 9. Construir outputs
	destScript, err := ScriptFromAddress(req.ToAddress, s.cfg.HRP())
	if err != nil {
		_ = s.repo.ReleaseUTXOs(ctx, utxoIDs)
		return SendResult{}, fmt.Errorf("btc: endereço de destino inválido: %w", err)
	}
	txOutputs := []TxOutput{{ValueSats: req.AmountSats, ScriptPubKey: destScript}}

	// Troco
	if changeSats > 0 {
		changeAddr, err := s.repo.GetUserAddress(ctx, req.UserID, network)
		if err != nil || changeAddr == nil {
			_ = s.repo.ReleaseUTXOs(ctx, utxoIDs)
			return SendResult{}, fmt.Errorf("btc: endereço de troco não encontrado")
		}
		changeScript, err := ScriptFromAddress(changeAddr.Address, s.cfg.HRP())
		if err != nil {
			_ = s.repo.ReleaseUTXOs(ctx, utxoIDs)
			return SendResult{}, err
		}
		txOutputs = append(txOutputs, TxOutput{ValueSats: changeSats, ScriptPubKey: changeScript})
	}

	// 10. Assinar transação
	rawHex, txid, err := BuildAndSignTx(txInputs, txOutputs)
	if err != nil {
		_ = s.repo.ReleaseUTXOs(ctx, utxoIDs)
		return SendResult{}, fmt.Errorf("btc: erro ao assinar transação: %w", err)
	}

	// 11. Persistir antes do broadcast (idempotência + auditoria)
	now := time.Now()
	btcTx := BTCTransaction{
		ID:              uuid.New().String(),
		UserID:          req.UserID,
		Network:         network,
		Direction:       TxDirectionWithdrawal,
		Txid:            txid,
		RawTxHash:       rawHex,
		DestinationAddr: req.ToAddress,
		AmountSats:      req.AmountSats,
		FeeSats:         feeSats,
		FeeRateSatVByte: feeRate,
		Status:          TxStatusSigned,
		Confirmations:   0,
		IdempotencyKey:  req.IdempotencyKey,
		RequestHash:     req.RequestHash, // sempre preenchido agora
		BroadcastAt:     nil,
	}
	if err := s.repo.SaveTransaction(ctx, btcTx); err != nil {
		_ = s.repo.ReleaseUTXOs(ctx, utxoIDs)
		return SendResult{}, fmt.Errorf("btc: erro ao salvar transação: %w", err)
	}

	// 12. Broadcast
	broadcastedTxid, err := s.provider.BroadcastTransaction(ctx, rawHex)
	if err != nil {
		if err == ErrBroadcastUnknown {
			_ = s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, TxStatusBroadcast, 0, 0)
			slog.Warn("btc: broadcast incerto — aguardando reconciliação", "txid", txid)
			return SendResult{TxID: txid, FeeSats: feeSats, AmountSats: req.AmountSats, Status: TxStatusBroadcast}, nil
		}
		_ = s.repo.ReleaseUTXOs(ctx, utxoIDs)
		_ = s.repo.UpdateTransactionError(ctx, btcTx.ID, "BROADCAST_FAILED", err.Error(), TxStatusFailed)
		return SendResult{}, fmt.Errorf("btc: broadcast falhou: %w", err)
	}

	finalTxid := broadcastedTxid
	if finalTxid == "" {
		finalTxid = txid
	}

	// 13. Atualizar status e marcar UTXOs como gastos
	_ = s.repo.UpdateTransactionConfirmations(ctx, btcTx.ID, TxStatusBroadcast, 0, 0)
	_ = s.repo.MarkUTXOsSpent(ctx, finalTxid, utxoIDs)

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
			addr, err := s.repo.GetUserAddress(ctx, u.UserID, string(s.cfg.Network))
			if err != nil || addr == nil {
				return nil, fmt.Errorf("btc: endereço do UTXO não encontrado")
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
