package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/eip712"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

type eipProbeRunner struct {
	cfg *config.Config
	db  *database.DB

	mu        sync.Mutex
	relayerMu sync.Mutex
	history   []eipProbeResult
	wallets   []eipProbeWallet
}

type eipProbeWallet struct {
	ID               string `json:"id"`
	Address          string `json:"address"`
	Source           string `json:"source"`
	Status           string `json:"status"`
	NativeBalanceWei string `json:"nativeBalanceWei,omitempty"`
	TokenBalanceRaw  string `json:"tokenBalanceRaw,omitempty"`
	TokenSymbol      string `json:"tokenSymbol,omitempty"`
}

type eipProbeRunRequest struct {
	Count       int    `json:"count"`
	Concurrency int    `json:"concurrency"`
	Asset       string `json:"asset"`
	AmountRaw   string `json:"amountRaw"`
	RealRun     bool   `json:"realRun"`
}

type eipProbeRunResponse struct {
	OK          bool             `json:"ok"`
	Mode        string           `json:"mode"`
	Network     string           `json:"network"`
	StartedAt   string           `json:"startedAt"`
	FinishedAt  string           `json:"finishedAt"`
	Count       int              `json:"count"`
	Concurrency int              `json:"concurrency"`
	Summary     map[string]any   `json:"summary"`
	Wallets     []eipProbeWallet `json:"wallets"`
	Results     []eipProbeResult `json:"results"`
	Warnings    []string         `json:"warnings,omitempty"`
}

type eipProbeResult struct {
	At                    string           `json:"at"`
	Event                 string           `json:"event"`
	Mode                  string           `json:"mode"`
	Network               string           `json:"network"`
	Rail                  string           `json:"rail"`
	Asset                 string           `json:"asset"`
	FromWallet            string           `json:"from_wallet"`
	ToWallet              string           `json:"to_wallet"`
	Relayer               string           `json:"relayer,omitempty"`
	Nonce                 string           `json:"nonce"`
	Digest                string           `json:"digest,omitempty"`
	TxHash                string           `json:"tx_hash,omitempty"`
	BlockNumber           uint64           `json:"block_number,omitempty"`
	GasUsed               uint64           `json:"gas_used,omitempty"`
	ExpectedGas           uint64           `json:"expected_gas,omitempty"`
	GasDriftPct           float64          `json:"gas_drift_pct,omitempty"`
	ExecutionLatencyMS    int64            `json:"execution_latency_ms"`
	AntiReplayNonceStored bool             `json:"anti_replay_nonce_stored"`
	Status                string           `json:"status"`
	Error                 string           `json:"error,omitempty"`
	Logs                  []map[string]any `json:"logs,omitempty"`
}

func newEIPProbeRunner(cfg *config.Config, db *database.DB) *eipProbeRunner {
	return &eipProbeRunner{cfg: cfg, db: db}
}

func (r *eipProbeRunner) status(ctx context.Context) eipProbeRunResponse {
	wallets, warnings := r.loadWallets()
	r.enrichWalletBalances(ctx, wallets)
	mode := "dry_run"
	if r.realRunAvailable(warnings) {
		mode = "real_run_ready"
	}
	r.mu.Lock()
	history := append([]eipProbeResult(nil), r.history...)
	r.mu.Unlock()
	return eipProbeRunResponse{
		OK:       true,
		Mode:     mode,
		Network:  r.network(),
		Wallets:  wallets,
		Results:  tailProbeResults(history, 80),
		Summary:  summarizeProbeResults(history),
		Warnings: warnings,
	}
}

