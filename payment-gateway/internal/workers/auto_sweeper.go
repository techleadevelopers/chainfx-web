// Package workers — auto_sweeper.go
// AutoSweeperWorker monitors the USDT balance of the hot wallet via ERC-20
// balanceOf (eth_call). When the balance exceeds AutoSweeperHotMaxUsdt, it
// sweeps the excess to the cold wallet through the signer, leaving at least
// AutoSweeperHotMinUsdt as operational reserve.
package workers

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/httpclient"
	"payment-gateway/internal/metrics"
	"payment-gateway/internal/rpc"
	"payment-gateway/internal/security"
)

// AutoSweeperWorker polls the hot wallet USDT balance and sweeps excess funds
// to the cold wallet whenever the configured ceiling is reached.
type AutoSweeperWorker struct {
	cfg    *config.Config
	db     *database.DB
	pool   *rpc.Pool
	client *http.Client
}

// NewAutoSweeperWorker creates the worker. pool may be nil — the worker will
// self-disable gracefully if the RPC pool is unavailable.
func NewAutoSweeperWorker(cfg *config.Config, db *database.DB, pool *rpc.Pool) *AutoSweeperWorker {
	return &AutoSweeperWorker{
		cfg:    cfg,
		db:     db,
		pool:   pool,
		client: httpclient.Default(),
	}
}

// Start runs the sweeper on a ticker. Blocks until ctx is cancelled.
func (w *AutoSweeperWorker) Start(ctx context.Context) {
	if !w.cfg.AutoSweeperEnabled {
		slog.Info("AutoSweeperWorker: disabled via config (AUTO_SWEEPER_ENABLED=false)")
		<-ctx.Done()
		return
	}

	interval := time.Duration(w.cfg.AutoSweeperIntervalSec) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}

	slog.Info("AutoSweeperWorker: started",
		"interval", interval,
		"hot_max_usdt", w.cfg.AutoSweeperHotMaxUsdt,
		"hot_min_usdt", w.cfg.AutoSweeperHotMinUsdt,
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("AutoSweeperWorker: shutting down")
			return
		case <-ticker.C:
			w.executeSweep(ctx)
		}
	}
}

// executeSweep performs one balance check + optional sweep cycle.
func (w *AutoSweeperWorker) executeSweep(ctx context.Context) {
	metrics.IncAutoSweeperRun()

	run := database.AutoSweeperRun{
		Network:    "BSC",
		HotWallet:  w.cfg.TreasuryHot,
		ColdWallet: w.cfg.TreasuryCold,
		Status:     "skipped",
	}

	// Guard: require essential config.
	if w.cfg.TreasuryHot == "" || w.cfg.TreasuryCold == "" {
		slog.Warn("AutoSweeperWorker: TREASURY_HOT or TREASURY_COLD not set, skipping")
		run.Status = "error"
		errMsg := "TREASURY_HOT or TREASURY_COLD not configured"
		run.ErrorMsg = &errMsg
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		metrics.IncAutoSweeperError()
		return
	}
	if w.pool == nil || w.cfg.BscUsdtContract == "" {
		slog.Warn("AutoSweeperWorker: RPC pool or USDT contract not configured, skipping")
		return
	}

	// 1. Read hot wallet USDT balance via balanceOf eth_call.
	balance, err := w.balanceOf(ctx, w.cfg.TreasuryHot, w.cfg.BscUsdtContract)
	if err != nil {
		slog.Error("AutoSweeperWorker: balanceOf failed", "error", err)
		run.Status = "error"
		errMsg := err.Error()
		run.ErrorMsg = &errMsg
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		metrics.IncAutoSweeperError()
		return
	}

	// Convert from token base units (USDT has 6 decimals on BSC).
	const usdtDecimals = 6
	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(usdtDecimals), nil))
	balanceFloat, _ := new(big.Float).Quo(new(big.Float).SetInt(balance), divisor).Float64()
	run.BalanceUSDT = balanceFloat

	slog.Info("AutoSweeperWorker: hot wallet balance",
		"usdt", balanceFloat,
		"max_usdt", w.cfg.AutoSweeperHotMaxUsdt,
	)

	// 2. If below ceiling, nothing to do.
	if balanceFloat <= w.cfg.AutoSweeperHotMaxUsdt {
		run.Status = "skipped"
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		return
	}

	// 3. Compute sweep amount: excess above the minimum reserve.
	sweepAmount := balanceFloat - w.cfg.AutoSweeperHotMinUsdt
	if sweepAmount <= 0 {
		slog.Warn("AutoSweeperWorker: sweep amount would be zero or negative, skipping",
			"balance", balanceFloat,
			"min_reserve", w.cfg.AutoSweeperHotMinUsdt,
		)
		run.Status = "skipped"
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		return
	}

	slog.Info("AutoSweeperWorker: initiating sweep to cold wallet",
		"sweep_usdt", sweepAmount,
		"cold_wallet", w.cfg.TreasuryCold,
	)

	blockNumber, err := w.pool.BlockNumber(ctx)
	if err != nil {
		slog.Error("AutoSweeperWorker: failed to read block number for idempotency", "error", err)
		run.Status = "error"
		errMsg := err.Error()
		run.ErrorMsg = &errMsg
		run.SweptUSDT = 0
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		metrics.IncAutoSweeperError()
		return
	}

	// 4. Dispatch sweep via signer.
	txHash, err := w.dispatchSweep(ctx, sweepAmount, blockNumber)
	if err != nil {
		slog.Error("AutoSweeperWorker: sweep dispatch failed", "error", err)
		run.Status = "error"
		errMsg := err.Error()
		run.ErrorMsg = &errMsg
		run.SweptUSDT = 0
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		metrics.IncAutoSweeperError()
		return
	}

	run.Status = "ok"
	run.SweptUSDT = sweepAmount
	run.TxHash = &txHash
	_ = w.db.RecordAutoSweeperRun(ctx, run)
	metrics.IncAutoSweeperSwept(sweepAmount)

	slog.Info("AutoSweeperWorker: sweep completed",
		"swept_usdt", sweepAmount,
		"tx_hash", txHash,
	)
}

