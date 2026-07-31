package solana

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
)

var (
	ErrDisabled             = errors.New("solana: rail desabilitada")
	ErrSigningNotConfigured = errors.New("solana: signer nao configurado")
	ErrWithdrawalsDisabled  = errors.New("solana: withdrawals desabilitados")
	ErrInvalidAddress       = errors.New("solana: endereco invalido")
	ErrInsufficientFunds    = errors.New("solana: saldo insuficiente")
	ErrIdempotencyConflict  = errors.New("solana: idempotency conflict")
	ErrMaxSendExceeded      = errors.New("solana: max send excedido")
)

type Config struct {
	Enabled               bool
	RPCURLs               string
	Cluster               string
	WithdrawalsEnabled    bool
	ScanInterval          time.Duration
	TxScanInterval        time.Duration
	MinConfirmations      int
	MaxSendLamports       int64
	DerivationSecret      string
	ActiveDerivationKeyID string
	LegacyDerivationKeyID string
}

type Service struct {
	cfg     Config
	keyring *derivationKeyring
	rpc     solRPC
	repo    solStore
}

type solRPC interface {
	GetBalance(ctx context.Context, address string) (int64, error)
	GetLatestBlockhash(ctx context.Context) (string, int64, error)
	GetFeeForMessage(ctx context.Context, msg []byte) (int64, error)
	SendTransaction(ctx context.Context, tx []byte) (string, error)
	GetSignaturesForAddress(ctx context.Context, address, before string, limit int) ([]SignatureInfo, error)
	GetTransaction(ctx context.Context, signature string) (map[string]any, error)
	GetSignatureStatuses(ctx context.Context, signatures []string) (map[string]string, error)
	GetBlockHeight(ctx context.Context) (int64, error)
}

type solStore interface {
	ensureSchema(ctx context.Context) error
	getAddress(ctx context.Context, userID string) (*Address, error)
	insertAddress(ctx context.Context, userID, address, keyID string) (*Address, error)
	listActiveAddresses(ctx context.Context) ([]Address, error)
	insertTransaction(ctx context.Context, tx Transaction, metadata map[string]any) error
	transactionByIdempotency(ctx context.Context, userID, key string) (*Transaction, string, error)
	claimWithdrawal(ctx context.Context, req SendRequest) (*Transaction, bool, error)
	reserveWithdrawal(ctx context.Context, txID, userID, source string, amount, fee, balance int64) error
	persistSigned(ctx context.Context, txID, signature string, rawTx []byte, feePayer, blockhash string, lastValidBlockHeight int64) error
	markSubmitStatus(ctx context.Context, signature, status string) error
	listUserTransactions(ctx context.Context, userID string, limit int) ([]Transaction, error)
	getUserTransaction(ctx context.Context, userID, id string) (*Transaction, error)
	pendingWithdrawals(ctx context.Context) ([]Transaction, error)
	updateTransactionStatus(ctx context.Context, signature, status string, confirmations int) error
	seenReceiveKey(ctx context.Context, receiveKey string) (bool, error)
}

func NewService(db *database.DB, cfg *config.Config) (*Service, error) {
	if db == nil || db.SQL == nil || cfg == nil || !cfg.SolanaEnabled || strings.TrimSpace(cfg.SolanaRpcUrls) == "" {
		return nil, nil
	}
	secret := strings.TrimSpace(cfg.SignerHmacSecret)
	if secret == "" {
		secret = strings.TrimSpace(cfg.LGPDSecret)
	}
	keyring, err := newDerivationKeyringFromEnv(secret, cfg.IsProduction())
	if err != nil {
		return nil, err
	}
	scan := time.Duration(cfg.SolanaScanIntervalSec) * time.Second
	if scan <= 0 {
		scan = 30 * time.Second
	}
	txScan := time.Duration(cfg.SolanaTxScanIntervalSec) * time.Second
	if txScan <= 0 {
		txScan = 20 * time.Second
	}
	svc := &Service{
		cfg: Config{
			Enabled:               true,
			RPCURLs:               cfg.SolanaRpcUrls,
			Cluster:               firstNonEmpty(strings.TrimSpace(cfg.SolanaCluster), "mainnet"),
			WithdrawalsEnabled:    cfg.SolanaWithdrawalsEnabled,
			ScanInterval:          scan,
			TxScanInterval:        txScan,
			MinConfirmations:      maxInt(cfg.SolanaMinConfirmations, 1),
			MaxSendLamports:       cfg.SolanaMaxSendLamports,
			DerivationSecret:      secret,
			ActiveDerivationKeyID: keyring.activeID,
			LegacyDerivationKeyID: keyring.legacyID,
		},
		keyring: keyring,
		rpc:     NewRPCClient(cfg.SolanaRpcUrls),
		repo:    &repository{sql: db.SQL},
	}
	if svc.rpc == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.repo.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *Service) Config() Config { return s.cfg }

