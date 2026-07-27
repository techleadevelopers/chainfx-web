package mobile

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/liquidity"
	"payment-gateway/internal/privacy"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"golang.org/x/crypto/bcrypt"
)

const (
	bscUSDCContractMobile = "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d"
	// Native USDC on Polygon (Circle, 2024+). The former 0x2791… was bridged USDC.e (deprecated).
	polygonUSDCContractMobile = "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"
	defaultPolygonUSDTMobile  = "0xc2132D05D31c914a87C6611C10748AEb04B58e8F"
	bscETHContractMobile      = "0x2170Ed0880ac9A755fd29B2688956BD959F933F8"
	polygonETHContractMobile  = "0x7ceB23fD6bC0adD59E62ac25578270cFf1b9f619"
	bscLINKContractMobile     = "0xF8A0BF9cF54Bb92F17374d9e9A321E6a111a51bD"
	polygonLINKContractMobile = "0x53E0bca35eC356BD5ddDFebbd1Fc0fD03FaBad39"
	bscAVAXContractMobile     = "0x1CE0c2827e2eF14D5C4f29a091d735A204794041"
	polygonAVAXContractMobile = "0x2C89bbc92BD86F8075d1DEcc58C7F4E0107f286b"
)

func (s *Server) handleWalletTransfer(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao preparar wallet custodial"})
		return
	}
	if !mobileUserKYCApproved(user) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":     "kyc obrigatorio para enviar cripto",
			"kycStatus": user.KYCStatus,
		})
		return
	}
	if user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet do usuario nao registrada"})
		return
	}

	var req struct {
		To      string `json:"to"`
		Amount  string `json:"amount"`
		Asset   string `json:"asset"`
		Network string `json:"network"`
		PIN     string `json:"pin"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}

	to := strings.TrimSpace(req.To)
	if !common.IsHexAddress(to) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "to deve ser um endereco EVM valido"})
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(req.Asset))
	if asset == "" {
		asset = "USDT"
	}
	network := normalizeMobileTransferNetwork(req.Network)
	if network == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network EVM nao suportada ou desabilitada"})
		return
	}

	token, decimals, chainID, err := s.mobileTransferToken(asset, network)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	nativeTransfer := token == "" && liquidity.IsNativeAsset(asset, network)
	rawAmount, err := parseTokenAmount(req.Amount, decimals)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if user.PinHash != nil && strings.TrimSpace(*user.PinHash) != "" {
		if strings.TrimSpace(req.PIN) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "pin obrigatorio para transferencia"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(*user.PinHash), []byte(req.PIN)); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "pin invalido"})
			return
		}
	}

	from := common.HexToAddress(*user.WalletAddress).Hex()
	keyRecord, err := mobileDB(s.db).GetCustodialWalletKey(r.Context(), user.ID, from)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao buscar chave custodial"})
		return
	}
	if keyRecord == nil || strings.TrimSpace(keyRecord.EncryptedPrivateKey) == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":          "wallet sem chave custodial no backend",
			"wallet_address": from,
			"next_step":      "importe a private key criptografada ou use uma wallet custodial criada pelo app",
		})
		return
	}

	recipient := common.HexToAddress(to)
	var tokenAddress common.Address
	if !nativeTransfer {
		tokenAddress = common.HexToAddress(token)
	}
	tokenContract := tokenAddress.Hex()
	mode := "backend_custodial_erc20_transfer"
	if nativeTransfer {
		tokenContract = ""
		mode = "backend_custodial_native_evm_transfer"
	}

	// Pre-record the transfer as pending BEFORE broadcasting on-chain.
	// This ensures that if the HTTP response fails after a successful broadcast,
	// the idempotency key will block a duplicate retry from double-sending.
	idempKey := idempotencyKeyFromCtx(r.Context())
	if strings.TrimSpace(idempKey) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "idempotency key obrigatorio"})
		return
	}
	if existing, ok, err := s.mobileTransferByIdempotency(r.Context(), user.ID, idempKey); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao verificar idempotencia da transferencia"})
		return
	} else if ok {
		status := strings.TrimSpace(existing["status"].(string))
		httpStatus := http.StatusAccepted
		if status == "failed" {
			httpStatus = http.StatusConflict
		}
		writeJSON(w, httpStatus, existing)
		return
	}
	if err := mobileDB(s.db).RecordMobileWalletTransfer(
		r.Context(),
		user.ID,
		from,
		recipient.Hex(),
		tokenContract,
		asset,
		network,
		req.Amount,
		rawAmount.String(),
		"pending:"+idempKey, // placeholder until broadcast confirms
		idempKey,
	); err != nil {
		if errors.Is(err, errMobileTransferAlreadyRecorded) {
			if existing, ok, lookupErr := s.mobileTransferByIdempotency(r.Context(), user.ID, idempKey); lookupErr == nil && ok {
				writeJSON(w, http.StatusAccepted, existing)
				return
			}
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "erro ao registrar transferencia pendente"})
		return
	}

	var txHash string
	if nativeTransfer {
		txHash, err = s.sendCustodialMobileNativeTransfer(
			r.Context(),
			keyRecord.EncryptedPrivateKey,
			from,
			recipient,
			rawAmount,
			network,
			int64(chainID),
		)
	} else {
		txHash, err = s.sendCustodialMobileERC20Transfer(
			r.Context(),
			keyRecord.EncryptedPrivateKey,
			from,
			recipient,
			tokenAddress,
			rawAmount,
			network,
			int64(chainID),
		)
	}
	if err != nil {
		// Update the pending record with the failure so the idempotency entry reflects reality.
		_, _ = s.db.SQL.ExecContext(r.Context(), `
			UPDATE mobile_wallet_transfers
			SET status = 'failed'
			WHERE user_id = $1::uuid AND idempotency_key = $2
		`, user.ID, idempKey)
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	// Broadcast succeeded — update the record with the real txHash.
	res, err := s.db.SQL.ExecContext(r.Context(), `
		UPDATE mobile_wallet_transfers
		SET tx_hash = $1, status = 'submitted'
		WHERE user_id = $2::uuid AND idempotency_key = $3
	`, txHash, user.ID, idempKey)
	if err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"mode":        mode,
			"tx_hash":     txHash,
			"status":      "submitted",
			"audit_error": "broadcast concluido, mas falhou atualizar auditoria local",
		})
		return
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"mode":        mode,
			"tx_hash":     txHash,
			"status":      "submitted",
			"audit_error": "broadcast concluido, mas registro pendente nao foi encontrado",
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"mode":           mode,
		"from":           from,
		"tx_hash":        txHash,
		"chainId":        chainID,
		"network":        network,
		"asset":          asset,
		"token_contract": tokenContract,
		"recipient":      recipient.Hex(),
		"amount":         req.Amount,
		"amount_raw":     rawAmount.String(),
		"decimals":       decimals,
		"status":         "submitted",
	})
}

func (s *Server) mobileTransferByIdempotency(ctx context.Context, userID, idempotencyKey string) (map[string]any, bool, error) {
	var txHash, status, asset, network, amount, amountRaw, toAddress, tokenContract string
	err := s.db.SQL.QueryRowContext(ctx, `
		SELECT tx_hash, status, asset, network, amount, amount_raw, to_address, token_contract
		FROM mobile_wallet_transfers
		WHERE user_id = $1::uuid AND idempotency_key = $2
		LIMIT 1
	`, userID, idempotencyKey).Scan(&txHash, &status, &asset, &network, &amount, &amountRaw, &toAddress, &tokenContract)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return map[string]any{
		"mode":           "backend_custodial_transfer",
		"tx_hash":        txHash,
		"status":         status,
		"network":        network,
		"asset":          asset,
		"token_contract": tokenContract,
		"recipient":      toAddress,
		"amount":         amount,
		"amount_raw":     amountRaw,
		"idempotent":     true,
	}, true, nil
}

func (s *Server) handleWalletTransferQuote(w http.ResponseWriter, r *http.Request) {
	uid := userIDFromCtx(r)
	user, err := mobileDB(s.db).GetUserByID(r.Context(), uid)
	if err != nil || user == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "usuario nao encontrado"})
		return
	}
	user, err = s.ensureUserWallet(r.Context(), user)
	if err != nil || user.WalletAddress == nil || strings.TrimSpace(*user.WalletAddress) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "wallet do usuario nao registrada"})
		return
	}
	var req struct {
		To      string `json:"to"`
		Amount  string `json:"amount"`
		Asset   string `json:"asset"`
		Network string `json:"network"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "payload invalido"})
		return
	}
	to := strings.TrimSpace(req.To)
	if !common.IsHexAddress(to) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "to deve ser um endereco EVM valido"})
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(req.Asset))
	if asset == "" {
		asset = "USDT"
	}
	network := normalizeMobileTransferNetwork(req.Network)
	if network == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network EVM nao suportada ou desabilitada"})
		return
	}
	token, decimals, chainID, err := s.mobileTransferToken(asset, network)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	nativeTransfer := token == "" && liquidity.IsNativeAsset(asset, network)
	rawAmount, err := parseTokenAmount(req.Amount, decimals)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rpcURLs := s.mobileTransferRPCURLs(network)
	if len(rpcURLs) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": network + " RPC nao configurado para cotacao de transferencia"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	from := common.HexToAddress(*user.WalletAddress)
	recipient := common.HexToAddress(to)
	tokenAddress := common.HexToAddress(token)
	toAddress := tokenAddress
	value := big.NewInt(0)
	var data []byte
	if nativeTransfer {
		toAddress = recipient
		value = rawAmount
	} else {
		data = common.FromHex(erc20TransferCalldata(recipient, rawAmount))
	}

	// Try each RPC URL in order — failover so a down primary node does not block
	// fee estimation for all users (mirrors sendCustodialMobileERC20Transfer).
	var gasPrice *big.Int
	var gasLimit uint64
	var quoteRPCErr error
	for _, rpcURL := range rpcURLs {
		var c *ethclient.Client
		c, quoteRPCErr = ethclient.DialContext(ctx, rpcURL)
		if quoteRPCErr != nil {
			continue
		}
		gasPrice, quoteRPCErr = c.SuggestGasPrice(ctx)
		if quoteRPCErr != nil {
			c.Close()
			continue
		}
		gasLimit, quoteRPCErr = c.EstimateGas(ctx, ethereum.CallMsg{From: from, To: &toAddress, Value: value, Data: data})
		c.Close()
		if quoteRPCErr == nil {
			break
		}
	}
	if quoteRPCErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "falha ao estimar gas: todos os RPCs indisponiveis"})
		return
	}
	minGasLimit := uint64(65_000)
	if nativeTransfer {
		minGasLimit = 21_000
	}
	if gasLimit < minGasLimit {
		gasLimit = minGasLimit
	}
	gasLimitWithBuffer := gasLimit + gasLimit/5
	feeWei := new(big.Int).Mul(new(big.Int).SetUint64(gasLimitWithBuffer), gasPrice)
	feeNative := bigIntToFloat(feeWei, 18)
	nativeSymbol := mobileTransferNativeSymbol(network)
	feeBRL := 0.0
	if nativeSymbol != "" {
		if nativePrice := mobileAssetPriceBRL(s.PriceCache(), nativeSymbol); nativePrice > 0 {
			feeBRL = feeNative * nativePrice
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"asset":     asset,
		"network":   network,
		"chainId":   chainID,
		"from":      from.Hex(),
		"recipient": recipient.Hex(),
		"token_contract": func() string {
			if nativeTransfer {
				return ""
			}
			return tokenAddress.Hex()
		}(),
		"amount":                    req.Amount,
		"amount_raw":                rawAmount.String(),
		"decimals":                  decimals,
		"gas_limit":                 gasLimitWithBuffer,
		"gas_price_wei":             gasPrice.String(),
		"estimated_network_fee_wei": feeWei.String(),
		"estimated_network_fee":     feeNative,
		"network_fee_symbol":        nativeSymbol,
		"estimated_fee_brl":         feeBRL,
		"requires_pin":              user.PinHash != nil && strings.TrimSpace(*user.PinHash) != "",
	})
}

