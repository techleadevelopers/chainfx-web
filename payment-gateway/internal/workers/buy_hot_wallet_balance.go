package workers

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"payment-gateway/internal/database"
	"payment-gateway/internal/liquidity"
	rpcpool "payment-gateway/internal/rpc"
)

func (bw *BuySendWorker) shouldUseLiquidityRouter(ctx context.Context, buy *database.BuyOrder, pair liquidity.Pair) bool {
	if bw == nil || bw.cfg == nil || buy == nil {
		return false
	}
	if !containsStrictCSVFold(bw.cfg.LiquidityHotWalletFirstAssets, buy.Asset) {
		return true
	}
	if !bw.hotWalletDeliverySupported(pair) {
		return true
	}
	hasBalance, err := bw.hotWalletBalanceAvailable(ctx, buy, pair)
	if err != nil {
		return true
	}
	return !hasBalance
}

func containsStrictCSVFold(raw, value string) bool {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, item := range splitCSV(raw) {
		if strings.ToUpper(strings.TrimSpace(item)) == value {
			return true
		}
	}
	return false
}

func (bw *BuySendWorker) hotWalletDeliverySupported(pair liquidity.Pair) bool {
	pair = liquidity.EnrichPair(pair)
	if !strings.EqualFold(pair.Asset, "USDT") || !strings.EqualFold(pair.Network, "BSC") {
		return false
	}
	network := strings.ToUpper(strings.TrimSpace(pair.Network))
	signerNetwork := strings.ToUpper(strings.TrimSpace(bw.cfg.SignerNetwork))
	if signerNetwork == "" || signerNetwork == "EVM" || signerNetwork == "BINANCE" || signerNetwork == "BEP20" {
		signerNetwork = "BSC"
	}
	return network == "BSC" && signerNetwork == "BSC"
}

func (bw *BuySendWorker) hotWalletBalanceAvailable(ctx context.Context, buy *database.BuyOrder, pair liquidity.Pair) (bool, error) {
	if bw.hotWalletHasBalance != nil {
		return bw.hotWalletHasBalance(ctx, buy, pair)
	}
	return bw.queryHotWalletBalanceAvailable(ctx, buy, pair)
}

func (bw *BuySendWorker) queryHotWalletBalanceAvailable(ctx context.Context, buy *database.BuyOrder, pair liquidity.Pair) (bool, error) {
	wallet := bw.hotWalletAddressForNetwork(pair.Network)
	if !common.IsHexAddress(wallet) {
		return false, fmt.Errorf("hot wallet invalida para %s", pair.Network)
	}
	needed, err := liquidityAmountRaw(buy.CryptoAmount, pair.Decimals)
	if err != nil {
		return false, err
	}
	if needed.Sign() <= 0 {
		return false, fmt.Errorf("amount invalido")
	}
	pool, err := rpcpool.NewPool(bw.rpcURLsForLiquidityNetwork(pair.Network))
	if err != nil {
		return false, err
	}
	var balance *big.Int
	if isNativeLiquidityPair(pair) {
		balance, err = pool.BalanceAt(ctx, common.HexToAddress(wallet))
	} else {
		balance, err = erc20BalanceOf(ctx, pool, wallet, pair.ContractAddress)
	}
	if err != nil {
		return false, err
	}
	return balance != nil && balance.Cmp(needed) >= 0, nil
}

func (bw *BuySendWorker) hotWalletAddressForNetwork(network string) string {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "POLYGON":
		return firstNonEmptyWorker(bw.cfg.PolygonTreasuryContract, bw.cfg.TreasuryHot)
	default:
		return firstNonEmptyWorker(bw.cfg.BscTreasuryContract, bw.cfg.TreasuryHot)
	}
}

func (bw *BuySendWorker) rpcURLsForLiquidityNetwork(network string) string {
	switch strings.ToUpper(strings.TrimSpace(network)) {
	case "POLYGON":
		return bw.cfg.PolygonRpcUrls
	case "BASE":
		return bw.cfg.BaseRpcUrls
	case "ARBITRUM":
		return bw.cfg.ArbitrumRpcUrls
	case "ETHEREUM":
		return bw.cfg.EthereumRpcUrls
	default:
		return bw.cfg.BscRpcUrls
	}
}

func liquidityAmountRaw(amount float64, decimals int) (*big.Int, error) {
	if amount <= 0 {
		return nil, fmt.Errorf("amount invalido")
	}
	if decimals < 0 {
		return nil, fmt.Errorf("decimals invalidos")
	}
	rat, ok := new(big.Rat).SetString(strconv.FormatFloat(amount, 'f', decimals, 64))
	if !ok {
		return nil, fmt.Errorf("amount invalido")
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	rat.Mul(rat, new(big.Rat).SetInt(scale))
	out := new(big.Int).Quo(rat.Num(), rat.Denom())
	return out, nil
}

func erc20BalanceOf(ctx context.Context, pool *rpcpool.Pool, wallet, tokenContract string) (*big.Int, error) {
	if !common.IsHexAddress(wallet) || !common.IsHexAddress(tokenContract) {
		return nil, fmt.Errorf("endereco EVM invalido")
	}
	walletAddr := common.HexToAddress(wallet)
	tokenAddr := common.HexToAddress(tokenContract)
	var callData [36]byte
	selector, _ := hex.DecodeString("70a08231")
	copy(callData[:4], selector)
	copy(callData[16:], walletAddr.Bytes())

	var result []byte
	err := pool.Do(ctx, func(c *ethclient.Client) error {
		var raw string
		msg := map[string]string{
			"to":   tokenAddr.Hex(),
			"data": "0x" + hex.EncodeToString(callData[:]),
		}
		if err := c.Client().CallContext(ctx, &raw, "eth_call", msg, "latest"); err != nil {
			return err
		}
		raw = strings.TrimPrefix(raw, "0x")
		if raw == "" {
			result = nil
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
