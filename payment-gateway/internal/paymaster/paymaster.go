// Package paymaster implements the Gas Station engine.
// paymaster.go — main service: Quote, SubmitRelay, relay worker loop.
package paymaster

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/rpc"
)

// Service is the top-level Gas Station orchestrator.
type Service struct {
	cfg       *config.Config
	db        *database.DB
	pool      *rpc.Pool
	oracle    *PriceOracle
	estimator *Estimator
	sigLock   SigLockStore
	batcher   *RelayBatcher
	relayer   *TokenRelayer // spread-capture engine; nil when not configured
	stopCh    chan struct{}
}

// NewService constructs a ready-to-use Paymaster Service.
// Call Start(ctx) to begin background workers.
func NewService(cfg *config.Config, db *database.DB, pool *rpc.Pool) *Service {
	stopCh := make(chan struct{})
	oracle := NewPriceOracle()
	estimator := NewEstimator(
		pool,
		oracle,
		cfg.GasStationSurchargeBps,
		cfg.GasStationMaxFeeUsdt,
		cfg.GasStationMinFeeUsdt,
	)
	sigLock := NewDBSigLock(db)

	// Build the spread-capture TokenRelayer (pure arithmetic engine — no private key).
	relayer := NewTokenRelayer(cfg.PaymasterRelaySpreadBps, cfg.TreasuryHot)
	slog.Info("paymaster: TokenRelayer ready",
		"spread_bps", relayer.SpreadBps(),
		"fee_destination", relayer.FeeDestination(),
		"fee_leg_active", relayer.HasFeeDestination(),
	)

	svc := &Service{
		cfg:       cfg,
		db:        db,
		pool:      pool,
		oracle:    oracle,
		estimator: estimator,
		sigLock:   sigLock,
		relayer:   relayer,
		stopCh:    stopCh,
	}

	svc.batcher = NewRelayBatcher(
		cfg.SignerUrl,
		cfg.SignerHmacSecret,
		cfg.PaymasterMulticallContract,
		relayer,
		svc.markDLQ,
	)

	return svc
}

// Start launches the relay batcher and the fallback poller.
// Blocks until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	slog.Info("paymaster: service starting")

	// Fallback poller: picks up relays that missed the batcher (crash recovery).
	go s.fallbackPoller(ctx)

	// Batcher blocks until ctx is cancelled.
	s.batcher.Start(ctx)

	close(s.stopCh)
	slog.Info("paymaster: service stopped")
}

// ── Public API ─────────────────────────────────────────────────────────────────

// GasQuote is returned by Quote().
type GasQuote struct {
	FeeUSDT      float64 `json:"fee_usdt"`
	GasPriceGwei float64 `json:"gas_price_gwei"`
	GasLimit     uint64  `json:"gas_limit"`
	NativeSymbol string  `json:"native_symbol"`
	ValidUntilMs int64   `json:"valid_until_ms"` // unix ms — client should re-quote after this
}

// Quote estimates the USDT fee to relay a transaction from userAddr to txTo.
func (s *Service) Quote(ctx context.Context, userAddr, txTo string, txData []byte) (*GasQuote, error) {
	if !s.cfg.GasStationEnabled {
		return nil, fmt.Errorf("gas station is disabled")
	}

	from := common.HexToAddress(userAddr)
	to := common.HexToAddress(txTo)

	est, err := s.estimator.EstimateRelay(ctx, from, to, txData)
	if err != nil {
		return nil, err
	}

	gasPriceGwei, _ := weiToGwei(est.GasPriceWei)

	return &GasQuote{
		FeeUSDT:      est.FeeUSDTFloat(),
		GasPriceGwei: gasPriceGwei,
		GasLimit:     est.GasLimit,
		NativeSymbol: est.NativeSymbol,
		ValidUntilMs: time.Now().Add(55 * time.Second).UnixMilli(),
	}, nil
}

