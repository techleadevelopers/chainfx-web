package workers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"payment-gateway/internal/config"
	"payment-gateway/internal/privacy"
	"payment-gateway/internal/security"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/lib/pq"
)

const (
	pancakeV2RouterABIJSON = `[
		{"name":"getAmountsOut","type":"function","stateMutability":"view","inputs":[{"name":"amountIn","type":"uint256"},{"name":"path","type":"address[]"}],"outputs":[{"name":"amounts","type":"uint256[]"}]},
		{"name":"swapExactTokensForTokens","type":"function","stateMutability":"nonpayable","inputs":[{"name":"amountIn","type":"uint256"},{"name":"amountOutMin","type":"uint256"},{"name":"path","type":"address[]"},{"name":"to","type":"address"},{"name":"deadline","type":"uint256"}],"outputs":[{"name":"amounts","type":"uint256[]"}]}
	]`
	pancakeERC20ABIJSON = `[
		{"name":"approve","type":"function","stateMutability":"nonpayable","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"name":"","type":"bool"}]},
		{"name":"allowance","type":"function","stateMutability":"view","inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"outputs":[{"name":"","type":"uint256"}]}
	]`
)

type pancakeSwapResult struct {
	ToAmount float64
	Rate     float64
	TxHash   string
}

type pancakeToken struct {
	Asset    string
	Contract common.Address
	Decimals int
	Native   bool
}

type pancakeSwapInstruction struct {
	Provider       string
	Network        string
	Router         common.Address
	Path           []common.Address
	AmountInRaw    *big.Int
	ExpectedOutRaw *big.Int
	MinReceivedRaw *big.Int
	SlippageBPS    int
	ExpiresAt      time.Time
	ExecutionID    string
}

type pancakeSignerSwapRequest struct {
	WalletAddress       string   `json:"walletAddress"`
	EncryptedPrivateKey string   `json:"encryptedPrivateKey"`
	Network             string   `json:"network"`
	Router              string   `json:"router"`
	Path                []string `json:"path"`
	AmountInRaw         string   `json:"amountInRaw"`
	ExpectedOutRaw      string   `json:"expectedOutRaw"`
	AmountOutMinRaw     string   `json:"amountOutMinRaw"`
	Deadline            int64    `json:"deadline"`
	IdempotencyKey      string   `json:"idempotencyKey"`
}

type pancakeSignerSwapResponse struct {
	TxHash  string `json:"txHash"`
	From    string `json:"from"`
	Network string `json:"network"`
}