func (r *eipProbeRunner) run(ctx context.Context, req eipProbeRunRequest, domain eip712.Domain, assets []eip712.AssetCapability) (eipProbeRunResponse, error) {
	if r == nil || r.cfg == nil || !r.cfg.EIPProbeEnabled {
		return eipProbeRunResponse{}, errors.New("EIP probes disabled; set EIP_PROBE_ENABLED=true")
	}
	if req.Count <= 0 {
		req.Count = 6
	}
	if req.Count > 50 {
		req.Count = 50
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 3
	}
	if req.Concurrency > 10 {
		req.Concurrency = 10
	}
	req.Asset = strings.ToUpper(strings.TrimSpace(firstNonEmpty(req.Asset, r.cfg.EIPProbeAsset, "USDT")))
	req.AmountRaw = strings.TrimSpace(firstNonEmpty(req.AmountRaw, r.cfg.EIPProbeAmountRaw, "10000"))
	wallets, warnings := r.loadWallets()
	r.enrichWalletBalances(ctx, wallets)
	realRun := req.RealRun && r.realRunAvailable(warnings)
	mode := "dry_run"
	if realRun {
		mode = "real_run"
	}

	started := time.Now().UTC()
	sem := make(chan struct{}, req.Concurrency)
	results := make([]eipProbeResult, req.Count)
	var wg sync.WaitGroup
	for i := 0; i < req.Count; i++ {
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			from := wallets[i%len(wallets)]
			to := wallets[(i+1)%len(wallets)]
			if realRun {
				switch r.probeRail(req.Asset) {
				case "erc20_transfer":
					results[i] = r.runERC20TransferReal(ctx, i, from, to, req, domain, assets)
				default:
					results[i] = r.runEIP3009Real(ctx, i, from, to, req, domain, assets)
				}
				return
			}
			results[i] = r.runDryProbe(ctx, i, from, to, req, domain, assets, mode)
		}()
	}
	wg.Wait()
	finished := time.Now().UTC()
	r.appendHistory(results)
	return eipProbeRunResponse{
		OK:          true,
		Mode:        mode,
		Network:     r.network(),
		StartedAt:   started.Format(time.RFC3339Nano),
		FinishedAt:  finished.Format(time.RFC3339Nano),
		Count:       req.Count,
		Concurrency: req.Concurrency,
		Summary:     summarizeProbeResults(results),
		Wallets:     wallets,
		Results:     results,
		Warnings:    warnings,
	}, nil
}

func (r *eipProbeRunner) runDryProbe(ctx context.Context, index int, from, to eipProbeWallet, req eipProbeRunRequest, domain eip712.Domain, assets []eip712.AssetCapability, mode string) eipProbeResult {
	started := time.Now()
	nonce := probeNonce(index)
	intent := eip712.Intent{
		IntentType:     eip712.TypeM2MIntent,
		Payer:          from.Address,
		Recipient:      to.Address,
		Asset:          req.Asset,
		Amount:         req.AmountRaw,
		FeeBps:         0,
		Nonce:          nonce,
		Deadline:       uint64(time.Now().Add(15 * time.Minute).Unix()),
		IdempotencyKey: nonce,
	}
	prepared, err := eip712.Prepare(domain, resolveEIPIntentAsset(intent, assets), assets)
	res := eipProbeResult{
		At:         time.Now().UTC().Format(time.RFC3339Nano),
		Event:      "probe_transaction_prepared",
		Mode:       mode,
		Network:    r.network(),
		Rail:       "eip712_intent_nonce_probe",
		Asset:      req.Asset,
		FromWallet: from.Address,
		ToWallet:   to.Address,
		Nonce:      nonce,
		Status:     "prepared",
		Logs: []map[string]any{{
			"event":         "probe_transaction_initiated",
			"rail_selected": "eip712_intent_nonce_probe",
			"asset":         req.Asset,
			"from_wallet":   from.Address,
			"to_wallet":     to.Address,
		}},
	}
	if err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		res.ExecutionLatencyMS = time.Since(started).Milliseconds()
		return res
	}
	res.Digest = prepared.Digest
	if r.db != nil {
		err = r.db.RecordEIP712Nonce(ctx, database.EIP712NonceInput{
			Signer:     prepared.ExpectedSigner,
			IntentType: prepared.IntentType,
			Nonce:      prepared.Nonce,
			Digest:     prepared.Digest,
			ChainID:    prepared.Domain.ChainID,
			ExpiresAt:  time.Unix(int64(prepared.Deadline), 0).UTC(),
		})
		if err == nil {
			res.AntiReplayNonceStored = true
		} else if errors.Is(err, database.ErrEIP712NonceReplay) {
			res.Status = "failed"
			res.Error = "nonce replay"
		} else {
			res.Status = "failed"
			res.Error = err.Error()
		}
	}
	res.ExecutionLatencyMS = time.Since(started).Milliseconds()
	if res.Status != "failed" {
		res.Status = "ok"
	}
	res.Logs = append(res.Logs, map[string]any{
		"event":                    "probe_transaction_mined",
		"execution_latency_secs":   float64(res.ExecutionLatencyMS) / 1000,
		"anti_replay_nonce_stored": res.AntiReplayNonceStored,
		"dry_run":                  true,
	})
	return res
}