// RelayRequest is the inbound request to POST /v1/gas/relay.
type RelayRequest struct {
	UserAddress string `json:"user_address"`
	TxTo        string `json:"tx_to"`
	TxData      string `json:"tx_data"` // hex-encoded, may be empty
	SigR        string `json:"sig_r"`
	SigS        string `json:"sig_s"`
	SigV        string `json:"sig_v"`
	Amount      string `json:"amount"`     // USDT amount being moved
	TokenAddr   string `json:"token_addr"` // ERC-20 contract
	Network     string `json:"network"`    // "BSC" | "POLYGON"
}

// RelayResponse is returned by SubmitRelay.
type RelayResponse struct {
	RelayID string  `json:"relay_id"`
	Status  string  `json:"status"`
	FeeUSDT float64 `json:"fee_usdt"`
}

// SubmitRelay validates the request, stores it durably, and enqueues it.
func (s *Service) SubmitRelay(ctx context.Context, req *RelayRequest) (*RelayResponse, error) {
	if !s.cfg.GasStationEnabled {
		return nil, fmt.Errorf("gas station is disabled")
	}

	if err := validateRelaySignature(req.SigR, req.SigS, req.SigV); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNonRetryable, err)
	}

	// 1. Derive replay hash from the signature components. This is an
	// anti-replay/idempotency guard for the custodial relay path; it is not an
	// EIP-2612 permit validation.
	sigHash, err := PermitSigHash(req.SigR, req.SigS)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid sig components: %v", ErrNonRetryable, err)
	}

	// 2. Acquire lock — rejects duplicate signatures atomically.
	ok, err := s.sigLock.AcquireLock(sigHash)
	if !ok {
		return nil, ErrDuplicateSig
	}
	if err != nil {
		return nil, err
	}

	// 3. Estimate fee on-chain.
	from := common.HexToAddress(req.UserAddress)
	to := common.HexToAddress(req.TxTo)
	var txDataBytes []byte
	if req.TxData != "" {
		txDataBytes, _ = hexDecodeOptional(req.TxData)
	}

	est, err := s.estimator.EstimateRelay(ctx, from, to, txDataBytes)
	if err != nil {
		s.sigLock.ReleaseLock(sigHash) // allow retry on estimation failure
		return nil, fmt.Errorf("fee estimation failed: %w", err)
	}

	gasPriceGwei, _ := weiToGwei(est.GasPriceWei)

	// 4. Persist to DB for durability before any on-chain action.
	relayID, err := s.db.CreateGasRelayRequest(ctx, database.CreateGasRelayParams{
		UserAddress:  req.UserAddress,
		SigR:         req.SigR,
		SigS:         req.SigS,
		SigHash:      sigHash,
		TxTo:         req.TxTo,
		TxData:       req.TxData,
		FeeUSDT:      est.FeeUSDTFloat(),
		GasPriceGwei: gasPriceGwei,
		GasLimit:     int64(est.GasLimit),
	})
	if err != nil {
		s.sigLock.ReleaseLock(sigHash)
		return nil, fmt.Errorf("persist relay: %w", err)
	}

	// 5. Enqueue to batcher for async dispatch.
	network := req.Network
	if network == "" {
		network = "BSC"
	}
	job := relayJob{
		relayID: relayID,
		to:      req.TxTo,
		amount:  req.Amount,
		token:   req.TokenAddr,
		network: network,
	}
	if !s.batcher.Enqueue(job) {
		slog.Warn("paymaster: batcher queue full, relay will be picked up by fallback poller",
			"relay_id", relayID,
		)
	}

	metrics.IncPaymasterRelay()

	return &RelayResponse{
		RelayID: relayID,
		Status:  "pending",
		FeeUSDT: est.FeeUSDTFloat(),
	}, nil
}

func validateRelaySignature(r, s, v string) error {
	if !isRelaySigHex(r, 32) || !isRelaySigHex(s, 32) {
		return fmt.Errorf("sig_r and sig_s must be 32-byte hex values")
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "1", "27", "28", "0x0", "0x1", "0x1b", "0x1c":
		return nil
	default:
		return fmt.Errorf("sig_v must be 0, 1, 27, or 28")
	}
}