func (w *SwapWorker) executePancakeSwap(ctx context.Context, swapID, userID, fromAsset, toAsset string, fromAmount, slippage float64, feeBPS int) (*pancakeSwapResult, error) {
	_ = slippage
	_ = feeBPS
	if w == nil || w.cfg == nil || !w.cfg.MobileSwapPancakeEnabled {
		return nil, fmt.Errorf("pancake swap desabilitado")
	}
	if fromAmount <= 0 {
		return nil, fmt.Errorf("amount deve ser positivo")
	}
	if slippage <= 0 {
		slippage = 0.005
	}
	if !common.IsHexAddress(w.cfg.PancakeV2Router) {
		return nil, fmt.Errorf("PANCAKE_V2_ROUTER invalido")
	}
	instruction, err := w.pancakeSwapInstruction(ctx, swapID)
	if err != nil {
		return nil, err
	}
	if time.Now().UTC().After(instruction.ExpiresAt) {
		return nil, fmt.Errorf("quote expirado antes do broadcast")
	}
	if !strings.EqualFold(instruction.Provider, "pancakeswap_v2") || !strings.EqualFold(instruction.Network, "BSC") {
		return nil, fmt.Errorf("provider/network de swap nao permitido")
	}
	if instruction.Router != common.HexToAddress(w.cfg.PancakeV2Router) {
		return nil, fmt.Errorf("router Pancake fora da allowlist")
	}

	fromToken, err := pancakeAssetToken(w.cfg, fromAsset)
	if err != nil {
		return nil, err
	}
	toToken, err := pancakeAssetToken(w.cfg, toAsset)
	if err != nil {
		return nil, err
	}
	if fromToken.Native || toToken.Native {
		return nil, fmt.Errorf("swap com BNB nativo ainda nao suportado na execucao real")
	}
	if len(instruction.Path) != 2 || instruction.Path[0] != fromToken.Contract || instruction.Path[1] != toToken.Contract {
		return nil, fmt.Errorf("path Pancake nao corresponde aos ativos do swap")
	}
	if w.cfg.IsProduction() || !w.cfg.AllowSimulations {
		return w.executePancakeSwapViaSigner(ctx, swapID, userID, fromAmount, toToken, instruction)
	}

	wallet, encryptedKey, err := w.mobileSwapWalletKey(ctx, userID)
	if err != nil {
		return nil, err
	}
	privateKey, err := w.decryptMobileSwapPrivateKey(encryptedKey)
	if err != nil {
		return nil, err
	}
	fromAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
	if !strings.EqualFold(fromAddress.Hex(), wallet.Hex()) {
		return nil, fmt.Errorf("chave custodial nao corresponde ao wallet_address do usuario")
	}

	client, err := w.pancakeClient(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	router := common.HexToAddress(w.cfg.PancakeV2Router)
	amountsOut, err := pancakeAmountsOut(ctx, client, router, instruction.AmountInRaw, instruction.Path)
	if err != nil {
		return nil, err
	}
	if len(amountsOut) == 0 || amountsOut[len(amountsOut)-1].Sign() <= 0 {
		return nil, fmt.Errorf("Pancake retornou saida vazia")
	}
	if amountsOut[len(amountsOut)-1].Cmp(instruction.MinReceivedRaw) < 0 {
		return nil, fmt.Errorf("slippage excedido antes do broadcast")
	}
	if instruction.MinReceivedRaw.Sign() <= 0 {
		return nil, fmt.Errorf("amountOutMin invalido")
	}

	erc20ABI, err := abi.JSON(strings.NewReader(pancakeERC20ABIJSON))
	if err != nil {
		return nil, err
	}
	chainID := big.NewInt(w.pancakeChainID())
	allowance, err := pancakeAllowance(ctx, client, erc20ABI, fromToken.Contract, wallet, router)
	if err != nil {
		return nil, fmt.Errorf("falha ao consultar allowance: %w", err)
	}
	if allowance.Cmp(instruction.AmountInRaw) < 0 {
		approveData, err := erc20ABI.Pack("approve", router, instruction.AmountInRaw)
		if err != nil {
			return nil, err
		}
		if _, err := sendPancakeTransaction(ctx, client, privateKey, fromToken.Contract, big.NewInt(0), approveData, chainID); err != nil {
			return nil, fmt.Errorf("falha ao aprovar router Pancake: %w", err)
		}
	}

	routerABI, err := abi.JSON(strings.NewReader(pancakeV2RouterABIJSON))
	if err != nil {
		return nil, err
	}
	deadline := big.NewInt(instruction.ExpiresAt.Unix())
	swapData, err := routerABI.Pack("swapExactTokensForTokens", instruction.AmountInRaw, instruction.MinReceivedRaw, instruction.Path, wallet, deadline)
	if err != nil {
		return nil, err
	}
	if _, err := w.db.SQL.ExecContext(ctx,
		"UPDATE swaps SET status='broadcast', broadcast_at=NOW(), updated_at=NOW() WHERE id=$1 AND status='signing'", swapID); err != nil {
		return nil, fmt.Errorf("falha ao marcar broadcast: %w", err)
	}
	swapHash, err := sendPancakeTransaction(ctx, client, privateKey, router, big.NewInt(0), swapData, chainID)
	if err != nil {
		return nil, fmt.Errorf("falha ao enviar swap Pancake: %w", err)
	}

	toAmount := tokenAmountFloat(instruction.ExpectedOutRaw, toToken.Decimals)
	return &pancakeSwapResult{
		ToAmount: toAmount,
		Rate:     toAmount / fromAmount,
		TxHash:   swapHash,
	}, nil
}

func (w *SwapWorker) executePancakeSwapViaSigner(ctx context.Context, swapID, userID string, fromAmount float64, toToken pancakeToken, instruction *pancakeSwapInstruction) (*pancakeSwapResult, error) {
	if strings.TrimSpace(w.cfg.SignerUrl) == "" {
		return nil, fmt.Errorf("SIGNER_URL obrigatorio para swap Pancake real")
	}
	if strings.TrimSpace(w.cfg.SignerHmacSecret) == "" {
		return nil, fmt.Errorf("SIGNER_HMAC_SECRET obrigatorio para swap Pancake real")
	}
	wallet, encryptedKey, err := w.mobileSwapWalletKey(ctx, userID)
	if err != nil {
		return nil, err
	}
	path := make([]string, 0, len(instruction.Path))
	for _, token := range instruction.Path {
		path = append(path, token.Hex())
	}
	idempotencyKey := strings.TrimSpace(instruction.ExecutionID)
	if idempotencyKey == "" {
		idempotencyKey = "swap:" + swapID
	}
	payload := pancakeSignerSwapRequest{
		WalletAddress:       wallet.Hex(),
		EncryptedPrivateKey: encryptedKey,
		Network:             "BSC",
		Router:              instruction.Router.Hex(),
		Path:                path,
		AmountInRaw:         instruction.AmountInRaw.String(),
		ExpectedOutRaw:      instruction.ExpectedOutRaw.String(),
		AmountOutMinRaw:     instruction.MinReceivedRaw.String(),
		Deadline:            instruction.ExpiresAt.Unix(),
		IdempotencyKey:      idempotencyKey,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(w.cfg.SignerUrl, "/") + "/mobile/swap/pancake-v2/execute"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, w.cfg.SignerHmacSecret, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao chamar signer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("signer recusou swap Pancake (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var decoded pancakeSignerSwapResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("resposta invalida do signer: %w", err)
	}
	if strings.TrimSpace(decoded.TxHash) == "" {
		return nil, fmt.Errorf("signer retornou txHash vazio")
	}
	toAmount := tokenAmountFloat(instruction.ExpectedOutRaw, toToken.Decimals)
	return &pancakeSwapResult{
		ToAmount: toAmount,
		Rate:     toAmount / fromAmount,
		TxHash:   decoded.TxHash,
	}, nil
}

func (w *SwapWorker) mobileSwapWalletKey(ctx context.Context, userID string) (common.Address, string, error) {
	var walletAddress, encryptedKey string
	err := w.db.SQL.QueryRowContext(ctx, `
		SELECT u.wallet_address, k.encrypted_private_key
		FROM users u
		JOIN mobile_wallet_keys k ON k.user_id = u.id AND lower(k.wallet_address) = lower(u.wallet_address)
		WHERE u.id = $1 AND u.deleted_at IS NULL`, userID).Scan(&walletAddress, &encryptedKey)
	if err == sql.ErrNoRows {
		return common.Address{}, "", fmt.Errorf("wallet custodial nao encontrada para executar swap")
	}
	if err != nil {
		return common.Address{}, "", err
	}
	if !common.IsHexAddress(walletAddress) {
		return common.Address{}, "", fmt.Errorf("wallet_address invalido")
	}
	return common.HexToAddress(walletAddress), encryptedKey, nil
}

func (w *SwapWorker) decryptMobileSwapPrivateKey(encryptedKey string) (*ecdsa.PrivateKey, error) {
	secret := strings.TrimSpace(os.Getenv("MOBILE_WALLET_ENCRYPTION_SECRET"))
	if secret == "" && w.cfg != nil {
		secret = strings.TrimSpace(w.cfg.LGPDSecret)
	}
	if secret == "" && w.cfg != nil {
		secret = strings.TrimSpace(w.cfg.WebhookSecret)
	}
	if secret == "" {
		return nil, fmt.Errorf("MOBILE_WALLET_ENCRYPTION_SECRET nao configurado")
	}
	codec, err := privacy.New(secret)
	if err != nil {
		return nil, err
	}
	plain, err := codec.Decrypt(encryptedKey)
	if err != nil {
		return nil, err
	}
	return crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(plain), "0x"))
}