func (s *Service) GetOrCreateAddress(ctx context.Context, userID string) (*Address, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	if addr, err := s.repo.getAddress(ctx, userID); err != nil || addr != nil {
		return addr, err
	}
	key, err := s.derivePrivateKeyForKeyID(userID, s.derivationKeyID())
	if err != nil {
		return nil, err
	}
	address := base58Encode(key.Public().(ed25519.PublicKey))
	return s.repo.insertAddress(ctx, userID, address, s.derivationKeyID())
}

func (s *Service) GetBalance(ctx context.Context, userID string) (Balance, error) {
	addr, err := s.GetOrCreateAddress(ctx, userID)
	if err != nil {
		return Balance{}, err
	}
	lamports, err := s.rpc.GetBalance(ctx, addr.Address)
	if err != nil {
		return Balance{}, err
	}
	return Balance{
		Asset:                AssetSOL,
		Network:              Network,
		Address:              addr.Address,
		Lamports:             lamports,
		SOL:                  solString(lamports),
		AvailableLamports:    lamports,
		MinimumConfirmations: s.cfg.MinConfirmations,
		UpdatedAt:            time.Now().UTC(),
	}, nil
}

func (s *Service) EstimateFee(ctx context.Context, userID, toAddress string, amountLamports int64) (FeeEstimate, error) {
	if err := ValidateAddress(toAddress); err != nil {
		return FeeEstimate{}, ErrInvalidAddress
	}
	addr, err := s.GetOrCreateAddress(ctx, userID)
	if err != nil {
		return FeeEstimate{}, err
	}
	blockhash, _, err := s.rpc.GetLatestBlockhash(ctx)
	if err != nil {
		return FeeEstimate{Asset: AssetSOL, Network: Network, EstimatedFeeLamports: 5000, EstimatedFeeSOL: solString(5000), Policy: "fallback_base_fee"}, nil
	}
	msg, err := BuildUnsignedSOLTransferMessage(addr.Address, toAddress, blockhash, maxInt64(amountLamports, 1))
	if err != nil {
		return FeeEstimate{}, err
	}
	fee, err := s.rpc.GetFeeForMessage(ctx, msg)
	if err != nil || fee <= 0 {
		fee = 5000
	}
	return FeeEstimate{Asset: AssetSOL, Network: Network, EstimatedFeeLamports: fee, EstimatedFeeSOL: solString(fee), Policy: "rpc_getFeeForMessage"}, nil
}