func (s *Server) sendCustodialMobileERC20Transfer(ctx context.Context, encryptedPrivateKey, expectedFrom string, recipient, token common.Address, amount *big.Int, network string, expectedChainID int64) (string, error) {
	codec, err := privacy.New(s.mobileWalletEncryptionSecret())
	if err != nil {
		return "", err
	}
	privateKeyHex, err := codec.Decrypt(encryptedPrivateKey)
	if err != nil {
		return "", fmt.Errorf("falha ao abrir chave custodial")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return "", fmt.Errorf("chave custodial invalida")
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	if !strings.EqualFold(from.Hex(), expectedFrom) {
		return "", fmt.Errorf("chave custodial nao corresponde a wallet do usuario")
	}

	rpcURLs := s.mobileTransferRPCURLs(network)
	if len(rpcURLs) == 0 {
		return "", fmt.Errorf("%s RPC nao configurado para transferencia mobile", network)
	}
	txCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	// Try each RPC URL in order — failover when the primary node is down.
	var client *ethclient.Client
	var dialErr error
	for _, rpcURL := range rpcURLs {
		c, err := ethclient.DialContext(txCtx, rpcURL)
		if err == nil {
			client = c
			break
		}
		dialErr = fmt.Errorf("RPC %s: %w", rpcURL, err)
	}
	if client == nil {
		return "", fmt.Errorf("falha ao conectar RPC %s: %w", network, dialErr)
	}
	defer client.Close()

	chainID, err := client.ChainID(txCtx)
	if err != nil {
		return "", fmt.Errorf("falha ao ler chainId: %w", err)
	}
	if expectedChainID > 0 && chainID.Int64() != expectedChainID {
		return "", fmt.Errorf("chainId invalido: esperado %d recebido %d", expectedChainID, chainID.Int64())
	}
	nonce, err := client.PendingNonceAt(txCtx, from)
	if err != nil {
		return "", fmt.Errorf("falha ao ler nonce: %w", err)
	}
	data := common.FromHex(erc20TransferCalldata(recipient, amount))
	gasPrice, err := client.SuggestGasPrice(txCtx)
	if err != nil {
		return "", fmt.Errorf("falha ao estimar gas price: %w", err)
	}
	gasLimit, err := client.EstimateGas(txCtx, ethereum.CallMsg{From: from, To: &token, Value: big.NewInt(0), Data: data})
	if err != nil {
		return "", fmt.Errorf("falha ao estimar gas: %w", err)
	}
	if gasLimit < 65_000 {
		gasLimit = 65_000
	}
	tx := types.NewTransaction(nonce, token, big.NewInt(0), gasLimit+gasLimit/5, gasPrice, data)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return "", fmt.Errorf("falha ao assinar transferencia: %w", err)
	}
	if err := client.SendTransaction(txCtx, signed); err != nil {
		return "", fmt.Errorf("falha ao enviar transferencia: %w", err)
	}
	return signed.Hash().Hex(), nil
}

