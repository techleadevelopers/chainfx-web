package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"

	"payment-gateway/internal/database"
	"payment-gateway/internal/money"
	"payment-gateway/internal/security"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

const agentTradeIntentTTL = 15 * time.Minute

const (
	bscUSDCContract = "0x8ac76a51cc950d9822d68b83fe1ad97b32cd580d"
	bscBUSDContract = "0xe9e7cea3dedca5984780bafc599b69add087d56"
	bscUSDTContract = "0x55d398326f99059ff775485246999027b3197955"
)

type agentTradeAmounts struct {
	PayAmount        float64
	ReceiveAmount    float64
	ChainFXFeeAmount float64
	FeeCalculation   string
}

func (s *Server) handleAgentTradeQuote(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PayAsset          string  `json:"payAsset"`
		ReceiveAsset      string  `json:"receiveAsset"`
		Network           string  `json:"network"`
		Amount            float64 `json:"amount"`
		AmountType        string  `json:"amountType"`
		AgentWallet       string  `json:"agentWallet"`
		DestinationWallet string  `json:"destinationWallet"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	req.PayAsset = strings.ToUpper(strings.TrimSpace(req.PayAsset))
	req.ReceiveAsset = strings.ToUpper(strings.TrimSpace(req.ReceiveAsset))
	req.Network = normalizeStablecoinNetwork(req.Network)
	req.AmountType = strings.ToLower(strings.TrimSpace(firstNonEmpty(req.AmountType, "receive")))
	req.AgentWallet = strings.ToLower(strings.TrimSpace(req.AgentWallet))
	req.DestinationWallet = strings.ToLower(strings.TrimSpace(firstNonEmpty(req.DestinationWallet, req.AgentWallet)))
	if !common.IsHexAddress(req.AgentWallet) || !common.IsHexAddress(req.DestinationWallet) || req.Amount <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agentWallet, destinationWallet e amount validos sao obrigatorios"})
		return
	}
	if req.Network != "BSC" && req.Network != "POLYGON" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "network deve ser BSC ou POLYGON"})
		return
	}
	payAsset, err := s.agentTradeAsset(r.Context(), req.PayAsset, req.Network)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	receiveAsset, err := s.agentTradeAsset(r.Context(), req.ReceiveAsset, req.Network)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if req.PayAsset == req.ReceiveAsset {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "trade M2M precisa trocar ativos diferentes; pagar USDT para receber USDT nao cria valor"})
		return
	}
	feeBps := maxInt(agentGatewayFeeBps, maxInt(payAsset.FeeBps, receiveAsset.FeeBps))
	amounts, err := calculateAgentTradeAmounts(req.Amount, req.AmountType, feeBps)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if amounts.PayAmount < payAsset.MinAmount || amounts.ReceiveAmount < receiveAsset.MinAmount {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("amount abaixo do minimo: pay %.2f %s, receive %.2f %s", payAsset.MinAmount, payAsset.Symbol, receiveAsset.MinAmount, receiveAsset.Symbol)})
		return
	}
	nonce := "tr_" + strings.ReplaceAll(database.NewID(), "-", "")
	paymentAddress := s.accessPaymentAddress()
	if paymentAddress == "" || !common.IsHexAddress(paymentAddress) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "TREASURY_HOT ou SELL_WALLET_ADDRESS nao configurado"})
		return
	}
	expiresAt := time.Now().UTC().Add(agentTradeIntentTTL)
	requestHash := agentTradeRequestHash(req.AgentWallet, req.DestinationWallet, req.PayAsset, req.ReceiveAsset, req.Network, payAsset.ContractAddress, receiveAsset.ContractAddress, amounts.PayAmount, amounts.ReceiveAmount, paymentAddress, nonce, expiresAt)
	intent, err := s.db.CreateAgentTradeIntent(r.Context(), database.AgentTradeIntentInput{
		AgentWallet:          req.AgentWallet,
		PayAsset:             req.PayAsset,
		ReceiveAsset:         req.ReceiveAsset,
		PayAmount:            amounts.PayAmount,
		ReceiveAmount:        amounts.ReceiveAmount,
		ChainFXFeeAmount:     amounts.ChainFXFeeAmount,
		FeeBps:               feeBps,
		Network:              req.Network,
		PaymentAddress:       paymentAddress,
		DestinationWallet:    req.DestinationWallet,
		PayTokenContract:     payAsset.ContractAddress,
		ReceiveTokenContract: receiveAsset.ContractAddress,
		Nonce:                nonce,
		RequestHash:          requestHash,
		TTL:                  agentTradeIntentTTL,
		IdempotencyKey:       strings.TrimSpace(r.Header.Get("X-Idempotency-Key")),
	})
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, agentTradeQuoteResponse(intent, publicBaseURL(r)))
}

func (s *Server) handleAgentAssets(w http.ResponseWriter, r *http.Request) {
	s.writeCachedDiscoveryJSON(w, r, "agent-assets", time.Minute, func() (any, error) {
		assets, err := s.agentTradeAssets(r.Context())
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"defaultNetwork":       "BSC",
			"supportedNetworks":    s.stablecoinPaymentNetworks(),
			"pricingModel":         "stablecoin pairs are quoted 1:1 before ChainFX fee",
			"treasuryInventory":    "receiveAsset settlement requires ChainFX treasury inventory for that token",
			"supportedPairs":       "any enabled stablecoin pair with different symbols",
			"defaultGatewayFeeBps": agentGatewayFeeBps,
			"assets":               assets,
		}, nil
	})
}

func (s *Server) handleAgentTradeExecute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TradeIntentID  string `json:"tradeIntentId"`
		TxHash         string `json:"txHash"`
		RequestHash    string `json:"requestHash"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "JSON invalido"})
		return
	}
	req.TradeIntentID = strings.TrimSpace(req.TradeIntentID)
	req.TxHash = strings.ToLower(strings.TrimSpace(req.TxHash))
	req.IdempotencyKey = firstNonEmpty(req.IdempotencyKey, r.Header.Get("X-Idempotency-Key"))
	if req.TradeIntentID == "" || req.TxHash == "" || req.IdempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tradeIntentId, txHash e idempotencyKey sao obrigatorios"})
		return
	}
	intent, err := s.db.GetAgentTradeIntent(r.Context(), req.TradeIntentID)
	if err != nil {
		writeError(w, err)
		return
	}
	if intent == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "trade intent nao encontrado"})
		return
	}
	if req.RequestHash != "" && !strings.EqualFold(req.RequestHash, intent.RequestHash) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "requestHash nao confere com trade intent"})
		return
	}
	network := normalizeStablecoinNetwork(intent.Network)
	payAsset, err := s.agentTradeAsset(r.Context(), intent.PayAsset, network)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if !strings.EqualFold(payAsset.ContractAddress, intent.PayTokenContract) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "contrato do payAsset diverge do intent"})
		return
	}
	receipt, err := s.verifyERC20TransferTx(r.Context(), network, req.TxHash, intent.PayTokenContract, intent.AgentWallet, intent.PaymentAddress, intent.PayAmount, intent.PayAsset, payAsset.Decimals, nil)
	if err != nil {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{"error": err.Error(), "status": "payment_not_verified"})
		return
	}
	locked, err := s.db.ConfirmAgentTradePayment(r.Context(), intent.ID, receipt, req.IdempotencyKey)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if locked.Status == database.AgentTradeStatusSettled {
		writeJSON(w, http.StatusOK, map[string]any{"status": "settled", "tradeIntent": locked})
		return
	}
	settlementTx, err := s.sendAgentTradeSettlement(r.Context(), locked, req.IdempotencyKey)
	if err != nil {
		_ = s.db.FailAgentTradeSettlement(r.Context(), intent.ID)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error(), "status": "settlement_failed"})
		return
	}
	settled, err := s.db.CompleteAgentTradeSettlement(r.Context(), intent.ID, settlementTx)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "settled",
		"tradeIntent":      settled,
		"settlementTxHash": settlementTx,
	})
}