func (w *SwapWorker) pancakeClient(ctx context.Context) (*ethclient.Client, error) {
	var lastErr error
	for _, rpcURL := range splitCSV(w.cfg.BscRpcUrls) {
		callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		client, err := ethclient.DialContext(callCtx, rpcURL)
		cancel()
		if err == nil {
			return client, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("BSC_RPC_URLS nao configurado")
}

func pancakeAmountsOut(ctx context.Context, client *ethclient.Client, router common.Address, amountIn *big.Int, path []common.Address) ([]*big.Int, error) {
	parsed, err := abi.JSON(strings.NewReader(pancakeV2RouterABIJSON))
	if err != nil {
		return nil, err
	}
	data, err := parsed.Pack("getAmountsOut", amountIn, path)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := client.CallContract(callCtx, ethereum.CallMsg{To: &router, Data: data}, nil)
	if err != nil {
		return nil, err
	}
	out, err := parsed.Unpack("getAmountsOut", raw)
	if err != nil {
		return nil, err
	}
	amounts, ok := out[0].([]*big.Int)
	if !ok {
		return nil, fmt.Errorf("resposta Pancake invalida")
	}
	return amounts, nil
}

func pancakeAllowance(ctx context.Context, client *ethclient.Client, erc20ABI abi.ABI, token, owner, spender common.Address) (*big.Int, error) {
	data, err := erc20ABI.Pack("allowance", owner, spender)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	raw, err := client.CallContract(callCtx, ethereum.CallMsg{To: &token, Data: data}, nil)
	if err != nil {
		return nil, err
	}
	out, err := erc20ABI.Unpack("allowance", raw)
	if err != nil {
		return nil, err
	}
	allowance, ok := out[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("resposta allowance invalida")
	}
	return allowance, nil
}

func sendPancakeTransaction(ctx context.Context, client *ethclient.Client, key *ecdsa.PrivateKey, to common.Address, value *big.Int, data []byte, chainID *big.Int) (string, error) {
	from := crypto.PubkeyToAddress(key.PublicKey)
	txCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	nonce, err := client.PendingNonceAt(txCtx, from)
	if err != nil {
		return "", err
	}
	gasPrice, err := client.SuggestGasPrice(txCtx)
	if err != nil {
		return "", err
	}
	gasLimit, err := client.EstimateGas(txCtx, ethereum.CallMsg{From: from, To: &to, Value: value, Data: data})
	if err != nil {
		return "", err
	}
	gasLimit = gasLimit + gasLimit/5
	tx := types.NewTransaction(nonce, to, value, gasLimit, gasPrice, data)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return "", err
	}
	if err := client.SendTransaction(txCtx, signed); err != nil {
		return "", err
	}
	if err := waitPancakeReceipt(ctx, client, signed.Hash()); err != nil {
		return "", err
	}
	return signed.Hash().Hex(), nil
}

func waitPancakeReceipt(ctx context.Context, client *ethclient.Client, hash common.Hash) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		receipt, err := client.TransactionReceipt(ctx, hash)
		if err == nil && receipt != nil {
			if receipt.Status != types.ReceiptStatusSuccessful {
				return fmt.Errorf("transacao %s revertida", hash.Hex())
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout aguardando transacao %s", hash.Hex())
		case <-ticker.C:
		}
	}
}