func (r *eipProbeRunner) runEIP3009Real(ctx context.Context, index int, from, to eipProbeWallet, req eipProbeRunRequest, domain eip712.Domain, assets []eip712.AssetCapability) eipProbeResult {
	started := time.Now()
	nonce := probeNonce(index)
	res := eipProbeResult{
		At:          time.Now().UTC().Format(time.RFC3339Nano),
		Event:       "probe_transaction_initiated",
		Mode:        "real_run",
		Network:     r.network(),
		Rail:        "eip3009_transfer_with_authorization",
		Asset:       req.Asset,
		FromWallet:  from.Address,
		ToWallet:    to.Address,
		Nonce:       nonce,
		ExpectedGas: r.cfg.EIPProbeExpectedGas3009,
		Status:      "transmitting",
		Logs: []map[string]any{{
			"event":                       "probe_transaction_initiated",
			"rail_selected":               "eip3009_transfer_with_authorization",
			"asset":                       req.Asset,
			"from_wallet":                 from.Address,
			"to_wallet":                   to.Address,
			"relayer_fee_paid_by_gateway": "native gas",
		}},
	}
	client, err := dialProbeClient(ctx, r.cfg.EIPProbeRPCUrls)
	if err != nil {
		return res.fail(started, err)
	}
	defer client.Close()
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return res.fail(started, err)
	}
	if isProbeMainnetChain(chainID) && !r.cfg.EIPProbeAllowMainnet {
		return res.fail(started, fmt.Errorf("mainnet probe blocked on chain %s; set EIP_PROBE_ALLOW_MAINNET=true to allow real value movement", chainID.String()))
	}
	token := common.HexToAddress(r.cfg.EIPProbeTokenContract)
	value, ok := new(big.Int).SetString(req.AmountRaw, 10)
	if !ok || value.Sign() <= 0 {
		return res.fail(started, fmt.Errorf("amountRaw invalido"))
	}
	fromKey, err := privateKeyForAddress(from.Address, r.cfg.EIPProbeWalletPrivKeys)
	if err != nil {
		return res.fail(started, err)
	}
	relayerKey, err := crypto.HexToECDSA(cleanHex(r.cfg.EIPProbeRelayerPrivKey))
	if err != nil {
		return res.fail(started, fmt.Errorf("relayer private key invalida: %w", err))
	}
	relayer := crypto.PubkeyToAddress(relayerKey.PublicKey)
	res.Relayer = strings.ToLower(relayer.Hex())
	validBefore := uint64(time.Now().Add(10 * time.Minute).Unix())
	sig, digest, err := signEIP3009Authorization(fromKey, r.cfg.EIPProbeTokenName, r.cfg.EIPProbeTokenVersion, chainID, token, common.HexToAddress(from.Address), common.HexToAddress(to.Address), value, 0, validBefore, nonce)
	if err != nil {
		return res.fail(started, err)
	}
	res.Digest = digest.Hex()
	data, err := eip712.BuildEIP3009TransferWithAuthorizationCalldata(from.Address, to.Address, value, 0, validBefore, nonce, "0x"+hex.EncodeToString(sig))
	if err != nil {
		return res.fail(started, err)
	}
	r.relayerMu.Lock()
	defer r.relayerMu.Unlock()
	txHash, receipt, err := sendContractCall(ctx, client, relayerKey, relayer, token, common.FromHex(data), chainID, time.Duration(r.cfg.EIPProbeConfirmTimeout)*time.Second)
	if err != nil {
		return res.fail(started, err)
	}
	res.TxHash = txHash.Hex()
	if receipt != nil {
		res.BlockNumber = receipt.BlockNumber.Uint64()
		res.GasUsed = receipt.GasUsed
	}
	if r.cfg.EIPProbeExpectedGas3009 > 0 && res.GasUsed > 0 {
		res.GasDriftPct = (float64(res.GasUsed) - float64(r.cfg.EIPProbeExpectedGas3009)) / float64(r.cfg.EIPProbeExpectedGas3009) * 100
	}
	prepared, _ := eip712.Prepare(domain, resolveEIPIntentAsset(eip712.Intent{
		IntentType:     eip712.TypeM2MIntent,
		Payer:          from.Address,
		Recipient:      to.Address,
		Asset:          req.Asset,
		Amount:         req.AmountRaw,
		Nonce:          nonce,
		Deadline:       validBefore,
		IdempotencyKey: nonce,
	}, assets), assets)
	if r.db != nil && prepared.Digest != "" {
		if err := r.db.RecordEIP712Nonce(ctx, database.EIP712NonceInput{Signer: prepared.ExpectedSigner, IntentType: prepared.IntentType, Nonce: prepared.Nonce, Digest: prepared.Digest, ChainID: prepared.Domain.ChainID, ExpiresAt: time.Unix(int64(validBefore), 0).UTC()}); err == nil {
			res.AntiReplayNonceStored = true
		}
	}
	res.ExecutionLatencyMS = time.Since(started).Milliseconds()
	res.Status = "ok"
	res.Event = "probe_transaction_mined"
	res.Logs = append(res.Logs, map[string]any{
		"event":                    "probe_transaction_mined",
		"tx_hash":                  res.TxHash,
		"block_number":             res.BlockNumber,
		"execution_latency_secs":   float64(res.ExecutionLatencyMS) / 1000,
		"gas_used":                 res.GasUsed,
		"anti_replay_nonce_stored": res.AntiReplayNonceStored,
	})
	return res
}