// balanceOf calls ERC-20 balanceOf(address) via eth_call and returns the raw token units.
func (w *AutoSweeperWorker) balanceOf(ctx context.Context, wallet, tokenContract string) (*big.Int, error) {
	// Encode: balanceOf(address) selector = 0x70a08231
	// Argument: 32 bytes, left-padded address
	walletAddr := common.HexToAddress(wallet)
	var callData [36]byte
	sel, _ := hex.DecodeString("70a08231")
	copy(callData[:4], sel)
	copy(callData[16:], walletAddr.Bytes()) // 12 zero pad + 20 addr bytes

	contractAddr := common.HexToAddress(tokenContract)

	var result []byte
	err := w.pool.Do(ctx, func(c *ethclient.Client) error {
		msg := map[string]string{
			"to":   contractAddr.Hex(),
			"data": "0x" + hex.EncodeToString(callData[:]),
		}
		// Use low-level RPC call for eth_call
		var raw string
		err := c.Client().CallContext(ctx, &raw, "eth_call", msg, "latest")
		if err != nil {
			return err
		}
		raw = strings.TrimPrefix(raw, "0x")
		if raw == "" {
			result = big.NewInt(0).Bytes()
			return nil
		}
		decoded, err := hex.DecodeString(raw)
		if err != nil {
			return fmt.Errorf("decode balanceOf response: %w", err)
		}
		result = decoded
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(result), nil
}

// sweepPayload matches the existing signer /hd/transfer contract.
type sweepPayload struct {
	DerivationIndex int    `json:"derivationIndex"`
	To              string `json:"to"`
	Amount          string `json:"amount"`
	TokenContract   string `json:"tokenContract"`
	Network         string `json:"network"`
	IdempotencyKey  string `json:"idempotencyKey"`
}

// dispatchSweep calls the signer to transfer sweepAmount USDT to the cold wallet.
func (w *AutoSweeperWorker) dispatchSweep(ctx context.Context, amount float64, blockNumber uint64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	hotWallet := strings.ToLower(strings.TrimSpace(w.cfg.TreasuryHot))
	idempotencyKey := fmt.Sprintf("sweep-%s-%d", hotWallet, blockNumber)
	payload := sweepPayload{
		DerivationIndex: 0,
		To:              w.cfg.TreasuryCold,
		Amount:          fmt.Sprintf("%.6f", amount),
		TokenContract:   w.cfg.BscUsdtContract,
		Network:         "BSC",
		IdempotencyKey:  idempotencyKey,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal sweep payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.SignerUrl+"/hd/transfer", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build sweep request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, w.cfg.SignerHmacSecret, body)

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sweep signer call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return "", fmt.Errorf("signer returned %d: %s", resp.StatusCode, errBody.Error)
	}

	var result struct {
		TxHash string `json:"txHash"`
	}
	txHash := idempotencyKey // fallback
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.TxHash != "" {
		txHash = result.TxHash
	}
	return txHash, nil
}
