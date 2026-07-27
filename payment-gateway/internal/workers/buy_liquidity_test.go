package workers

import (
	"context"
	"errors"
	"testing"

	"payment-gateway/internal/config"
	"payment-gateway/internal/database"
	"payment-gateway/internal/liquidity"
)

func TestResolveLiquidityPairRequiresContractsForEVMTokens(t *testing.T) {
	cfg := &config.Config{
		LiquidityAllowedPairs: "AVAX:BSC,AVAX:POLYGON:0x0000000000000000000000000000000000000001:18,BNB:BSC::18,BTC:BITCOIN::8",
	}

	if _, ok := resolveLiquidityPair(cfg, "AVAX", "BSC"); ok {
		t.Fatal("expected AVAX/BSC without contract to be blocked")
	}
	avaxPolygon, ok := resolveLiquidityPair(cfg, "AVAX", "POLYGON")
	if !ok {
		t.Fatal("expected AVAX/POLYGON with contract to be allowed")
	}
	if avaxPolygon.ContractAddress == "" || avaxPolygon.Decimals != 18 {
		t.Fatalf("unexpected AVAX/POLYGON pair: %+v", avaxPolygon)
	}
	if _, ok := resolveLiquidityPair(cfg, "AVAX", "AVALANCHE"); ok {
		t.Fatal("expected AVAX native network to be blocked")
	}
	if _, ok := resolveLiquidityPair(cfg, "BNB", "BSC"); !ok {
		t.Fatal("expected native BNB/BSC to be allowed without contract")
	}
	btc, ok := resolveLiquidityPair(cfg, "BTC", "BITCOIN")
	if !ok {
		t.Fatal("expected native BTC/BITCOIN to be allowed without contract")
	}
	if btc.ContractAddress != "" || btc.Decimals != 8 {
		t.Fatalf("unexpected BTC/BITCOIN pair: %+v", btc)
	}
}

func TestResolveLiquidityPairHydratesConfiguredUSDTContracts(t *testing.T) {
	cfg := &config.Config{
		LiquidityAllowedPairs: "USDT:BSC,USDT:POLYGON",
		BscUsdtContract:       "0x55d398326f99059ff775485246999027b3197955",
		PolygonUsdtContract:   "0xc2132d05d31c914a87c6611c10748aeb04b58e8f",
	}

	bsc, ok := resolveLiquidityPair(cfg, "USDT", "BEP-20")
	if !ok {
		t.Fatal("expected USDT/BSC to be allowed")
	}
	if bsc.ContractAddress != cfg.BscUsdtContract || bsc.Decimals != 18 {
		t.Fatalf("unexpected USDT/BSC pair: %+v", bsc)
	}

	polygon, ok := resolveLiquidityPair(cfg, "USDT", "MATIC")
	if !ok {
		t.Fatal("expected USDT/POLYGON to be allowed")
	}
	if polygon.ContractAddress != cfg.PolygonUsdtContract || polygon.Decimals != 6 {
		t.Fatalf("unexpected USDT/POLYGON pair: %+v", polygon)
	}
}

func TestShouldUseLiquidityRouterKeepsUSDTOnHotWalletWhenBalanceIsSufficient(t *testing.T) {
	worker := &BuySendWorker{
		cfg: &config.Config{
			LiquidityHotWalletFirstAssets: "USDT",
			SignerNetwork:                 "bsc",
		},
		hotWalletHasBalance: func(context.Context, *database.BuyOrder, liquidity.Pair) (bool, error) {
			return true, nil
		},
	}
	pair := liquidity.Pair{Asset: "USDT", Network: "BSC", ContractAddress: "0x55d398326f99059ff775485246999027b3197955", Decimals: 18}
	if worker.shouldUseLiquidityRouter(context.Background(), &database.BuyOrder{ID: "buy-1", Asset: "USDT", Network: "BSC", CryptoAmount: 10}, pair) {
		t.Fatal("USDT com saldo suficiente deve continuar no fluxo hot wallet/signer")
	}
}

func TestShouldUseLiquidityRouterRoutesUSDTWhenHotWalletBalanceIsInsufficient(t *testing.T) {
	worker := &BuySendWorker{
		cfg: &config.Config{
			LiquidityHotWalletFirstAssets: "USDT",
			SignerNetwork:                 "bsc",
		},
		hotWalletHasBalance: func(context.Context, *database.BuyOrder, liquidity.Pair) (bool, error) {
			return false, nil
		},
	}
	pair := liquidity.Pair{Asset: "USDT", Network: "BSC", ContractAddress: "0x55d398326f99059ff775485246999027b3197955", Decimals: 18}
	if !worker.shouldUseLiquidityRouter(context.Background(), &database.BuyOrder{ID: "buy-1", Asset: "USDT", Network: "BSC", CryptoAmount: 10}, pair) {
		t.Fatal("USDT com saldo insuficiente deve cair no liquidity router")
	}
}

func TestShouldUseLiquidityRouterRoutesUSDTWhenHotWalletBalanceCannotBeChecked(t *testing.T) {
	worker := &BuySendWorker{
		cfg: &config.Config{
			LiquidityHotWalletFirstAssets: "USDT",
			SignerNetwork:                 "bsc",
		},
		hotWalletHasBalance: func(context.Context, *database.BuyOrder, liquidity.Pair) (bool, error) {
			return false, errors.New("rpc unavailable")
		},
	}
	pair := liquidity.Pair{Asset: "USDT", Network: "BSC", ContractAddress: "0x55d398326f99059ff775485246999027b3197955", Decimals: 18}
	if !worker.shouldUseLiquidityRouter(context.Background(), &database.BuyOrder{ID: "buy-1", Asset: "USDT", Network: "BSC", CryptoAmount: 10}, pair) {
		t.Fatal("USDT sem prova de saldo suficiente deve tentar liquidity router")
	}
}

func TestShouldUseLiquidityRouterRoutesNonHotWalletFirstAssets(t *testing.T) {
	worker := &BuySendWorker{
		cfg: &config.Config{
			LiquidityHotWalletFirstAssets: "USDT",
			SignerNetwork:                 "bsc",
		},
	}
	pair := liquidity.Pair{Asset: "BTC", Network: "BITCOIN", Decimals: 8}
	if !worker.shouldUseLiquidityRouter(context.Background(), &database.BuyOrder{ID: "buy-1", Asset: "BTC", Network: "BITCOIN", CryptoAmount: 0.01}, pair) {
		t.Fatal("BTC deve usar liquidity router quando o par for permitido")
	}
}