func isRelaySigHex(value string, wantBytes int) bool {
	raw := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	if len(raw) != wantBytes*2 {
		return false
	}
	_, err := hexDecodeOptional(raw)
	return err == nil
}

// GetRelay returns the current state of a relay request.
func (s *Service) GetRelay(ctx context.Context, id string) (*database.GasRelayRequest, error) {
	return s.db.GetGasRelayRequest(ctx, id)
}

// Stats returns admin stats for the gas station dashboard.
func (s *Service) Stats(ctx context.Context) (map[string]any, error) {
	return s.db.GasRelayStats(ctx)
}

// ── Internal ───────────────────────────────────────────────────────────────────

// fallbackPoller picks up any relays that are pending but not in the batcher
// (e.g., after a restart). Runs every 5 seconds.
func (s *Service) fallbackPoller(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollPendingRelays(ctx)
		}
	}
}

func (s *Service) pollPendingRelays(ctx context.Context) {
	relays, err := s.db.ListRetryableRelays(ctx)
	if err != nil {
		slog.Error("paymaster: fallback poller list error", "error", err)
		return
	}
	for _, r := range relays {
		job := relayJob{
			relayID: r.ID,
			to:      r.TxTo,
			amount:  "0", // amount is embedded in tx_data; signer resolves it
			token:   s.cfg.BscUsdtContract,
			network: "BSC",
		}
		s.batcher.Enqueue(job)
	}
}

// markDLQ is called by the batcher when all retries are exhausted.
func (s *Service) markDLQ(ctx context.Context, id, errMsg string) {
	if err := s.db.MarkRelayDLQ(ctx, id, errMsg); err != nil {
		slog.Error("paymaster: failed to mark relay as DLQ", "relay_id", id, "error", err)
	}
	metrics.IncPaymasterRelayError()
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// weiToGwei converts a *big.Int wei value to a float64 Gwei value.
// 1 Gwei = 1e9 wei.
func weiToGwei(wei *big.Int) (float64, error) {
	if wei == nil {
		return 0, nil
	}
	gweiBig := new(big.Float).Quo(
		new(big.Float).SetInt(wei),
		new(big.Float).SetInt64(1_000_000_000),
	)
	f, _ := gweiBig.Float64()
	return f, nil
}

func hexDecodeOptional(s string) ([]byte, error) {
	if len(s) >= 2 && s[:2] == "0x" {
		s = s[2:]
	}
	if s == "" {
		return nil, nil
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s)-1; i += 2 {
		var v byte
		fmt.Sscanf(s[i:i+2], "%02x", &v)
		b[i/2] = v
	}
	return b, nil
}

// IsEnabled reports whether the Gas Station feature is active.
func (s *Service) IsEnabled() bool {
	return s.cfg.GasStationEnabled
}

// StatusJSON returns a JSON-encodable status snapshot.
func (s *Service) StatusJSON(ctx context.Context) map[string]any {
	bnbPrice, bnbErr := s.oracle.BNBPrice(ctx)
	status := map[string]any{
		"enabled":       s.cfg.GasStationEnabled,
		"surcharge_bps": s.cfg.GasStationSurchargeBps,
		"max_fee_usdt":  s.cfg.GasStationMaxFeeUsdt,
		"min_fee_usdt":  s.cfg.GasStationMinFeeUsdt,
	}
	if bnbErr == nil {
		status["bnb_usd"] = bnbPrice
	}
	return status
}

// Ensure *Service satisfies the interface used by the admin handler.
var _ json.Marshaler = (*statusJSON)(nil)

type statusJSON struct{ m map[string]any }

func (s statusJSON) MarshalJSON() ([]byte, error) { return json.Marshal(s.m) }

// HTTPStatus returns the HTTP status code for the Gas Station.
func (s *Service) HTTPStatus() int {
	if s.cfg.GasStationEnabled {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}