func (w *SwapWorker) pancakeSwapInstruction(ctx context.Context, swapID string) (*pancakeSwapInstruction, error) {
	var provider, network, router, amountInRaw, expectedOutRaw, minReceivedRaw, executionID string
	var path []string
	var slippageBPS sql.NullInt64
	var expiresAt time.Time
	err := w.db.SQL.QueryRowContext(ctx, `
		SELECT COALESCE(provider,''), COALESCE(network,''), COALESCE(router,''), COALESCE(path, ARRAY[]::text[]),
		       COALESCE(amount_in_raw,''), COALESCE(expected_out_raw,''), COALESCE(min_received_raw,''),
		       slippage_bps, quote_expires_at, COALESCE(execution_id,'')
		FROM swaps WHERE id=$1 AND status='signing'`, swapID).Scan(
		&provider, &network, &router, pq.Array(&path), &amountInRaw, &expectedOutRaw, &minReceivedRaw,
		&slippageBPS, &expiresAt, &executionID)
	if err != nil {
		return nil, err
	}
	if !common.IsHexAddress(router) {
		return nil, fmt.Errorf("router invalido no quote")
	}
	parsedPath := make([]common.Address, 0, len(path))
	for _, item := range path {
		if !common.IsHexAddress(item) {
			return nil, fmt.Errorf("path contem endereco invalido")
		}
		parsedPath = append(parsedPath, common.HexToAddress(item))
	}
	amountIn, ok := new(big.Int).SetString(strings.TrimSpace(amountInRaw), 10)
	if !ok || amountIn.Sign() <= 0 {
		return nil, fmt.Errorf("amount_in_raw invalido")
	}
	expectedOut, ok := new(big.Int).SetString(strings.TrimSpace(expectedOutRaw), 10)
	if !ok || expectedOut.Sign() <= 0 {
		return nil, fmt.Errorf("expected_out_raw invalido")
	}
	minReceived, ok := new(big.Int).SetString(strings.TrimSpace(minReceivedRaw), 10)
	if !ok || minReceived.Sign() <= 0 {
		return nil, fmt.Errorf("min_received_raw invalido")
	}
	return &pancakeSwapInstruction{
		Provider:       provider,
		Network:        network,
		Router:         common.HexToAddress(router),
		Path:           parsedPath,
		AmountInRaw:    amountIn,
		ExpectedOutRaw: expectedOut,
		MinReceivedRaw: minReceived,
		SlippageBPS:    int(slippageBPS.Int64),
		ExpiresAt:      expiresAt.UTC(),
		ExecutionID:    executionID,
	}, nil
}