func (r *eipProbeRunner) runERC20TransferReal(ctx context.Context, index int, from, to eipProbeWallet, req eipProbeRunRequest, domain eip712.Domain, assets []eip712.AssetCapability) eipProbeResult {
	started := time.Now()
	nonce := probeNonce(index)
	res := eipProbeResult{
		At:         time.Now().UTC().Format(time.RFC3339Nano),
		Event:      "probe_transaction_initiated",
		Mode:       "real_run",
		Network:    r.network(),
		Rail:       "erc20_transfer",
		Asset:      req.Asset,
		FromWallet: from.Address,
		ToWallet:   to.Address,
		Nonce:      nonce,
		Status:     "transmitting",
		Logs: []map[string]any{{
			"event":         "probe_transaction_initiated",
			"rail_selected": "erc20_transfer",
			"asset":         req.Asset,
			"from_wallet":   from.Address,
			"to_wallet":     to.Address,
			"gas_paid_by":   from.Address,
		}},
	}
	client, err := dialProbeClient(ctx, r.cfg.EIPProbeRPCUrls)
	if err != nil {
		return res.fail(started, err)
	}
	defer client.Close()
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return res.fail(started, err)
	}
	if isProbeMainnetChain(chainID) && !r.cfg.EIPProbeAllowMainnet {
		return res.fail(started, fmt.Errorf("mainnet probe blocked on chain %s; set EIP_PROBE_ALLOW_MAINNET=true to allow real value movement", chainID.String()))
	}
	token := common.HexToAddress(r.cfg.EIPProbeTokenContract)
	value, ok := new(big.Int).SetString(req.AmountRaw, 10)
	if !ok || value.Sign() <= 0 {
		return res.fail(started, fmt.Errorf("amountRaw invalido"))
	}
	fromKey, err := privateKeyForAddress(from.Address, r.cfg.EIPProbeWalletPrivKeys)
	if err != nil {
		return res.fail(started, err)
	}
	fromAddr := common.HexToAddress(from.Address)
	data := buildERC20TransferCalldata(common.HexToAddress(to.Address), value)
	txHash, receipt, err := sendContractCall(ctx, client, fromKey, fromAddr, token, data, chainID, time.Duration(r.cfg.EIPProbeConfirmTimeout)*time.Second)
	if err != nil {
		return res.fail(started, err)
	}
	res.TxHash = txHash.Hex()
	if receipt != nil {
		res.BlockNumber = receipt.BlockNumber.Uint64()
		res.GasUsed = receipt.GasUsed
	}
	prepared, _ := eip712.Prepare(domain, resolveEIPIntentAsset(eip712.Intent{
		IntentType:     eip712.TypeM2MIntent,
		Payer:          from.Address,
		Recipient:      to.Address,
		Asset:          req.Asset,
		Amount:         req.AmountRaw,
		Nonce:          nonce,
		Deadline:       uint64(time.Now().Add(10 * time.Minute).Unix()),
		IdempotencyKey: nonce,
	}, assets), assets)
	if r.db != nil && prepared.Digest != "" {
		if err := r.db.RecordEIP712Nonce(ctx, database.EIP712NonceInput{Signer: prepared.ExpectedSigner, IntentType: prepared.IntentType, Nonce: prepared.Nonce, Digest: prepared.Digest, ChainID: prepared.Domain.ChainID, ExpiresAt: time.Now().Add(10 * time.Minute).UTC()}); err == nil {
			res.AntiReplayNonceStored = true
		}
	}
	res.ExecutionLatencyMS = time.Since(started).Milliseconds()
	res.Status = "ok"
	res.Event = "probe_transaction_mined"
	res.Logs = append(res.Logs, map[string]any{
		"event":                    "probe_transaction_mined",
		"tx_hash":                  res.TxHash,
		"block_number":             res.BlockNumber,
		"execution_latency_secs":   float64(res.ExecutionLatencyMS) / 1000,
		"gas_used":                 res.GasUsed,
		"anti_replay_nonce_stored": res.AntiReplayNonceStored,
	})
	return res
}