func (s *Server) handleAgentTradeGet(w http.ResponseWriter, r *http.Request) {
	intent, err := s.db.GetAgentTradeIntent(r.Context(), strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	if intent == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "trade intent nao encontrado"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tradeIntent": intent})
}

func (s *Server) agentTradeAssets(ctx context.Context) ([]*database.AgentSupportedAsset, error) {
	if s.db != nil {
		assets, err := s.db.ListAgentSupportedAssets(ctx)
		if err == nil && len(assets) > 0 {
			active := assets[:0]
			for _, asset := range assets {
				s.normalizeAgentTradeAsset(asset)
				// Filter out legacy/disabled assets from the public listing.
				if asset.Enabled && !strings.EqualFold(asset.Status, "legacy") {
					active = append(active, asset)
				}
			}
			return active, nil
		}
		if err != nil {
			return nil, err
		}
	}
	// Fallback: only return enabled, non-legacy assets.
	var active []*database.AgentSupportedAsset
	for _, a := range s.fallbackAgentTradeAssets() {
		if a.Enabled && !strings.EqualFold(a.Status, "legacy") {
			active = append(active, a)
		}
	}
	return active, nil
}

func (s *Server) agentTradeAsset(ctx context.Context, symbol string, network string) (*database.AgentSupportedAsset, error) {
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	network = normalizeStablecoinNetwork(network)
	if s.db != nil {
		asset, err := s.db.GetAgentSupportedAsset(ctx, normalized, network)
		if err != nil {
			return nil, err
		}
		if asset != nil {
			// GetAgentSupportedAsset already filters enabled=true at DB level,
			// but double-check legacy status for defence-in-depth.
			if !asset.Enabled || strings.EqualFold(asset.Status, "legacy") {
				return nil, fmt.Errorf("asset %s esta desabilitado (status: %s); consulte GET /agent/v1/assets", normalized, asset.Status)
			}
			s.normalizeAgentTradeAsset(asset)
			return asset, nil
		}
	}
	// Fallback (DB unreachable): reject legacy/disabled assets explicitly.
	for _, asset := range s.fallbackAgentTradeAssets() {
		if asset.Symbol != normalized || !strings.EqualFold(asset.Network, network) {
			continue
		}
		if !asset.Enabled || strings.EqualFold(asset.Status, "legacy") {
			return nil, fmt.Errorf("asset %s nao esta disponivel para trading (status: %s)", normalized, asset.Status)
		}
		return asset, nil
	}
	return nil, fmt.Errorf("asset nao suportado no rail M2M: consulte GET /agent/v1/assets")
}

func (s *Server) fallbackAgentTradeAssets() []*database.AgentSupportedAsset {
	usdt := ""
	if s.cfg != nil {
		usdt = strings.ToLower(strings.TrimSpace(s.cfg.BscUsdtContract))
	}
	if usdt == "" {
		usdt = bscUSDTContract
	}
	now := time.Now().UTC()
	return []*database.AgentSupportedAsset{
		{Symbol: "USDC", Network: "BSC", ContractAddress: bscUSDCContract, Decimals: 18, FeeBps: agentGatewayFeeBps, MinAmount: 5, Status: "active", Enabled: true, CreatedAt: now},
		{Symbol: "USDT", Network: "BSC", ContractAddress: usdt, Decimals: 18, FeeBps: agentGatewayFeeBps, MinAmount: 5, Status: "active", Enabled: true, CreatedAt: now},
		{Symbol: "BUSD", Network: "BSC", ContractAddress: bscBUSDContract, Decimals: 18, FeeBps: agentGatewayFeeBps, MinAmount: 5, Status: "legacy", Enabled: false, CreatedAt: now},
	}
}

func (s *Server) normalizeAgentTradeAsset(asset *database.AgentSupportedAsset) {
	asset.Symbol = strings.ToUpper(strings.TrimSpace(asset.Symbol))
	asset.Network = strings.ToUpper(strings.TrimSpace(asset.Network))
	asset.ContractAddress = strings.ToLower(strings.TrimSpace(asset.ContractAddress))
	if s.cfg != nil && asset.Symbol == "USDT" {
		switch normalizeStablecoinNetwork(asset.Network) {
		case "POLYGON":
			if strings.TrimSpace(s.cfg.PolygonUsdtContract) != "" {
				asset.ContractAddress = strings.ToLower(strings.TrimSpace(s.cfg.PolygonUsdtContract))
			}
		default:
			if strings.TrimSpace(s.cfg.BscUsdtContract) != "" {
				asset.ContractAddress = strings.ToLower(strings.TrimSpace(s.cfg.BscUsdtContract))
			}
		}
	}
	if asset.FeeBps < agentGatewayFeeBps {
		asset.FeeBps = agentGatewayFeeBps
	}
	if strings.TrimSpace(asset.Status) == "" {
		asset.Status = "active"
	}
}

func calculateAgentTradeAmounts(amount float64, amountType string, feeBps int) (agentTradeAmounts, error) {
	if amount <= 0 {
		return agentTradeAmounts{}, fmt.Errorf("amount deve ser maior que zero")
	}
	if feeBps <= 0 || feeBps >= 10000 {
		return agentTradeAmounts{}, fmt.Errorf("feeBps invalido")
	}
	amountUnits := money.TokenFromFloat(amount)
	var payUnits, receiveUnits money.TokenUnits
	switch amountType {
	case "pay":
		payUnits = amountUnits
		receiveUnits = payUnits - money.TokenFeeBps(payUnits, feeBps)
	case "receive", "":
		receiveUnits = amountUnits
		payUnits = money.GrossForNetToken(receiveUnits, feeBps)
	default:
		return agentTradeAmounts{}, fmt.Errorf("amountType deve ser pay ou receive")
	}
	feeUnits := payUnits - receiveUnits
	return agentTradeAmounts{PayAmount: payUnits.Float64(), ReceiveAmount: receiveUnits.Float64(), ChainFXFeeAmount: feeUnits.Float64(), FeeCalculation: "deducted_from_gross_payment"}, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func agentTradeQuoteResponse(intent *database.AgentTradeIntent, base string) map[string]any {
	return map[string]any{
		"tradeIntentId":        intent.ID,
		"payAsset":             intent.PayAsset,
		"receiveAsset":         intent.ReceiveAsset,
		"payAmount":            fmt.Sprintf("%.6f", intent.PayAmount),
		"receiveAmount":        fmt.Sprintf("%.6f", intent.ReceiveAmount),
		"chainfxFeeAmount":     fmt.Sprintf("%.6f", intent.ChainFXFeeAmount),
		"feeBps":               intent.FeeBps,
		"feeCalculation":       "deducted_from_gross_payment",
		"feeFormula":           "receiveAmount = payAmount * (1 - feeBps/10000)",
		"overpaymentPolicy":    "amount greater than required is accepted, recorded as overpaymentAmount, and not automatically refunded",
		"network":              intent.Network,
		"paymentAddress":       intent.PaymentAddress,
		"destinationWallet":    intent.DestinationWallet,
		"payTokenContract":     intent.PayTokenContract,
		"receiveTokenContract": intent.ReceiveTokenContract,
		"nonce":                intent.Nonce,
		"requestHash":          intent.RequestHash,
		"expiresAt":            intent.ExpiresAt,
		"executeUrl":           base + "/agent/v1/trade/execute",
		"security": []string{
			"transfer must be ERC20 on the quoted network from agentWallet to paymentAddress",
			"requestHash binds wallets, assets, token contracts, amounts, nonce and expiry",
			"txHash and idempotencyKey cannot be reused",
			"ChainFX settles receiveAsset only after on-chain payment verification",
		},
	}
}

func (s *Server) sendAgentTradeSettlement(ctx context.Context, intent *database.AgentTradeIntent, idempotencyKey string) (string, error) {
	if s.cfg.AllowSimulations && !s.cfg.IsProduction() && (s.cfg.SignerUrl == "" || s.cfg.SignerHmacSecret == "") {
		return "simulated-agent-trade-" + strings.ReplaceAll(intent.ID, "-", ""), nil
	}
	if strings.TrimSpace(s.cfg.SignerUrl) == "" || strings.TrimSpace(s.cfg.SignerHmacSecret) == "" {
		return "", fmt.Errorf("SIGNER_URL e SIGNER_HMAC_SECRET sao obrigatorios para liquidar trade M2M")
	}
	payload := map[string]any{
		"to":             intent.DestinationWallet,
		"amount":         fmt.Sprintf("%.8f", intent.ReceiveAmount),
		"tokenContract":  intent.ReceiveTokenContract,
		"network":        strings.ToLower(intent.Network),
		"idempotencyKey": "agent-trade-" + intent.ID + "-" + strings.TrimSpace(idempotencyKey),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.SignerUrl, "/")+"/hd/transfer", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	security.SignRawBodyHeaders(req, s.cfg.SignerHmacSecret, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		TxHash string `json:"txHash"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		if out.Error != "" {
			return "", fmt.Errorf("signer recusou settlement: %s", out.Error)
		}
		return "", fmt.Errorf("signer recusou settlement com status %d", resp.StatusCode)
	}
	if strings.TrimSpace(out.TxHash) == "" {
		return "", fmt.Errorf("signer nao retornou txHash")
	}
	return strings.ToLower(strings.TrimSpace(out.TxHash)), nil
}

func (s *Server) verifyERC20TransferTx(ctx context.Context, network, txHash, tokenContract, fromAddress, toAddress string, amount float64, asset string, decimals int, expectedLogIndex *int) (database.AgentTradeReceipt, error) {
	return s.verifyERC20TransferTxRaw(ctx, network, txHash, tokenContract, fromAddress, toAddress, amountToBaseUnits(amount, decimals), asset, decimals, expectedLogIndex)
}

func (s *Server) verifyERC20TransferTxRaw(ctx context.Context, network, txHash, tokenContract, fromAddress, toAddress string, expected *big.Int, asset string, decimals int, expectedLogIndex *int) (database.AgentTradeReceipt, error) {
	network = normalizeStablecoinNetwork(network)
	expectedChainID := stablecoinNetworkChainID(network)
	rpcURL := firstCSV(s.stablecoinRPCURLs(network))
	if rpcURL == "" {
		return database.AgentTradeReceipt{}, fmt.Errorf("%s nao configurado para verificar pagamento", stablecoinNetworkRPCEnvName(network))
	}
	if expected == nil || expected.Sign() <= 0 {
		return database.AgentTradeReceipt{}, fmt.Errorf("amount interno invalido")
	}
	if !common.IsHexAddress(fromAddress) || !common.IsHexAddress(toAddress) || !common.IsHexAddress(tokenContract) {
		return database.AgentTradeReceipt{}, fmt.Errorf("wallet, paymentAddress ou tokenContract invalido")
	}
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return database.AgentTradeReceipt{}, fmt.Errorf("falha ao conectar RPC %s: %w", network, err)
	}
	defer client.Close()
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return database.AgentTradeReceipt{}, fmt.Errorf("falha ao validar chainId: %w", err)
	}
	if chainID.Int64() != expectedChainID {
		return database.AgentTradeReceipt{}, fmt.Errorf("chainId invalido: esperado %d %s, recebido %d", expectedChainID, network, chainID.Int64())
	}
	receipt, err := client.TransactionReceipt(ctx, common.HexToHash(txHash))
	if err != nil {
		if err == ethereum.NotFound {
			return database.AgentTradeReceipt{}, fmt.Errorf("tx nao encontrada na %s", network)
		}
		return database.AgentTradeReceipt{}, fmt.Errorf("falha ao buscar receipt: %w", err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return database.AgentTradeReceipt{}, fmt.Errorf("tx %s sem sucesso", network)
	}
	latest, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return database.AgentTradeReceipt{}, fmt.Errorf("falha ao validar bloco mais recente: %w", err)
	}
	if latest.Number == nil || receipt.BlockNumber == nil || latest.Number.Cmp(receipt.BlockNumber) <= 0 {
		return database.AgentTradeReceipt{}, fmt.Errorf("pagamento aguardando pelo menos 1 confirmacao")
	}
	header, err := client.HeaderByNumber(ctx, receipt.BlockNumber)
	if err != nil {
		return database.AgentTradeReceipt{}, fmt.Errorf("falha ao validar bloco do pagamento: %w", err)
	}
	if header.Hash() != receipt.BlockHash {
		return database.AgentTradeReceipt{}, fmt.Errorf("blockHash do pagamento nao esta canonico")
	}
	from := common.HexToAddress(fromAddress)
	to := common.HexToAddress(toAddress)
	token := common.HexToAddress(tokenContract)
	for _, lg := range receipt.Logs {
		if expectedLogIndex != nil && int(lg.Index) != *expectedLogIndex {
			continue
		}
		if lg.Address != token || len(lg.Topics) < 3 || lg.Topics[0] != erc20TransferTopic {
			continue
		}
		if topicAddress(lg.Topics[1]) != from || topicAddress(lg.Topics[2]) != to {
			continue
		}
		paid := new(big.Int).SetBytes(lg.Data)
		if paid.Cmp(expected) >= 0 {
			overpayment := baseUnitsToAmount(new(big.Int).Sub(new(big.Int).Set(paid), expected), decimals)
			return database.AgentTradeReceipt{
				ChainID:           chainID.Int64(),
				TxHash:            strings.ToLower(strings.TrimSpace(txHash)),
				LogIndex:          int(lg.Index),
				BlockNumber:       receipt.BlockNumber.Uint64(),
				BlockHash:         strings.ToLower(receipt.BlockHash.Hex()),
				TokenContract:     strings.ToLower(tokenContract),
				TransferFrom:      strings.ToLower(from.Hex()),
				TransferTo:        strings.ToLower(to.Hex()),
				TransferAmountRaw: paid.String(),
				OverpaymentAmount: overpayment,
			}, nil
		}
	}
	return database.AgentTradeReceipt{}, fmt.Errorf("tx nao contem Transfer %s suficiente para o trade", strings.ToUpper(asset))
}

func decimalStringToBaseUnits(amount string, decimals int) (*big.Int, error) {
	if decimals < 0 {
		decimals = 18
	}
	text := strings.TrimSpace(amount)
	if text == "" {
		return nil, fmt.Errorf("amount vazio")
	}
	if strings.HasPrefix(text, "-") {
		return nil, fmt.Errorf("amount negativo")
	}
	parts := strings.SplitN(text, ".", 2)
	if parts[0] == "" {
		parts[0] = "0"
	}
	whole := new(big.Int)
	if _, ok := whole.SetString(parts[0], 10); !ok {
		return nil, fmt.Errorf("amount invalido")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole.Mul(whole, scale)
	frac := big.NewInt(0)
	if len(parts) == 2 && decimals > 0 {
		f := parts[1]
		if len(f) > decimals {
			f = f[:decimals]
		}
		f += strings.Repeat("0", decimals-len(f))
		if strings.TrimLeft(f, "0") != "" {
			if _, ok := frac.SetString(f, 10); !ok {
				return nil, fmt.Errorf("amount invalido")
			}
		}
	}
	return whole.Add(whole, frac), nil
}

func amountToBaseUnits(amount float64, decimals int) *big.Int {
	if decimals < 0 {
		decimals = 18
	}
	text := fmt.Sprintf("%.8f", amount)
	parts := strings.SplitN(text, ".", 2)
	whole := new(big.Int)
	whole.SetString(parts[0], 10)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole.Mul(whole, scale)
	frac := big.NewInt(0)
	if len(parts) == 2 && decimals > 0 {
		f := parts[1]
		if len(f) > decimals {
			f = f[:decimals]
		}
		f += strings.Repeat("0", decimals-len(f))
		frac.SetString(strings.TrimLeft(firstNonEmpty(f, "0"), "0"), 10)
	}
	return whole.Add(whole, frac)
}

func baseUnitsToAmount(amount *big.Int, decimals int) float64 {
	if amount == nil || amount.Sign() <= 0 {
		return 0
	}
	rat := new(big.Rat).SetInt(amount)
	rat.Quo(rat, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)))
	value, _ := rat.Float64()
	return math.Round(value*1_000_000) / 1_000_000
}

func agentTradeRequestHash(agentWallet, destinationWallet, payAsset, receiveAsset, network, payToken, receiveToken string, payAmount, receiveAmount float64, paymentAddress, nonce string, expiresAt time.Time) string {
	raw := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(agentWallet)),
		strings.ToLower(strings.TrimSpace(destinationWallet)),
		strings.ToUpper(strings.TrimSpace(payAsset)),
		strings.ToUpper(strings.TrimSpace(receiveAsset)),
		normalizeStablecoinNetwork(network),
		strings.ToLower(strings.TrimSpace(payToken)),
		strings.ToLower(strings.TrimSpace(receiveToken)),
		fmt.Sprintf("%.6f", payAmount),
		fmt.Sprintf("%.6f", receiveAmount),
		strings.ToLower(strings.TrimSpace(paymentAddress)),
		strings.TrimSpace(nonce),
		expiresAt.UTC().Format(time.RFC3339Nano),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
