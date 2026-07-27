package paymaster

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"payment-gateway/internal/security"
)

const (
	batchMaxSize    = 5
	batchWindowMs   = 500 * time.Millisecond
	batchChanCap    = 256
	batchConcurrent = 3

	// Multicall3 on BSC + Polygon mainnet
	multicall3DefaultAddr = "0xcA11bde05977b3631167028862bE2a173976CA11"

	// aggregate3(Call3[]) function selector
	// keccak256("aggregate3((address,bool,bytes)[])") → 0x82ad56cb
	multicall3Selector = "82ad56cb"
)

// relayJob is a single relay queued for dispatch.
type relayJob struct {
	relayID string
	to      string
	amount  string
	token   string
	network string
}

// RelayBatcher collects relay jobs and dispatches them concurrently,
// optionally encoding them as a single Multicall3 on-chain tx.
type RelayBatcher struct {
	queue      chan relayJob
	signerURL  string
	signerHMAC string
	multicall  string                                       // Multicall3 contract address (empty = disabled)
	relayer    *TokenRelayer                                // spread-capture engine; nil = send full amount
	retryFn    func(ctx context.Context, id, errMsg string) // callback on DLQ
	httpClient *http.Client
	stop       chan struct{}
	wg         sync.WaitGroup
}

// NewRelayBatcher creates a batcher and starts its dispatch goroutine.
// relayer may be nil — when nil every job is dispatched as a single full-amount transfer.
// When relayer is non-nil the relay is split: net leg → destination, fee leg → hot wallet.
func NewRelayBatcher(signerURL, signerHMAC, multicallAddr string, relayer *TokenRelayer, retryFn func(ctx context.Context, id, errMsg string)) *RelayBatcher {
	addr := multicallAddr
	if addr == "" {
		addr = multicall3DefaultAddr
	}
	rb := &RelayBatcher{
		queue:      make(chan relayJob, batchChanCap),
		signerURL:  signerURL,
		signerHMAC: signerHMAC,
		multicall:  addr,
		relayer:    relayer,
		retryFn:    retryFn,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		stop:       make(chan struct{}),
	}
	return rb
}

