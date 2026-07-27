package paymaster

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"payment-gateway/internal/rpc"
)

const (
	// basisPointDivisor is 10_000 — BPS denominator.
	basisPointDivisor = 10_000
	// usdtDecimals is the USDT token decimal places (6 on BSC).
	usdtDecimals = 6
)

// GasEstimate holds the result of a gas cost estimation.
type GasEstimate struct {
	GasPriceWei  *big.Int // current network gas price (wei)
	GasLimit     uint64   // estimated gas units
	CostWei      *big.Int // GasPriceWei × GasLimit
	CostUSDT     *big.Int // CostWei converted to micro-USDT (6 decimals)
	FeeUSDT      *big.Int // CostUSDT + surcharge (micro-USDT)
	NativeSymbol string   // "BNB" or "POL"
}

// Estimator computes gas costs for relay transactions.
type Estimator struct {
	pool         *rpc.Pool
	oracle       *PriceOracle
	surchargeBps int // extra fee margin, e.g. 1000 = 10%
	maxFeeUsdt   int64 // cap in micro-USDT
	minFeeUsdt   int64 // floor in micro-USDT
}

// NewEstimator creates an Estimator.
func NewEstimator(pool *rpc.Pool, oracle *PriceOracle, surchargeBps int, maxFeeUsdt, minFeeUsdt float64) *Estimator {
	return &Estimator{
		pool:         pool,
		oracle:       oracle,
		surchargeBps: surchargeBps,
		maxFeeUsdt:   int64(maxFeeUsdt * 1_000_000),
		minFeeUsdt:   int64(minFeeUsdt * 1_000_000),
	}
}

// EstimateRelay estimates the USDT fee for relaying txData on behalf of from → to.
func (e *Estimator) EstimateRelay(ctx context.Context, from, to common.Address, txData []byte) (*GasEstimate, error) {
	var gasPrice *big.Int
	var gasLimit uint64

	err := e.pool.Do(ctx, func(c *ethclient.Client) error {
		var err error
		gasPrice, err = c.SuggestGasPrice(ctx)
		if err != nil {
			return fmt.Errorf("SuggestGasPrice: %w", err)
		}

		msg := ethereum.CallMsg{
			From: from,
			To:   &to,
			Data: txData,
		}
		gasLimit, err = c.EstimateGas(ctx, msg)
		if err != nil {
			// Fallback to a safe default for ERC-20 transfers when estimation fails.
			gasLimit = 65_000
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("estimator: rpc error: %w", err)
	}

	// Add 20% buffer to gas limit to handle estimation inaccuracies.
	gasLimit = gasLimit * 120 / 100

	// Cost in wei = gasPrice × gasLimit
	costWei := new(big.Int).Mul(gasPrice, new(big.Int).SetUint64(gasLimit))

	// Convert wei cost to USDT using the BNB/USD oracle.
	bnbPrice, err := e.oracle.BNBPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("estimator: oracle: %w", err)
	}

	// costUSDT (micro) = costWei × bnbPrice × 1e6 / 1e18
	// = costWei × bnbPrice / 1e12
	// Use big.Int arithmetic to avoid float precision loss.
	bnbPriceMicro := new(big.Int).SetInt64(int64(bnbPrice * 1_000_000))
	// costUSDT_micro = costWei * bnbPriceMicro / 1e18
	costUSDTMicro := new(big.Int).Mul(costWei, bnbPriceMicro)
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	costUSDTMicro.Div(costUSDTMicro, divisor)

	// Apply surcharge: feeUSDT_micro = costUSDT_micro × (10000 + surchargeBps) / 10000
	numerator := new(big.Int).Mul(costUSDTMicro, big.NewInt(int64(basisPointDivisor+e.surchargeBps)))
	feeUSDTMicro := new(big.Int).Div(numerator, big.NewInt(basisPointDivisor))

	// Enforce floor and cap.
	if e.minFeeUsdt > 0 && feeUSDTMicro.Cmp(big.NewInt(e.minFeeUsdt)) < 0 {
		feeUSDTMicro = big.NewInt(e.minFeeUsdt)
	}
	if e.maxFeeUsdt > 0 && feeUSDTMicro.Cmp(big.NewInt(e.maxFeeUsdt)) > 0 {
		feeUSDTMicro = big.NewInt(e.maxFeeUsdt)
	}

	return &GasEstimate{
		GasPriceWei:  gasPrice,
		GasLimit:     gasLimit,
		CostWei:      costWei,
		CostUSDT:     costUSDTMicro,
		FeeUSDT:      feeUSDTMicro,
		NativeSymbol: "BNB",
	}, nil
}

// FeeUSDTFloat returns the fee as a human-readable float64.
func (est *GasEstimate) FeeUSDTFloat() float64 {
	f, _ := new(big.Float).Quo(
		new(big.Float).SetInt(est.FeeUSDT),
		big.NewFloat(1_000_000),
	).Float64()
	return f
}