func (res eipProbeResult) fail(started time.Time, err error) eipProbeResult {
	res.Status = "failed"
	res.Event = "probe_transaction_failed"
	res.Error = err.Error()
	res.ExecutionLatencyMS = time.Since(started).Milliseconds()
	res.Logs = append(res.Logs, map[string]any{"event": "probe_transaction_failed", "error": res.Error})
	return res
}

func signEIP3009Authorization(key *ecdsa.PrivateKey, name, version string, chainID *big.Int, token, from, to common.Address, value *big.Int, validAfter, validBefore uint64, nonceHex string) ([]byte, common.Hash, error) {
	nonce, err := decodeProbeBytes32(nonceHex)
	if err != nil {
		return nil, common.Hash{}, err
	}
	domainType := crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	transferType := crypto.Keccak256Hash([]byte("TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"))
	domainSeparator := crypto.Keccak256Hash(
		domainType.Bytes(),
		crypto.Keccak256Hash([]byte(name)).Bytes(),
		crypto.Keccak256Hash([]byte(version)).Bytes(),
		uint256Bytes(chainID),
		addressBytes(token),
	)
	structHash := crypto.Keccak256Hash(
		transferType.Bytes(),
		addressBytes(from),
		addressBytes(to),
		uint256Bytes(value),
		uint256Bytes(new(big.Int).SetUint64(validAfter)),
		uint256Bytes(new(big.Int).SetUint64(validBefore)),
		nonce,
	)
	digest := crypto.Keccak256Hash(append(append([]byte{0x19, 0x01}, domainSeparator.Bytes()...), structHash.Bytes()...))
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		return nil, common.Hash{}, err
	}
	return sig, digest, nil
}

func sendContractCall(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, from, to common.Address, data []byte, chainID *big.Int, timeout time.Duration) (common.Hash, *types.Receipt, error) {
	gasPrice, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return common.Hash{}, nil, err
	}
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, nil, err
	}
	msg := ethereum.CallMsg{From: from, To: &to, GasPrice: gasPrice, Value: big.NewInt(0), Data: data}
	gasLimit, err := client.EstimateGas(ctx, msg)
	if err != nil {
		return common.Hash{}, nil, err
	}
	tx := types.NewTransaction(nonce, to, big.NewInt(0), gasLimit+gasLimit/5, gasPrice, data)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return common.Hash{}, nil, err
	}
	if err := client.SendTransaction(ctx, signed); err != nil {
		return common.Hash{}, nil, err
	}
	receipt, err := waitReceipt(ctx, client, signed.Hash(), timeout)
	if err != nil {
		return signed.Hash(), nil, err
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return signed.Hash(), receipt, fmt.Errorf("tx reverted")
	}
	return signed.Hash(), receipt, nil
}

