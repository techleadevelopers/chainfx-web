package solana

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	Enabled            bool
	RPCURLs            string
	Cluster            string
	WithdrawalsEnabled bool
	ScanInterval       time.Duration
	TxScanInterval     time.Duration
	MinConfirmations   int
	MaxSendLamports    int64
	DerivationSecret   string
}

type Service struct {
	cfg  Config
	rpc  *RPCClient
	repo *repository
}

func NewService(db *database.DB, cfg *config.Config) (*Service, error) {
	if db == nil || db.SQL == nil || cfg == nil || !cfg.SolanaEnabled || strings.TrimSpace(cfg.SolanaRpcUrls) == "" {
		return nil, nil
	}
	secret := strings.TrimSpace(cfg.SignerHmacSecret)
	if secret == "" {
		secret = strings.TrimSpace(cfg.LGPDSecret)
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
			Enabled:            true,
			RPCURLs:            cfg.SolanaRpcUrls,
			Cluster:            firstNonEmpty(strings.TrimSpace(cfg.SolanaCluster), "mainnet"),
			WithdrawalsEnabled: cfg.SolanaWithdrawalsEnabled,
			ScanInterval:       scan,
			TxScanInterval:     txScan,
			MinConfirmations:   maxInt(cfg.SolanaMinConfirmations, 1),
			MaxSendLamports:    cfg.SolanaMaxSendLamports,
			DerivationSecret:   secret,
		},
		rpc:  NewRPCClient(cfg.SolanaRpcUrls),
		repo: &repository{sql: db.SQL},
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
	key, err := s.derivePrivateKey(userID)
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
	existing, existingHash, err := s.repo.transactionByIdempotency(ctx, req.UserID, req.IdempotencyKey)
	if err != nil && err != sql.ErrNoRows {
		return SendResult{}, err
	}
	if existing != nil {
		if existingHash != "" && existingHash != req.RequestHash {
			return SendResult{}, ErrIdempotencyConflict
		}
		return SendResult{Signature: existing.Signature, AmountLamports: parseAmountRaw(existing.AmountRaw), Status: existing.Status}, nil
	}
	addr, err := s.GetOrCreateAddress(ctx, req.UserID)
	if err != nil {
		return SendResult{}, err
	}
	bal, err := s.rpc.GetBalance(ctx, addr.Address)
	if err != nil {
		return SendResult{}, err
	}
	feeEst, _ := s.EstimateFee(ctx, req.UserID, req.ToAddress, req.AmountLamports)
	if bal < req.AmountLamports+feeEst.EstimatedFeeLamports {
		return SendResult{}, ErrInsufficientFunds
	}
	key, err := s.derivePrivateKey(req.UserID)
	if err != nil {
		return SendResult{}, err
	}
	blockhash, _, err := s.rpc.GetLatestBlockhash(ctx)
	if err != nil {
		return SendResult{}, err
	}
	rawTx, _, err := BuildSOLTransfer(key, req.ToAddress, blockhash, req.AmountLamports)
	if err != nil {
		return SendResult{}, err
	}
	signature, err := s.rpc.SendTransaction(ctx, rawTx)
	if err != nil {
		return SendResult{}, err
	}
	tx := Transaction{
		UserID:        req.UserID,
		Network:       Network,
		Signature:     signature,
		Direction:     DirectionWithdrawal,
		AmountRaw:     lamportsString(req.AmountLamports),
		Decimals:      9,
		Status:        StatusBroadcast,
		Confirmations: 0,
	}
	_ = s.repo.insertTransaction(ctx, tx, map[string]any{
		"from":            addr.Address,
		"to":              req.ToAddress,
		"idempotency_key": req.IdempotencyKey,
		"request_hash":    req.RequestHash,
		"fee_lamports":    feeEst.EstimatedFeeLamports,
	})
	return SendResult{Signature: signature, AmountLamports: req.AmountLamports, FeeLamports: feeEst.EstimatedFeeLamports, Status: StatusBroadcast}, nil
}

func (s *Service) ListUserTransactions(ctx context.Context, userID string, limit int) ([]Transaction, error) {
	if s == nil {
		return nil, ErrDisabled
	}
	return s.repo.listUserTransactions(ctx, userID, limit)
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
		seen, err := s.repo.seenSignature(ctx, info.Signature)
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
			UserID:        addr.UserID,
			Network:       Network,
			Signature:     info.Signature,
			Direction:     DirectionDeposit,
			AmountRaw:     lamportsString(delta),
			Decimals:      9,
			Status:        status,
			Confirmations: confirmations,
			Slot:          info.Slot,
		}
		if err := s.repo.insertTransaction(ctx, tx, map[string]any{"address": addr.Address, "source": "scanner"}); err != nil {
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
		if status == "" || status == tx.Status {
			continue
		}
		confs := 0
		if status == StatusConfirmed {
			confs = s.cfg.MinConfirmations
		}
		if err := s.repo.updateTransactionStatus(ctx, tx.Signature, status, confs); err != nil {
			continue
		}
		if status == StatusConfirmed {
			events = append(events, WorkerEvent{Type: "sol.withdrawal.confirmed", Payload: map[string]any{
				"user_id": tx.UserID, "signature": tx.Signature, "amount_lamports": tx.AmountRaw, "network": Network, "asset": AssetSOL,
			}})
		}
	}
	return events, nil
}

func (s *Service) ActiveAddresses(ctx context.Context) ([]Address, error) {
	return s.repo.listActiveAddresses(ctx)
}

func (s *Service) derivePrivateKey(userID string) (ed25519.PrivateKey, error) {
	secret := []byte(strings.TrimSpace(s.cfg.DerivationSecret))
	if len(secret) < 32 {
		return nil, ErrSigningNotConfigured
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("chainfx-solana-wallet-v1:" + strings.TrimSpace(userID)))
	seed := mac.Sum(nil)
	return ed25519.NewKeyFromSeed(seed[:32]), nil
}

func (s *Service) derivationKeyID() string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(s.cfg.DerivationSecret)))
	return "hmac-sha256:" + hex.EncodeToString(sum[:8])
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
		return int64(numberFloat(post[i]) - numberFloat(pre[i]))
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

func numberFloat(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
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