func (s *Service) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	if s == nil {
		return SendResult{}, ErrDisabled
	}
	if !s.cfg.WithdrawalsEnabled {
		return SendResult{}, ErrWithdrawalsDisabled
	}
	if req.AmountLamports <= 0 {
		return SendResult{}, fmt.Errorf("solana: amount_lamports deve ser > 0")
	}
	if s.cfg.MaxSendLamports > 0 && req.AmountLamports > s.cfg.MaxSendLamports {
		return SendResult{}, ErrMaxSendExceeded
	}
	if err := ValidateAddress(req.ToAddress); err != nil {
		return SendResult{}, ErrInvalidAddress
	}
	if req.RequestHash == "" {
		req.RequestHash = requestHash(req.UserID, req.ToAddress, req.AmountLamports, req.IdempotencyKey)
	}
	claimed, owner, err := s.repo.claimWithdrawal(ctx, req)
	if err != nil {
		return SendResult{}, err
	}
	if claimed == nil {
		return SendResult{}, fmt.Errorf("solana: falha ao criar operacao")
	}
	if !owner {
		if claimed.RequestHash != "" && claimed.RequestHash != req.RequestHash {
			return SendResult{}, ErrIdempotencyConflict
		}
		return SendResult{Signature: claimed.Signature, AmountLamports: parseAmountRaw(claimed.AmountRaw), FeeLamports: claimed.FeeLamports, Status: claimed.Status}, nil
	}
	addr, err := s.GetOrCreateAddress(ctx, req.UserID)
	if err != nil {
		return SendResult{}, err
	}
	key, signerAddress, err := s.signingKeyForAddress(req.UserID, *addr)
	if err != nil {
		return SendResult{}, err
	}
	bal, err := s.rpc.GetBalance(ctx, addr.Address)
	if err != nil {
		return SendResult{}, err
	}
	feeEst, _ := s.EstimateFee(ctx, req.UserID, req.ToAddress, req.AmountLamports)
	if bal < req.AmountLamports+feeEst.EstimatedFeeLamports {
		_ = s.repo.reserveWithdrawal(ctx, claimed.ID, req.UserID, addr.Address, req.AmountLamports, feeEst.EstimatedFeeLamports, -1)
		return SendResult{}, ErrInsufficientFunds
	}
	if err := s.repo.reserveWithdrawal(ctx, claimed.ID, req.UserID, addr.Address, req.AmountLamports, feeEst.EstimatedFeeLamports, bal); err != nil {
		return SendResult{}, err
	}
	blockhash, lastValidBlockHeight, err := s.rpc.GetLatestBlockhash(ctx)
	if err != nil {
		return SendResult{}, err
	}
	rawTx, _, err := BuildSOLTransfer(key, req.ToAddress, blockhash, req.AmountLamports)
	if err != nil {
		return SendResult{}, err
	}
	signature, err := SignatureFromSignedTransaction(rawTx)
	if err != nil {
		return SendResult{}, err
	}
	if err := s.repo.persistSigned(ctx, claimed.ID, signature, rawTx, signerAddress, blockhash, lastValidBlockHeight); err != nil {
		return SendResult{}, err
	}
	rpcSignature, err := s.rpc.SendTransaction(ctx, rawTx)
	if err != nil {
		if errors.Is(err, ErrSubmitUnknown) {
			_ = s.repo.markSubmitStatus(ctx, signature, StatusSubmitUnknown)
			return SendResult{Signature: signature, AmountLamports: req.AmountLamports, FeeLamports: feeEst.EstimatedFeeLamports, Status: StatusSubmitUnknown}, nil
		}
		_ = s.repo.markSubmitStatus(ctx, signature, StatusFailedBeforeSubmit)
		return SendResult{}, err
	}
	if rpcSignature != "" && rpcSignature != signature {
		_ = s.repo.markSubmitStatus(ctx, signature, StatusManualReview)
		return SendResult{}, fmt.Errorf("solana: RPC retornou assinatura diferente")
	}
	if err := s.repo.markSubmitStatus(ctx, signature, StatusSubmitted); err != nil {
		return SendResult{}, err
	}
	return SendResult{Signature: signature, AmountLamports: req.AmountLamports, FeeLamports: feeEst.EstimatedFeeLamports, Status: StatusSubmitted}, nil
}

func (s *Service) ListUserTransactions(ctx context.Context, userID string, limit int) ([]Transaction, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	return s.repo.listUserTransactions(ctx, userID, limit)
}

func (s *Service) GetUserTransaction(ctx context.Context, userID, id string) (*Transaction, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	return s.repo.getUserTransaction(ctx, userID, id)
}