// Start begins the collector loop. Call once; blocks until ctx is cancelled.
func (rb *RelayBatcher) Start(ctx context.Context) {
	rb.wg.Add(1)
	defer rb.wg.Done()
	for {
		batch := rb.collectBatch(ctx)
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		rb.dispatchBatch(ctx, batch)
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// Enqueue adds a relay job to the queue. Non-blocking; returns false if full.
func (rb *RelayBatcher) Enqueue(job relayJob) bool {
	select {
	case rb.queue <- job:
		return true
	default:
		slog.Warn("relay batcher: queue full, job dropped", "relay_id", job.relayID)
		return false
	}
}

// collectBatch waits for up to batchMaxSize items or batchWindowMs, whichever comes first.
func (rb *RelayBatcher) collectBatch(ctx context.Context) []relayJob {
	var batch []relayJob

	// Block until we get the first item or context cancels.
	select {
	case <-ctx.Done():
		return nil
	case job := <-rb.queue:
		batch = append(batch, job)
	}

	// Drain remaining items up to batchMaxSize within the window.
	deadline := time.After(batchWindowMs)
	for len(batch) < batchMaxSize {
		select {
		case job := <-rb.queue:
			batch = append(batch, job)
		case <-deadline:
			return batch
		case <-ctx.Done():
			return batch
		}
	}
	return batch
}

// dispatchBatch sends all jobs in the batch, using concurrent goroutines
// limited by a semaphore of batchConcurrent.
func (rb *RelayBatcher) dispatchBatch(ctx context.Context, batch []relayJob) {
	slog.Info("relay batcher: dispatching batch",
		"size", len(batch),
		"multicall", len(batch) >= 2 && rb.multicall != "",
	)

	if len(batch) >= 2 && rb.multicall != "" {
		if rb.canMulticall(batch) {
			rb.dispatchMulticallBatch(ctx, batch)
			return
		}
		slog.Info("relay batcher: multicall skipped; batch is not homogeneous or has invalid relay data", "size", len(batch))
	}

	sem := make(chan struct{}, batchConcurrent)
	var wg sync.WaitGroup

	for _, job := range batch {
		job := job
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			rb.dispatchOne(ctx, job)
		}()
	}
	wg.Wait()
}

// ── signerTransferPayload matches the existing signer /hd/transfer contract ──

func (rb *RelayBatcher) canMulticall(batch []relayJob) bool {
	if len(batch) < 2 || strings.TrimSpace(rb.multicall) == "" {
		return false
	}
	network := strings.ToUpper(strings.TrimSpace(batch[0].network))
	token := strings.ToLower(strings.TrimSpace(batch[0].token))
	if network == "" || token == "" || !isHexAddressString(token) || !isHexAddressString(rb.multicall) {
		return false
	}
	for _, job := range batch {
		if strings.ToUpper(strings.TrimSpace(job.network)) != network {
			return false
		}
		if strings.ToLower(strings.TrimSpace(job.token)) != token {
			return false
		}
		if !isHexAddressString(job.to) || strings.TrimSpace(job.amount) == "" || strings.TrimSpace(job.amount) == "0" {
			return false
		}
	}
	return true
}

func (rb *RelayBatcher) dispatchMulticallBatch(ctx context.Context, batch []relayJob) {
	payload, err := rb.buildMulticallPayload(batch)
	if err != nil {
		slog.Warn("relay batcher: multicall build failed, falling back to individual dispatch", "error", err)
		for _, job := range batch {
			rb.dispatchOne(ctx, job)
		}
		return
	}

	cfg := DefaultRetryConfig()
	err = ExecuteWithRetry(ctx, cfg, "relay:multicall:"+payload.IdempotencyKey, func(ctx context.Context) error {
		return rb.signerContractCall(ctx, payload)
	})
	if err != nil {
		slog.Error("relay batcher: multicall dispatch failed", "error", err, "batch_size", len(batch), "idempotency_key", payload.IdempotencyKey)
		for _, job := range batch {
			if rb.retryFn != nil {
				rb.retryFn(ctx, job.relayID, err.Error())
			}
		}
		return
	}

	slog.Info("relay batcher: multicall dispatched", "batch_size", len(batch), "idempotency_key", payload.IdempotencyKey, "amount", payload.Amount)
}

func (rb *RelayBatcher) buildMulticallPayload(batch []relayJob) (signerContractCallPayload, error) {
	if len(batch) < 2 {
		return signerContractCallPayload{}, fmt.Errorf("batch pequeno demais")
	}
	network := strings.ToUpper(strings.TrimSpace(batch[0].network))
	token := strings.TrimSpace(batch[0].token)
	decimals := tokenDecimalsForNetwork(network)
	var calls []multicall3Call
	totalAudit := big.NewInt(0)
	var ids []string

	for _, job := range batch {
		ids = append(ids, job.relayID)
		netAmount := strings.TrimSpace(job.amount)
		feeAmount := ""

		if rb.relayer != nil && netAmount != "" && netAmount != "0" {
			plan, err := rb.relayer.Plan(netAmount)
			if err != nil {
				return signerContractCallPayload{}, fmt.Errorf("spread plan %s: %w", job.relayID, err)
			}
			netAmount = microUSDTToString(plan.NetMicro)
			if rb.relayer.HasFeeDestination() && plan.FeeMicro.Sign() > 0 {
				feeAmount = microUSDTToString(plan.FeeMicro)
			}
			slog.Info("[Paymaster] multicall relay split computed",
				"relay_id", job.relayID,
				"total_usdt", plan.TotalUSDT,
				"net_usdt", plan.NetUSDT,
				"fee_usdt", plan.FeeUSDT,
				"spread_bps", plan.SpreadBps,
				"hot_wallet", rb.relayer.FeeDestination(),
			)
		}

		netUnits, err := decimalToTokenUnits(netAmount, decimals)
		if err != nil {
			return signerContractCallPayload{}, fmt.Errorf("net amount %s: %w", job.relayID, err)
		}
		if netUnits.Sign() <= 0 {
			return signerContractCallPayload{}, fmt.Errorf("net amount zero: %s", job.relayID)
		}
		calls = append(calls, multicall3Call{
			Target:       token,
			AllowFailure: false,
			CallData:     encodeERC20Transfer(job.to, netUnits.Bytes()),
		})

		auditUnits, err := decimalToTokenUnits(job.amount, 6)
		if err != nil {
			return signerContractCallPayload{}, fmt.Errorf("audit amount %s: %w", job.relayID, err)
		}
		totalAudit.Add(totalAudit, auditUnits)

		if feeAmount != "" {
			feeUnits, err := decimalToTokenUnits(feeAmount, decimals)
			if err != nil {
				return signerContractCallPayload{}, fmt.Errorf("fee amount %s: %w", job.relayID, err)
			}
			if feeUnits.Sign() > 0 {
				calls = append(calls, multicall3Call{
					Target:       token,
					AllowFailure: false,
					CallData:     encodeERC20Transfer(rb.relayer.FeeDestination(), feeUnits.Bytes()),
				})
			}
		}
	}

	return signerContractCallPayload{
		DerivationIndex: 0,
		To:              strings.ToLower(strings.TrimSpace(rb.multicall)),
		Data:            "0x" + encodeMulticall3(calls),
		Network:         network,
		IdempotencyKey:  "multicall_" + strings.Join(ids, "_"),
		Amount:          tokenUnitsToDecimal(totalAudit, 6),
		TokenContract:   token,
	}, nil
}

type signerTransferPayload struct {
	DerivationIndex int    `json:"derivationIndex"`
	To              string `json:"to"`
	Amount          string `json:"amount"`
	TokenContract   string `json:"tokenContract"`
	Network         string `json:"network"`
	IdempotencyKey  string `json:"idempotencyKey"`
}

type signerContractCallPayload struct {
	DerivationIndex int    `json:"derivationIndex"`
	To              string `json:"to"`
	Data            string `json:"data"`
	Network         string `json:"network"`
	IdempotencyKey  string `json:"idempotencyKey"`
	Amount          string `json:"amount,omitempty"`
	TokenContract   string `json:"tokenContract,omitempty"`
}

// dispatchOne sends one relay to the signer with retry + backoff.
//
// When a TokenRelayer is configured the amount is split:
//   - net leg  → destination          (user receives netAmount)
//   - fee leg  → TreasuryHot wallet   (ChainFX retains feeAmount = spread)
//
// When no TokenRelayer is set (or the relayer has no fee destination) the full
// amount is sent in a single transfer — no spread is captured.
func (rb *RelayBatcher) dispatchOne(ctx context.Context, job relayJob) {
	cfg := DefaultRetryConfig()

	// ── Compute the spread split (if relayer configured) ───────────────────
	netAmount := job.amount
	feeAmount := ""
	var feeUSDT float64

	if rb.relayer != nil && job.amount != "" && job.amount != "0" {
		if plan, err := rb.relayer.Plan(job.amount); err == nil {
			netAmount = microUSDTToString(plan.NetMicro)
			if rb.relayer.HasFeeDestination() && plan.FeeMicro.Sign() > 0 {
				feeAmount = microUSDTToString(plan.FeeMicro)
				feeUSDT = plan.FeeUSDT
			}
			slog.Info("[Paymaster] relay split computed",
				"relay_id", job.relayID,
				"total_usdt", plan.TotalUSDT,
				"net_usdt", plan.NetUSDT,
				"fee_usdt", plan.FeeUSDT,
				"spread_bps", plan.SpreadBps,
				"hot_wallet", rb.relayer.FeeDestination(),
			)
		} else {
			slog.Warn("relay batcher: spread plan failed, sending full amount",
				"relay_id", job.relayID, "error", err)
		}
	}

	// ── Net leg: netAmount → destination ───────────────────────────────────
	err := ExecuteWithRetry(ctx, cfg, "relay:net:"+job.relayID, func(ctx context.Context) error {
		return rb.signerTransfer(ctx, job.to, netAmount, job.token, job.network, job.relayID)
	})
	if err != nil {
		if rb.retryFn != nil {
			rb.retryFn(ctx, job.relayID, err.Error())
		}
		return
	}

	// Fee leg is mandatory once a split was planned. If it fails after retries,
	// the relay is marked DLQ so operations can reconcile the already-sent net leg.
	if feeAmount != "" && rb.relayer != nil {
		hotWallet := rb.relayer.FeeDestination()
		feeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		feeErr := ExecuteWithRetry(feeCtx, cfg, "relay:fee:"+job.relayID, func(ctx context.Context) error {
			return rb.signerTransfer(ctx, hotWallet, feeAmount, job.token, job.network, job.relayID+"_fee")
		})
		if feeErr != nil {
			slog.Error("[Paymaster] fee leg failed after retries",
				"relay_id", job.relayID,
				"fee_usdt", feeUSDT,
				"error", feeErr,
			)
			if rb.retryFn != nil {
				rb.retryFn(ctx, job.relayID, "fee leg failed after net leg: "+feeErr.Error())
			}
			return
		}
		slog.Info("[Paymaster] fee leg captured",
			"relay_id", job.relayID,
			"fee_usdt", feeUSDT,
			"hot_wallet", hotWallet,
		)
		feeAmount = ""
	}
}

// signerTransfer calls the signer service /hd/transfer endpoint.
func (rb *RelayBatcher) signerTransfer(ctx context.Context, to, amount, token, network, idempotencyKey string) error {
	payload := signerTransferPayload{
		DerivationIndex: 0, // hot wallet index 0
		To:              to,
		Amount:          amount,
		TokenContract:   token,
		Network:         network,
		IdempotencyKey:  idempotencyKey,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrNonRetryable, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rb.signerURL+"/hd/transfer", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrNonRetryable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, rb.signerHMAC, body)

	resp, err := rb.httpClient.Do(req)
	if err != nil {
		return err // retryable
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("%w: signer 4xx %d: %s", ErrNonRetryable, resp.StatusCode, errBody.Error)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("signer 5xx %d", resp.StatusCode) // retryable
	}
	return nil
}

// signerContractCall calls the signer service /hd/contract-call endpoint.
func (rb *RelayBatcher) signerContractCall(ctx context.Context, payload signerContractCallPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrNonRetryable, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rb.signerURL+"/hd/contract-call", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrNonRetryable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, rb.signerHMAC, body)

	resp, err := rb.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("%w: signer contract-call 4xx %d: %s", ErrNonRetryable, resp.StatusCode, errBody.Error)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("signer contract-call 5xx %d", resp.StatusCode)
	}
	return nil
}