func (s *Server) sendCustodialMobileNativeTransfer(ctx context.Context, encryptedPrivateKey, expectedFrom string, recipient common.Address, amount *big.Int, network string, expectedChainID int64) (string, error) {
	codec, err := privacy.New(s.mobileWalletEncryptionSecret())
	if err != nil {
		return "", err
	}
	privateKeyHex, err := codec.Decrypt(encryptedPrivateKey)
	if err != nil {
		return "", fmt.Errorf("falha ao abrir chave custodial")
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(privateKeyHex), "0x"))
	if err != nil {
		return "", fmt.Errorf("chave custodial invalida")
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	if !strings.EqualFold(from.Hex(), expectedFrom) {
		return "", fmt.Errorf("chave custodial nao corresponde a wallet do usuario")
	}

	rpcURLs := s.mobileTransferRPCURLs(network)
	if len(rpcURLs) == 0 {
		return "", fmt.Errorf("%s RPC nao configurado para transferencia mobile", network)
	}
	txCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var client *ethclient.Client
	var dialErr error
	for _, rpcURL := range rpcURLs {
		c, err := ethclient.DialContext(txCtx, rpcURL)
		if err == nil {
			client = c
			break
		}
		dialErr = fmt.Errorf("RPC %s: %w", rpcURL, err)
	}
	if client == nil {
		return "", fmt.Errorf("falha ao conectar RPC %s: %w", network, dialErr)
	}
	defer client.Close()

	chainID, err := client.ChainID(txCtx)
	if err != nil {
		return "", fmt.Errorf("falha ao ler chainId: %w", err)
	}
	if expectedChainID > 0 && chainID.Int64() != expectedChainID {
		return "", fmt.Errorf("chainId invalido: esperado %d recebido %d", expectedChainID, chainID.Int64())
	}
	nonce, err := client.PendingNonceAt(txCtx, from)
	if err != nil {
		return "", fmt.Errorf("falha ao ler nonce: %w", err)
	}
	gasPrice, err := client.SuggestGasPrice(txCtx)
	if err != nil {
		return "", fmt.Errorf("falha ao estimar gas price: %w", err)
	}
	gasLimit, err := client.EstimateGas(txCtx, ethereum.CallMsg{From: from, To: &recipient, Value: amount})
	if err != nil {
		return "", fmt.Errorf("falha ao estimar gas: %w", err)
	}
	if gasLimit < 21_000 {
		gasLimit = 21_000
	}
	tx := types.NewTransaction(nonce, recipient, amount, gasLimit+gasLimit/5, gasPrice, nil)
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), key)
	if err != nil {
		return "", fmt.Errorf("falha ao assinar transferencia: %w", err)
	}
	if err := client.SendTransaction(txCtx, signed); err != nil {
		return "", fmt.Errorf("falha ao enviar transferencia: %w", err)
	}
	return signed.Hash().Hex(), nil
}