func (s *Service) SyncAddress(ctx context.Context, addr Address) ([]WorkerEvent, error) {
	signatures, err := s.rpc.GetSignaturesForAddress(ctx, addr.Address, "", 20)
	if err != nil {
		return nil, err
	}
	var events []WorkerEvent
	for i := len(signatures) - 1; i >= 0; i-- {
		info := signatures[i]
		if strings.TrimSpace(info.Signature) == "" || info.Err != nil {
			continue
		}
		receiveKey := info.Signature + ":balance_delta:" + addr.Address
		seen, err := s.repo.seenReceiveKey(ctx, receiveKey)
		if err != nil || seen {
			continue
		}
		txRaw, err := s.rpc.GetTransaction(ctx, info.Signature)
		if err != nil {
			continue
		}
		delta := solBalanceDelta(txRaw, addr.Address)
		if delta <= 0 {
			continue
		}
		status := StatusConfirmed
		confirmations := s.cfg.MinConfirmations
		tx := Transaction{
			UserID:             addr.UserID,
			Network:            Network,
			Signature:          info.Signature,
			Direction:          DirectionDeposit,
			AmountRaw:          lamportsString(delta),
			Decimals:           9,
			Status:             status,
			Confirmations:      confirmations,
			Slot:               info.Slot,
			DestinationAddress: addr.Address,
		}
		if err := s.repo.insertTransaction(ctx, tx, map[string]any{"address": addr.Address, "source": "scanner", "receive_key": receiveKey}); err != nil {
			continue
		}
		events = append(events, WorkerEvent{Type: "sol.deposit.confirmed", Payload: map[string]any{
			"user_id": addr.UserID, "address": addr.Address, "signature": info.Signature, "amount_lamports": delta, "network": Network, "asset": AssetSOL,
		}})
	}
	return events, nil
}

func (s *Service) TrackWithdrawals(ctx context.Context) ([]WorkerEvent, error) {
	txs, err := s.repo.pendingWithdrawals(ctx)
	if err != nil || len(txs) == 0 {
		return nil, err
	}
	sigs := make([]string, 0, len(txs))
	for _, tx := range txs {
		sigs = append(sigs, tx.Signature)
	}
	statuses, err := s.rpc.GetSignatureStatuses(ctx, sigs)
	if err != nil {
		return nil, err
	}
	var events []WorkerEvent
	for _, tx := range txs {
		status := statuses[tx.Signature]
		if status == StatusPending && (tx.Status == StatusSigned || tx.Status == StatusSubmitUnknown) {
			if tx.Status == StatusSigned {
				if err := s.rebroadcastSigned(ctx, tx); err != nil {
					_ = s.repo.updateTransactionStatus(ctx, tx.Signature, StatusManualReview, 0)
				}
			}
			continue
		}
		if status == "" || status == tx.Status {
			continue
		}
		confs := 0
		if status == StatusConfirmed || status == StatusFinalized {
			confs = s.cfg.MinConfirmations
		}
		if err := s.repo.updateTransactionStatus(ctx, tx.Signature, status, confs); err != nil {
			continue
		}
		if status == StatusConfirmed || status == StatusFinalized {
			events = append(events, WorkerEvent{Type: "sol.withdrawal.confirmed", Payload: map[string]any{
				"user_id": tx.UserID, "signature": tx.Signature, "amount_lamports": tx.AmountRaw, "network": Network, "asset": AssetSOL,
			}})
		}
	}
	return events, nil
}

func (s *Service) rebroadcastSigned(ctx context.Context, tx Transaction) error {
	if strings.TrimSpace(tx.SignedRawTx) == "" {
		return fmt.Errorf("solana: signed raw tx ausente")
	}
	rawTx, err := base64.StdEncoding.DecodeString(tx.SignedRawTx)
	if err != nil {
		return err
	}
	localSig, err := SignatureFromSignedTransaction(rawTx)
	if err != nil || localSig != tx.Signature {
		return fmt.Errorf("solana: signed raw tx corrupta")
	}
	if tx.LastValidBlockHeight > 0 {
		height, err := s.rpc.GetBlockHeight(ctx)
		if err != nil {
			return err
		}
		if height > tx.LastValidBlockHeight {
			return s.repo.updateTransactionStatus(ctx, tx.Signature, StatusRebuildRequired, 0)
		}
	}
	rpcSig, err := s.rpc.SendTransaction(ctx, rawTx)
	if err != nil {
		if errors.Is(err, ErrSubmitUnknown) {
			return s.repo.markSubmitStatus(ctx, tx.Signature, StatusSubmitUnknown)
		}
		return err
	}
	if rpcSig != "" && rpcSig != tx.Signature {
		return fmt.Errorf("solana: rebroadcast retornou assinatura diferente")
	}
	return s.repo.markSubmitStatus(ctx, tx.Signature, StatusSubmitted)
}