// microUSDTToString converts micro-USDT *big.Int to a decimal string with 6 places.
// e.g. big.Int(99000000) → "99.000000"
func microUSDTToString(micro *big.Int) string {
	if micro == nil || micro.Sign() == 0 {
		return "0.000000"
	}
	divisor := big.NewInt(1_000_000)
	intPart := new(big.Int)
	fracPart := new(big.Int)
	intPart.DivMod(micro, divisor, fracPart)
	return fmt.Sprintf("%s.%06d", intPart.String(), fracPart.Int64())
}

// ── Multicall3 inline ABI encoder ────────────────────────────────────────────
// Encodes aggregate3(Call3[] calls) without importing go-ethereum/accounts/abi.
// Call3 = (address target, bool allowFailure, bytes callData)
//
// This encoder is used to prepare a single contract-call payload that bundles
// multiple ERC-20 transfer calls. The signer endpoint /hd/contract-call receives
// this when len(batch) >= 2 and PAYMASTER_MULTICALL_CONTRACT is set.

type multicall3Call struct {
	Target       string // 20-byte address hex
	AllowFailure bool
	CallData     []byte // ABI-encoded calldata
}

// encodeMulticall3 encodes an aggregate3 call for the given calls slice.
// Returns the hex-encoded calldata (without 0x prefix).
func encodeMulticall3(calls []multicall3Call) string {
	// Selector: aggregate3((address,bool,bytes)[])
	sel, _ := hex.DecodeString(multicall3Selector)

	// ABI encoding of dynamic array:
	// offset to array data = 0x20 (32 bytes)
	// array length = len(calls)
	// each Call3 tuple is encoded as:
	//   address (32 bytes, left-padded)
	//   bool    (32 bytes)
	//   offset to bytes within the tuple (relative to tuple start)
	//   ... bytes data

	var buf bytes.Buffer
	buf.Write(sel)

	// Head: offset to array start
	writeUint256(&buf, 32)

	// Array length
	writeUint256(&buf, uint64(len(calls)))

	// Each tuple head (address + bool + offset-to-bytes)
	// We need to compute offsets upfront.
	// Layout: N * 3 * 32 bytes of heads, then dynamic bytes for each.
	headSize := len(calls) * 3 * 32 // per-tuple: 3 slots
	bytesOffsets := make([]int, len(calls))
	currentBytesOffset := headSize
	for i, c := range calls {
		bytesOffsets[i] = currentBytesOffset
		// bytes take: 32 (length) + ceil(len/32)*32
		dataLen := len(c.CallData)
		currentBytesOffset += 32 + ((dataLen + 31) / 32 * 32)
	}

	// Write heads
	for i, c := range calls {
		// address: 12 zero bytes + 20-byte address
		addrHex := strings.TrimPrefix(c.Target, "0x")
		addrBytes, _ := hex.DecodeString(addrHex)
		var addrSlot [32]byte
		if len(addrBytes) == 20 {
			copy(addrSlot[12:], addrBytes)
		}
		buf.Write(addrSlot[:])

		// bool: 31 zero bytes + 0x01 if true
		var boolSlot [32]byte
		if c.AllowFailure {
			boolSlot[31] = 1
		}
		buf.Write(boolSlot[:])

		// offset to bytes (relative to start of this tuple's head, not the function)
		// ABI encodes tuple-internal offsets relative to the start of the tuple.
		tupleStart := i * 3 * 32
		relOffset := bytesOffsets[i] - tupleStart
		writeUint256(&buf, uint64(relOffset))
	}

	// Write bytes data for each call
	for _, c := range calls {
		dataLen := len(c.CallData)
		writeUint256(&buf, uint64(dataLen))
		buf.Write(c.CallData)
		// Pad to 32-byte boundary
		padLen := (32 - (dataLen % 32)) % 32
		buf.Write(make([]byte, padLen))
	}

	return hex.EncodeToString(buf.Bytes())
}