func normalizeMobileTransferNetwork(network string) string {
	normalized := liquidity.NormalizeNetwork(network)
	if strings.TrimSpace(network) == "" {
		normalized = "BSC"
	}
	if !liquidity.IsEVMNetwork(normalized) {
		return ""
	}
	return normalized
}

func (s *Server) mobileTransferToken(asset, network string) (string, int, int, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	network = normalizeMobileTransferNetwork(network)
	if network == "" {
		return "", 0, 0, fmt.Errorf("network EVM nao suportada ou desabilitada")
	}
	if pair, ok := s.resolveMobileLiquidityPair(asset, network); ok {
		pair = liquidity.EnrichPair(pair)
		if pair.TokenStandard == "NATIVE" {
			return "", pair.Decimals, s.mobileTransferChainID(pair.Network), nil
		}
		if pair.TokenStandard == "ERC20" {
			if !common.IsHexAddress(pair.ContractAddress) {
				return "", 0, 0, fmt.Errorf("%s %s contrato ERC20 nao configurado", pair.Network, pair.Asset)
			}
			return pair.ContractAddress, pair.Decimals, s.mobileTransferChainID(pair.Network), nil
		}
	}
	switch network {
	case "BSC":
		switch asset {
		case "USDT":
			if s.cfg == nil || !common.IsHexAddress(s.cfg.BscUsdtContract) {
				return "", 0, 0, fmt.Errorf("BSC USDT nao configurado")
			}
			return s.cfg.BscUsdtContract, 18, s.mobileTransferChainID("BSC"), nil
		case "USDC":
			return bscUSDCContractMobile, 18, s.mobileTransferChainID("BSC"), nil
		case "ETH":
			return bscETHContractMobile, 18, s.mobileTransferChainID("BSC"), nil
		case "LINK":
			return bscLINKContractMobile, 18, s.mobileTransferChainID("BSC"), nil
		case "AVAX":
			return bscAVAXContractMobile, 18, s.mobileTransferChainID("BSC"), nil
		}
	case "POLYGON":
		switch asset {
		case "USDT":
			token := defaultPolygonUSDTMobile
			if s.cfg != nil && common.IsHexAddress(s.cfg.PolygonUsdtContract) {
				token = s.cfg.PolygonUsdtContract
			}
			return token, 6, s.mobileTransferChainID("POLYGON"), nil
		case "USDC":
			return polygonUSDCContractMobile, 6, s.mobileTransferChainID("POLYGON"), nil
		case "ETH":
			return polygonETHContractMobile, 18, s.mobileTransferChainID("POLYGON"), nil
		case "LINK":
			return polygonLINKContractMobile, 18, s.mobileTransferChainID("POLYGON"), nil
		case "AVAX":
			return polygonAVAXContractMobile, 18, s.mobileTransferChainID("POLYGON"), nil
		}
	case "BASE":
		if asset == "USDC" && s.cfg != nil && common.IsHexAddress(s.cfg.BaseUsdcContract) {
			return s.cfg.BaseUsdcContract, 6, s.mobileTransferChainID("BASE"), nil
		}
	case "ARBITRUM":
		if asset == "USDC" && s.cfg != nil && common.IsHexAddress(s.cfg.ArbitrumUsdcContract) {
			return s.cfg.ArbitrumUsdcContract, 6, s.mobileTransferChainID("ARBITRUM"), nil
		}
	case "ETHEREUM":
		if asset == "USDC" && s.cfg != nil && common.IsHexAddress(s.cfg.EthereumUsdcContract) {
			return s.cfg.EthereumUsdcContract, 6, s.mobileTransferChainID("ETHEREUM"), nil
		}
	}
	return "", 0, 0, fmt.Errorf("asset/network nao suportado para transferencia mobile")
}