func (s *Service) ActiveAddresses(ctx context.Context) ([]Address, error) {
	return s.repo.listActiveAddresses(ctx)
}

func (s *Service) derivePrivateKey(userID string) (ed25519.PrivateKey, error) {
	return s.derivePrivateKeyForKeyID(userID, s.derivationKeyID())
}

func (s *Service) derivePrivateKeyForKeyID(userID, keyID string) (ed25519.PrivateKey, error) {
	keyring, err := s.derivationKeyring()
	if err != nil {
		return nil, err
	}
	return keyring.derivePrivateKey(userID, keyID)
}

func (s *Service) signingKeyForAddress(userID string, addr Address) (ed25519.PrivateKey, string, error) {
	keyID := strings.TrimSpace(addr.DerivationKeyID)
	key, err := s.derivePrivateKeyForKeyID(userID, keyID)
	if err != nil {
		slog.Warn("solana wallet key unavailable", "rail", "SOL", "wallet_key_id", keyID, "error_class", "wallet_key_unavailable")
		return nil, "", err
	}
	signerAddress := base58Encode(key.Public().(ed25519.PublicKey))
	if signerAddress != strings.TrimSpace(addr.Address) {
		slog.Warn("solana signer/address mismatch", "rail", "SOL", "wallet_key_id", keyID, "error_class", "signer_key_mismatch")
		return nil, "", ErrSignerKeyMismatch
	}
	return key, signerAddress, nil
}

func (s *Service) derivationKeyID() string {
	keyring, err := s.derivationKeyring()
	if err == nil && strings.TrimSpace(keyring.activeID) != "" {
		return keyring.activeID
	}
	return derivationKeyIDForSecret(s.cfg.DerivationSecret)
}

func (s *Service) derivationKeyring() (*derivationKeyring, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	if s.keyring != nil {
		return s.keyring, nil
	}
	keyring, err := newDerivationKeyring(
		s.cfg.ActiveDerivationKeyID,
		"",
		s.cfg.LegacyDerivationKeyID,
		s.cfg.DerivationSecret,
		false,
	)
	return keyring, err
}

func solBalanceDelta(tx map[string]any, address string) int64 {
	meta, _ := tx["meta"].(map[string]any)
	transaction, _ := tx["transaction"].(map[string]any)
	message, _ := transaction["message"].(map[string]any)
	keys, _ := message["accountKeys"].([]any)
	pre, _ := meta["preBalances"].([]any)
	post, _ := meta["postBalances"].([]any)
	for i, rawKey := range keys {
		key := accountKeyString(rawKey)
		if key != address || i >= len(pre) || i >= len(post) {
			continue
		}
		postLamports, okPost := numberInt64(post[i])
		preLamports, okPre := numberInt64(pre[i])
		if !okPost || !okPre {
			return 0
		}
		return postLamports - preLamports
	}
	return 0
}

func accountKeyString(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case map[string]any:
		if pubkey, _ := v["pubkey"].(string); pubkey != "" {
			return pubkey
		}
	}
	return ""
}

func numberInt64(raw any) (int64, bool) {
	switch v := raw.(type) {
	case float64:
		if v < 0 || v > float64(^uint64(0)>>1) || v != float64(int64(v)) {
			return 0, false
		}
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case json.Number:
		i, err := v.Int64()
		return i, err == nil
	}
	return 0, false
}

func solString(lamports int64) string {
	whole := lamports / LamportsPerSOL
	frac := lamports % LamportsPerSOL
	return strconv.FormatInt(whole, 10) + "." + fmt.Sprintf("%09d", frac)
}

func sha256Bytes(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