func buildERC20TransferCalldata(to common.Address, value *big.Int) []byte {
	data := append([]byte{}, crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]...)
	data = append(data, addressBytes(to)...)
	data = append(data, uint256Bytes(value)...)
	return data
}

func waitReceipt(ctx context.Context, client *ethclient.Client, hash common.Hash, timeout time.Duration) (*types.Receipt, error) {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	for {
		receipt, err := client.TransactionReceipt(ctx, hash)
		if err == nil && receipt != nil {
			return receipt, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("receipt timeout: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (r *eipProbeRunner) loadWallets() ([]eipProbeWallet, []string) {
	var warnings []string
	var wallets []eipProbeWallet
	for i, raw := range splitCSV(r.cfg.EIPProbeWalletPrivKeys) {
		key, err := crypto.HexToECDSA(cleanHex(raw))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("EIP_PROBE_WALLET_PRIVATE_KEYS[%d] invalida", i))
			continue
		}
		wallets = append(wallets, eipProbeWallet{ID: fmt.Sprintf("Wallet_Probe_%02d", len(wallets)+1), Address: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), Source: "env", Status: "IDLE"})
	}
	if len(wallets) < 2 {
		warnings = append(warnings, "menos de 2 wallets em EIP_PROBE_WALLET_PRIVATE_KEYS; usando wallets efemeras dry_run")
		for len(wallets) < 3 {
			key, _ := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
			wallets = append(wallets, eipProbeWallet{ID: fmt.Sprintf("Wallet_Probe_%02d", len(wallets)+1), Address: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), Source: "ephemeral", Status: "DRY_RUN_ONLY"})
		}
	}
	return wallets, warnings
}

func (r *eipProbeRunner) enrichWalletBalances(ctx context.Context, wallets []eipProbeWallet) {
	if r == nil || r.cfg == nil || strings.TrimSpace(r.cfg.EIPProbeRPCUrls) == "" {
		return
	}
	client, err := dialProbeClient(ctx, r.cfg.EIPProbeRPCUrls)
	if err != nil {
		return
	}
	defer client.Close()
	var token *common.Address
	if common.IsHexAddress(r.cfg.EIPProbeTokenContract) {
		addr := common.HexToAddress(r.cfg.EIPProbeTokenContract)
		token = &addr
	}
	for i := range wallets {
		wallets[i].TokenSymbol = r.cfg.EIPProbeTokenSymbol
		addr := common.HexToAddress(wallets[i].Address)
		if balance, err := client.BalanceAt(ctx, addr, nil); err == nil && balance != nil {
			wallets[i].NativeBalanceWei = balance.String()
		}
		if token != nil {
			if balance, err := callERC20BalanceOf(ctx, client, *token, addr); err == nil && balance != nil {
				wallets[i].TokenBalanceRaw = balance.String()
			}
		}
	}
}

func (r *eipProbeRunner) realRunAvailable(warnings []string) bool {
	if r.cfg == nil || !r.cfg.EIPProbeRealRun || len(warnings) > 0 || strings.TrimSpace(r.cfg.EIPProbeRPCUrls) == "" || !common.IsHexAddress(r.cfg.EIPProbeTokenContract) {
		return false
	}
	if r.probeRail(r.cfg.EIPProbeAsset) == "eip3009_transfer_with_authorization" {
		return strings.TrimSpace(r.cfg.EIPProbeRelayerPrivKey) != ""
	}
	return true
}

func callERC20BalanceOf(ctx context.Context, client *ethclient.Client, token, owner common.Address) (*big.Int, error) {
	data := append(crypto.Keccak256([]byte("balanceOf(address)"))[:4], addressBytes(owner)...)
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &token, Data: data}, nil)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return big.NewInt(0), nil
	}
	return new(big.Int).SetBytes(out), nil
}

func (r *eipProbeRunner) appendHistory(results []eipProbeResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.history = append(r.history, results...)
	if len(r.history) > 500 {
		r.history = r.history[len(r.history)-500:]
	}
}

func (r *eipProbeRunner) network() string {
	if r == nil || r.cfg == nil || strings.TrimSpace(r.cfg.EIPProbeNetwork) == "" {
		return "bsc"
	}
	return r.cfg.EIPProbeNetwork
}

