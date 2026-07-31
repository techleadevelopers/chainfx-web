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
	"payment-gateway/internal/treasury"
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

	const usdtDecimals = 6
	balanceFloat := rawTokenToFloat(balance, usdtDecimals)
	run.BalanceUSDT = balanceFloat

	slog.Info("AutoSweeperWorker: hot wallet balance",
		"usdt", balanceFloat,
		"max_usdt", w.cfg.AutoSweeperHotMaxUsdt,
	)

	maxRaw, err := decimalUSDTToRaw(fmt.Sprintf("%.6f", w.cfg.AutoSweeperHotMaxUsdt))
	if err != nil {
		w.recordSweepError(ctx, run, fmt.Errorf("AUTO_SWEEPER_HOT_MAX_USDT invalido: %w", err))
		return
	}
	minRaw, err := decimalUSDTToRaw(fmt.Sprintf("%.6f", w.cfg.AutoSweeperHotMinUsdt))
	if err != nil {
		w.recordSweepError(ctx, run, fmt.Errorf("AUTO_SWEEPER_HOT_MIN_USDT invalido: %w", err))
		return
	}
	if !common.IsHexAddress(w.cfg.TreasuryHot) || !common.IsHexAddress(w.cfg.TreasuryCold) {
		w.recordSweepError(ctx, run, fmt.Errorf("treasury hot/cold EVM invalida"))
		return
	}
	token := common.HexToAddress(w.cfg.BscUsdtContract).Hex()
	if token == (common.Address{}).Hex() {
		w.recordSweepError(ctx, run, fmt.Errorf("BSC_USDT_CONTRACT invalido"))
		return
	}
	if strings.EqualFold(w.cfg.TreasuryHot, w.cfg.TreasuryCold) {
		w.recordSweepError(ctx, run, fmt.Errorf("TREASURY_HOT e TREASURY_COLD nao podem ser iguais"))
		return
	}
	if balance.Cmp(maxRaw) <= 0 {
		run.Status = "skipped"
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		return
	}

	sweepRaw := new(big.Int).Sub(balance, minRaw)
	if sweepRaw.Sign() <= 0 {
		slog.Warn("AutoSweeperWorker: sweep amount would be zero or negative, skipping",
			"balance", balanceFloat,
			"min_reserve", w.cfg.AutoSweeperHotMinUsdt,
		)
		run.Status = "skipped"
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		return
	}
	sweepAmount := rawTokenToFloat(sweepRaw, usdtDecimals)

	gasCostUSD, err := w.estimateSweepGasUSD(ctx)
	if err != nil {
		w.recordSweepError(ctx, run, fmt.Errorf("profitability gas estimate failed: %w", err))
		return
	}
	netUSD := sweepAmount - gasCostUSD - w.cfg.AutoSweeperProviderUSD - w.cfg.AutoSweeperRelayUSD - w.cfg.AutoSweeperBufferUSD
	if netUSD < w.cfg.AutoSweeperMinNetUSD {
		slog.Warn("AutoSweeperWorker: sweep skipped by profitability policy",
			"gross_value_usd", sweepAmount,
			"estimated_gas_usd", gasCostUSD,
			"provider_cost_usd", w.cfg.AutoSweeperProviderUSD,
			"relay_cost_usd", w.cfg.AutoSweeperRelayUSD,
			"operational_buffer_usd", w.cfg.AutoSweeperBufferUSD,
			"net_sweep_value_usd", netUSD,
			"min_net_sweep_value_usd", w.cfg.AutoSweeperMinNetUSD,
		)
		run.Status = "skipped"
		_ = w.db.RecordAutoSweeperRun(ctx, run)
		return
	}

	slog.Info("AutoSweeperWorker: initiating sweep to cold wallet",
		"sweep_usdt", sweepAmount,
		"cold_wallet", w.cfg.TreasuryCold,
	)
	rail := treasury.ResolveRail(w.cfg, "BSC", treasury.StateCreated, false)
	if rail.CurrentRail == treasury.RailDelegate7702 {
		slog.Warn("AutoSweeperWorker: 7702 delegate gate ready; using legacy fallback until delegate executor/reconciliation is explicitly enabled",
			"network", rail.Network,
			"delegate", rail.DelegateAddress,
			"fallback", rail.Fallback,
		)
	}

	// 4. Dispatch sweep via signer.
	operationID := autoSweepOperationID(w.cfg.TreasuryHot, w.cfg.TreasuryCold, token, "BSC", sweepRaw)
	dispatch, err := w.dispatchSweep(ctx, sweepRaw, operationID)
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

	run.Status = autoSweeperRunStatusFromSigner(dispatch.Status)
	run.SweptUSDT = sweepAmount
	run.TxHash = &dispatch.TxHash
	run.OperationID = operationID
	run.ChainID = dispatch.ChainID
	run.TokenContract = token
	run.AmountRaw = sweepRaw.String()
	run.SignerStatus = dispatch.Status
	run.Nonce = dispatch.Nonce
	_ = w.db.RecordAutoSweeperRun(ctx, run)
	if run.Status == "broadcast" || run.Status == "confirmed" {
		metrics.IncAutoSweeperSwept(sweepAmount)
	}

	slog.Info("AutoSweeperWorker: sweep dispatched",
		"swept_usdt", sweepAmount,
		"tx_hash", dispatch.TxHash,
		"status", run.Status,
	)
}