func (s *Server) mobileTransferChainID(network string) int {
	if s == nil || s.cfg == nil {
		switch network {
		case "POLYGON":
			return 137
		case "BASE":
			return 8453
		case "ARBITRUM":
			return 42161
		case "ETHEREUM":
			return 1
		}
		return 56
	}
	switch network {
	case "POLYGON":
		if s.cfg.PolygonChainID > 0 {
			return int(s.cfg.PolygonChainID)
		}
		return 137
	case "BASE":
		if s.cfg.BaseChainID > 0 {
			return int(s.cfg.BaseChainID)
		}
		return 8453
	case "ARBITRUM":
		if s.cfg.ArbitrumChainID > 0 {
			return int(s.cfg.ArbitrumChainID)
		}
		return 42161
	case "ETHEREUM":
		if s.cfg.EthereumChainID > 0 {
			return int(s.cfg.EthereumChainID)
		}
		return 1
	default:
		if s.cfg.BscChainID > 0 {
			return int(s.cfg.BscChainID)
		}
		return 56
	}
}

// mobileTransferRPCURL returns the first configured RPC URL for backward compatibility.
func (s *Server) mobileTransferRPCURL(network string) string {
	urls := s.mobileTransferRPCURLs(network)
	if len(urls) == 0 {
		return ""
	}
	return urls[0]
}