func writeUint256(buf *bytes.Buffer, v uint64) {
	var slot [32]byte
	binary.BigEndian.PutUint64(slot[24:], v)
	buf.Write(slot[:])
}

// encodeERC20Transfer encodes an ERC-20 transfer(address,uint256) calldata.
// amount is in token base units (e.g., micro-USDT = amount * 1e6).
func encodeERC20Transfer(to string, amountWei []byte) []byte {
	// selector: transfer(address,uint256) = 0xa9059cbb
	sel, _ := hex.DecodeString("a9059cbb")
	var buf bytes.Buffer
	buf.Write(sel)
	// to address (32 bytes, left-padded)
	toHex := strings.TrimPrefix(to, "0x")
	toBytes, _ := hex.DecodeString(toHex)
	var addrSlot [32]byte
	if len(toBytes) == 20 {
		copy(addrSlot[12:], toBytes)
	}
	buf.Write(addrSlot[:])
	// amount (32 bytes, left-padded)
	var amtSlot [32]byte
	if len(amountWei) <= 32 {
		copy(amtSlot[32-len(amountWei):], amountWei)
	}
	buf.Write(amtSlot[:])
	return buf.Bytes()
}

func tokenDecimalsForNetwork(network string) int {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "POLYGON":
		return 6
	default:
		return 18
	}
}