func (w *AutoSweeperWorker) recordSweepError(ctx context.Context, run database.AutoSweeperRun, err error) {
	slog.Error("AutoSweeperWorker: hardening guard failed", "error", err)
	run.Status = "error"
	errMsg := err.Error()
	run.ErrorMsg = &errMsg
	_ = w.db.RecordAutoSweeperRun(ctx, run)
	metrics.IncAutoSweeperError()
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
	To             string `json:"to"`
	Amount         string `json:"amount"`
	TokenContract  string `json:"tokenContract"`
	Network        string `json:"network"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type sweepDispatchResult struct {
	TxHash      string
	Status      string
	OperationID string
	Nonce       uint64
	ChainID     uint64
}

// dispatchSweep calls the signer to transfer sweepAmount USDT to the cold wallet.
func (w *AutoSweeperWorker) dispatchSweep(ctx context.Context, amountRaw *big.Int, operationID string) (sweepDispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	payload := sweepPayload{
		To:             common.HexToAddress(w.cfg.TreasuryCold).Hex(),
		Amount:         rawUSDTToDecimal(amountRaw),
		TokenContract:  w.cfg.BscUsdtContract,
		Network:        "BSC",
		IdempotencyKey: operationID,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return sweepDispatchResult{}, fmt.Errorf("marshal sweep payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.SignerUrl+"/hd/transfer", bytes.NewReader(body))
	if err != nil {
		return sweepDispatchResult{}, fmt.Errorf("build sweep request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, w.cfg.SignerHmacSecret, body)

	resp, err := w.client.Do(req)
	if err != nil {
		return sweepDispatchResult{}, fmt.Errorf("sweep signer call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return sweepDispatchResult{}, fmt.Errorf("signer returned %d: %s", resp.StatusCode, errBody.Error)
	}

	var result struct {
		TxHash      string `json:"txHash"`
		Status      string `json:"status"`
		OperationID string `json:"operationId"`
		Nonce       uint64 `json:"nonce"`
		ChainID     uint64 `json:"chainId"`
	}
	txHash := operationID // fallback
	if err := json.NewDecoder(resp.Body).Decode(&result); err == nil && result.TxHash != "" {
		txHash = result.TxHash
	}
	result.TxHash = txHash
	if strings.TrimSpace(result.OperationID) == "" {
		result.OperationID = operationID
	}
	return sweepDispatchResult{
		TxHash:      result.TxHash,
		Status:      result.Status,
		OperationID: result.OperationID,
		Nonce:       result.Nonce,
		ChainID:     result.ChainID,
	}, nil
}

func autoSweeperRunStatusFromSigner(status string) string {
	switch strings.TrimSpace(status) {
	case "confirmed":
		return "confirmed"
	case "broadcast", "submitted":
		return "broadcast"
	case "broadcast_unknown", "signed", "manual_review":
		return strings.TrimSpace(status)
	default:
		return "broadcast_unknown"
	}
}

func autoSweepOperationID(hot, cold, token, network string, amountRaw *big.Int) string {
	parts := strings.Join([]string{
		"autosweep",
		strings.ToLower(common.HexToAddress(hot).Hex()),
		strings.ToLower(common.HexToAddress(cold).Hex()),
		strings.ToLower(common.HexToAddress(token).Hex()),
		strings.ToUpper(strings.TrimSpace(network)),
		amountRaw.String(),
	}, ":")
	return parts
}

func decimalUSDTToRaw(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, fmt.Errorf("amount invalido")
	}
	whole := new(big.Int)
	if _, ok := whole.SetString(parts[0], 10); !ok {
		return nil, fmt.Errorf("amount invalido")
	}
	whole.Mul(whole, big.NewInt(1_000_000))
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 6 {
			frac = frac[:6]
		}
		for len(frac) < 6 {
			frac += "0"
		}
		f := new(big.Int)
		if _, ok := f.SetString(frac, 10); !ok {
			return nil, fmt.Errorf("amount invalido")
		}
		whole.Add(whole, f)
	}
	return whole, nil
}

func rawUSDTToDecimal(raw *big.Int) string {
	if raw == nil {
		return "0.000000"
	}
	intPart := new(big.Int)
	fracPart := new(big.Int)
	intPart.DivMod(new(big.Int).Set(raw), big.NewInt(1_000_000), fracPart)
	return fmt.Sprintf("%s.%06d", intPart.String(), fracPart.Int64())
}

func rawTokenToFloat(raw *big.Int, decimals int) float64 {
	if raw == nil {
		return 0
	}
	scale := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	out, _ := new(big.Float).Quo(new(big.Float).SetInt(raw), scale).Float64()
	return out
}

func (w *AutoSweeperWorker) estimateSweepGasUSD(ctx context.Context) (float64, error) {
	if w.cfg.AutoSweeperGasCostUSD > 0 {
		return w.cfg.AutoSweeperGasCostUSD, nil
	}
	if w.cfg.AutoSweeperNativeUSD <= 0 {
		return 0, fmt.Errorf("configure AUTO_SWEEPER_NATIVE_USD ou AUTO_SWEEPER_ESTIMATED_GAS_USD")
	}
	if w.pool == nil {
		return 0, fmt.Errorf("RPC pool ausente")
	}
	gasLimit := w.cfg.AutoSweeperGasLimit
	if gasLimit == 0 {
		return 0, fmt.Errorf("AUTO_SWEEPER_GAS_LIMIT invalido")
	}
	var gasPrice *big.Int
	err := w.pool.Do(ctx, func(c *ethclient.Client) error {
		var err error
		gasPrice, err = c.SuggestGasPrice(ctx)
		return err
	})
	if err != nil {
		return 0, err
	}
	wei := new(big.Int).Mul(gasPrice, new(big.Int).SetUint64(gasLimit))
	native, _ := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18)).Float64()
	return native * w.cfg.AutoSweeperNativeUSD, nil
}