func (r *eipProbeRunner) probeRail(asset string) string {
	if r != nil && r.cfg != nil {
		rail := strings.ToLower(strings.TrimSpace(r.cfg.EIPProbeRail))
		switch rail {
		case "erc20", "transfer", "erc20_transfer":
			return "erc20_transfer"
		case "eip3009", "transferwithauthorization", "transfer_with_authorization", "eip3009_transfer_with_authorization":
			return "eip3009_transfer_with_authorization"
		}
	}
	if strings.EqualFold(asset, "USDT") {
		return "erc20_transfer"
	}
	return "eip3009_transfer_with_authorization"
}

func isProbeMainnetChain(chainID *big.Int) bool {
	if chainID == nil {
		return false
	}
	switch chainID.Int64() {
	case 1, 56, 137:
		return true
	default:
		return false
	}
}

func summarizeProbeResults(results []eipProbeResult) map[string]any {
	var ok, failed int
	var latencies []int64
	var gas []uint64
	for _, item := range results {
		if item.Status == "ok" {
			ok++
		} else if item.Status == "failed" {
			failed++
		}
		if item.ExecutionLatencyMS > 0 {
			latencies = append(latencies, item.ExecutionLatencyMS)
		}
		if item.GasUsed > 0 {
			gas = append(gas, item.GasUsed)
		}
	}
	return map[string]any{
		"count":      len(results),
		"ok":         ok,
		"failed":     failed,
		"p95_ms":     percentileInt64(latencies, 95),
		"avg_gas":    avgUint64(gas),
		"max_gas":    maxUint64(gas),
		"real_mined": countMined(results),
	}
}

func countMined(results []eipProbeResult) int {
	n := 0
	for _, item := range results {
		if item.TxHash != "" && item.BlockNumber > 0 {
			n++
		}
	}
	return n
}

func percentileInt64(values []int64, p int) int64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]int64(nil), values...)
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	idx := (len(cp)*p+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func avgUint64(values []uint64) uint64 {
	if len(values) == 0 {
		return 0
	}
	var sum uint64
	for _, v := range values {
		sum += v
	}
	return sum / uint64(len(values))
}

func maxUint64(values []uint64) uint64 {
	var max uint64
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	return max
}

func tailProbeResults(results []eipProbeResult, n int) []eipProbeResult {
	if len(results) <= n {
		return results
	}
	return results[len(results)-n:]
}

func privateKeyForAddress(address, csv string) (*ecdsa.PrivateKey, error) {
	for _, raw := range splitCSV(csv) {
		key, err := crypto.HexToECDSA(cleanHex(raw))
		if err != nil {
			continue
		}
		if strings.EqualFold(crypto.PubkeyToAddress(key.PublicKey).Hex(), address) {
			return key, nil
		}
	}
	return nil, fmt.Errorf("private key da wallet %s nao encontrada em EIP_PROBE_WALLET_PRIVATE_KEYS", address)
}

func probeNonce(index int) string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	copy(raw[:8], []byte(fmt.Sprintf("%08x", time.Now().UnixNano()+int64(index))))
	return "0x" + hex.EncodeToString(raw[:])
}

func splitCSV(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

func dialProbeClient(ctx context.Context, raw string) (*ethclient.Client, error) {
	var lastErr error
	for _, item := range splitCSV(raw) {
		client, err := ethclient.DialContext(ctx, item)
		if err == nil {
			if _, chainErr := client.ChainID(ctx); chainErr == nil {
				return client, nil
			} else {
				lastErr = chainErr
			}
			client.Close()
			continue
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("nenhum RPC EIP_PROBE_RPC_URLS/BSC_RPC_URLS/RPC1..RPCN configurado")
}

func cleanHex(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "0x")
}

func uint256Bytes(value *big.Int) []byte {
	out := make([]byte, 32)
	if value != nil {
		value.FillBytes(out)
	}
	return out
}

func addressBytes(addr common.Address) []byte {
	out := make([]byte, 32)
	copy(out[12:], addr.Bytes())
	return out
}

func decodeProbeBytes32(value string) ([]byte, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "0x")
	if len(value)%2 == 1 {
		value = "0" + value
	}
	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > 32 {
		raw = crypto.Keccak256(raw)
	}
	out := make([]byte, 32)
	copy(out[32-len(raw):], raw)
	return out, nil
}