func decimalToTokenUnits(value string, decimals int) (*big.Int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("amount vazio")
	}
	if decimals < 0 || decimals > 30 {
		return nil, fmt.Errorf("decimais invalidos")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, fmt.Errorf("amount invalido")
	}
	whole := new(big.Int)
	if _, ok := whole.SetString(parts[0], 10); !ok {
		return nil, fmt.Errorf("amount invalido")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole.Mul(whole, scale)
	if len(parts) == 1 {
		return whole, nil
	}
	fraction := parts[1]
	if len(fraction) > decimals {
		fraction = fraction[:decimals]
	}
	for len(fraction) < decimals {
		fraction += "0"
	}
	frac := new(big.Int)
	if fraction != "" {
		if _, ok := frac.SetString(fraction, 10); !ok {
			return nil, fmt.Errorf("amount invalido")
		}
	}
	return whole.Add(whole, frac), nil
}

func tokenUnitsToDecimal(units *big.Int, decimals int) string {
	if units == nil {
		return "0"
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	intPart := new(big.Int)
	fracPart := new(big.Int)
	intPart.DivMod(new(big.Int).Set(units), scale, fracPart)
	if decimals == 0 {
		return intPart.String()
	}
	frac := fracPart.String()
	for len(frac) < decimals {
		frac = "0" + frac
	}
	frac = strings.TrimRight(frac, "0")
	if frac == "" {
		return intPart.String()
	}
	return intPart.String() + "." + frac
}

func isHexAddressString(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