// mobileTransferRPCURLs returns all CSV-separated RPC URLs for the network,
// enabling the caller to iterate and failover when the primary node is unavailable.
func (s *Server) mobileTransferRPCURLs(network string) []string {
	if s == nil || s.cfg == nil {
		return nil
	}
	switch network {
	case "POLYGON":
		return allCSVValues(s.cfg.PolygonRpcUrls)
	case "BASE":
		return allCSVValues(s.cfg.BaseRpcUrls)
	case "ARBITRUM":
		return allCSVValues(s.cfg.ArbitrumRpcUrls)
	case "ETHEREUM":
		return allCSVValues(s.cfg.EthereumRpcUrls)
	default:
		return allCSVValues(s.cfg.BscRpcUrls)
	}
}

// allCSVValues splits a comma-separated string and returns all non-empty values.
func allCSVValues(raw string) []string {
	var out []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func mobileTransferNativeSymbol(network string) string {
	switch network {
	case "POLYGON":
		return "MATIC"
	case "BSC":
		return "BNB"
	case "BASE", "ARBITRUM", "ETHEREUM":
		return "ETH"
	default:
		return ""
	}
}

func firstCSVValue(raw string) string {
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func parseTokenAmount(amount string, decimals int) (*big.Int, error) {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return nil, fmt.Errorf("amount obrigatorio")
	}
	rat, ok := new(big.Rat).SetString(amount)
	if !ok || rat.Sign() <= 0 {
		return nil, fmt.Errorf("amount deve ser positivo")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	rat.Mul(rat, new(big.Rat).SetInt(scale))
	if !rat.IsInt() {
		return nil, fmt.Errorf("amount tem mais casas decimais que o token suporta")
	}
	return rat.Num(), nil
}

func erc20TransferCalldata(to common.Address, amount *big.Int) string {
	selector := []byte{0xa9, 0x05, 0x9c, 0xbb}
	data := make([]byte, 0, 68)
	data = append(data, selector...)
	data = append(data, common.LeftPadBytes(to.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(amount.Bytes(), 32)...)
	return "0x" + hex.EncodeToString(data)
}