func (w *SwapWorker) pancakeChainID() int64 {
	if w != nil && w.cfg != nil && w.cfg.BscChainID > 0 {
		return w.cfg.BscChainID
	}
	return 56
}

func pancakeAssetToken(cfg *config.Config, asset string) (pancakeToken, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "BNB" {
		wbnb := ""
		if cfg != nil {
			wbnb = strings.TrimSpace(cfg.PancakeWBNBContract)
		}
		if !common.IsHexAddress(wbnb) {
			return pancakeToken{}, fmt.Errorf("PANCAKE_WBNB_CONTRACT invalido")
		}
		return pancakeToken{Asset: asset, Contract: common.HexToAddress(wbnb), Decimals: 18, Native: true}, nil
	}
	contract, decimals, ok := defaultPancakeBSCToken(asset)
	if asset == "USDT" && cfg != nil && common.IsHexAddress(cfg.BscUsdtContract) {
		contract = cfg.BscUsdtContract
		decimals = 18
		ok = true
	}
	if !ok || !common.IsHexAddress(contract) {
		return pancakeToken{}, fmt.Errorf("%s nao suportado no Pancake BSC", asset)
	}
	return pancakeToken{Asset: asset, Contract: common.HexToAddress(contract), Decimals: decimals}, nil
}

func defaultPancakeBSCToken(asset string) (string, int, bool) {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case "USDT":
		return "0x55d398326f99059fF775485246999027B3197955", 18, true
	case "USDC":
		return "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d", 18, true
	case "BTC", "BTCB":
		return "0x7130d2A12B9BCbFAe4f2634d864A1Ee1Ce3Ead9c", 18, true
	case "ETH":
		return "0x2170Ed0880ac9A755fd29B2688956BD959F933F8", 18, true
	case "LINK":
		return "0xF8A0BF9cF54Bb92F17374d9e9A321E6a111a51bD", 18, true
	case "AVAX":
		return "0x1CE0c2827e2eF14D5C4f29a091d735A204794041", 18, true
	default:
		return "", 0, false
	}
}
